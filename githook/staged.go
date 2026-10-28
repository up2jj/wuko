package githook

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/up2jj/wuko/internal/filelock"
	"github.com/up2jj/wuko/internal/yamlstrict"
)

// StagedGuard temporarily hides unstaged tracked changes in one worktree. Cleanup restores
// them after the caller finishes, including after cancellation. Untracked files and submodules
// are left alone. The guard must surround workflow loading as well as execution.
type StagedGuard struct {
	repository Repository
	lock       *filelock.Handle
	base       string
	tree       string
	directory  string
	// index is a private copy of the index with assume-unchanged and skip-worktree flags
	// cleared, used when such entries exist so their worktree edits are hidden too.
	index      string
	paths      []byte
	hidden     bool
	prepared   bool
	once       sync.Once
	cleanupErr error
}

// IsolateStaged acquires a worktree-local guard and replaces unstaged tracked files with their
// indexed contents. It waits for an overlapping run in the same worktree to finish, honoring
// ctx. A failed preparation restores any changes it already hid before returning.
func IsolateStaged(ctx context.Context, repository Repository) (_ *StagedGuard, err error) {
	directory := filepath.Join(repository.GitDir, "wuko")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("preparing staged isolation: %w", err)
	}
	lock, err := filelock.Acquire(ctx, filepath.Join(directory, "staged.lock"))
	if err != nil {
		return nil, fmt.Errorf("locking staged isolation: %w", err)
	}
	guard := &StagedGuard{repository: repository, lock: lock, base: directory}
	defer func() {
		if err != nil {
			err = errors.Join(err, guard.Cleanup(ctx))
		}
	}()
	recovery := filepath.Join(directory, "staged")
	if _, err := os.Lstat(recovery); err == nil {
		return nil, fmt.Errorf("unfinished staged isolation at %s; recover the saved unstaged changes before retrying", recovery)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("checking staged isolation recovery: %w", err)
	}
	if err := guard.prepareIndex(ctx); err != nil {
		return nil, err
	}
	// One cheap name-status pass both detects intent-to-add entries and lists the paths to
	// hide, so a clean worktree never generates a patch.
	status, err := guard.git(ctx, nil, "diff", "--name-status", "--no-ext-diff", "--no-renames", "--no-relative", "--ignore-submodules", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("listing unstaged changes: %w", err)
	}
	paths, intent := parseNameStatus(status)
	// An unmerged or intent-to-add index cannot be a complete proposed commit. Refuse it
	// before hiding anything rather than dropping entries or altering their index flags.
	if intent {
		return nil, fmt.Errorf("staged isolation requires intent-to-add files to be staged or removed from the index first")
	}
	tree, err := guard.writeTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("capturing staged tree (resolve any index conflicts first): %w", err)
	}
	guard.tree = tree
	if len(paths) != 0 {
		guard.paths = paths
		if err := guard.checkRestorePaths(); err != nil {
			return nil, err
		}
		if err := os.Mkdir(recovery, 0o700); err != nil {
			return nil, fmt.Errorf("creating staged isolation recovery: %w", err)
		}
		guard.directory = recovery
		if err := guard.save("tree", []byte(guard.tree+"\n")); err != nil {
			return nil, err
		}
		// Stream the patch straight to disk: binary changes can be large, and holding them
		// in memory for the whole hook run is unnecessary when Cleanup applies the file.
		size, err := guard.savePatch(ctx)
		if err != nil {
			return nil, err
		}
		if size != 0 {
			// Set this before checkout: Git may update some files before another path fails.
			guard.hidden = true
			if _, err := guard.git(ctx, guard.paths, "checkout-index", "--force", "-z", "--stdin"); err != nil {
				return nil, fmt.Errorf("hiding unstaged changes: %w", err)
			}
		}
	}
	guard.prepared = true
	return guard, nil
}

// Cleanup restores saved changes once and releases the guard. It reports hook modifications
// even when every check exited successfully. On a patch conflict, saved user changes take
// precedence over hook edits to those paths. A failed restoration retains its recovery files.
func (guard *StagedGuard) Cleanup(ctx context.Context) error {
	guard.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		guard.cleanupErr = guard.cleanup(ctx)
		guard.cleanupErr = errors.Join(guard.cleanupErr, guard.lock.Release())
	})
	return guard.cleanupErr
}

func (guard *StagedGuard) cleanup(ctx context.Context) error {
	var checkErr error
	if guard.prepared {
		index, err := guard.writeTree(ctx)
		if err != nil {
			checkErr = fmt.Errorf("checking staged tree after hooks: %w", err)
		} else if index != guard.tree {
			checkErr = fmt.Errorf("Git hook modified the index; review the staged changes before committing")
		}
		// --quiet stops at the first difference instead of listing every modified path.
		modified, err := guard.differs(ctx, "diff", "--quiet", "--no-ext-diff", "--no-renames", "--no-relative", "--ignore-submodules", guard.tree, "--")
		if err != nil {
			checkErr = errors.Join(checkErr, fmt.Errorf("checking hook file changes: %w", err))
		} else if modified {
			checkErr = errors.Join(checkErr, fmt.Errorf("files were modified by the Git hook; review and stage the changes before committing"))
		}
	}
	if guard.hidden {
		patch := filepath.Join(guard.directory, "unstaged.patch")
		if _, err := guard.git(ctx, nil, "apply", "--binary", "--whitespace=nowarn", patch); err != nil {
			// Applying a patch without --reject is atomic. Restore only the saved patch's
			// paths to their original staged baseline, then retry, keeping unrelated fixes.
			// Hook edits on those paths are overwritten, so save them all for review first.
			checkErr = errors.Join(checkErr, guard.saveHookEdits(ctx))
			if pathErr := guard.checkRestorePaths(); pathErr != nil {
				return errors.Join(checkErr, guard.recoveryError(pathErr))
			}
			_, resetErr := guard.git(ctx, guard.paths, "restore", "--source="+guard.tree, "--worktree", "--pathspec-from-file=-", "--pathspec-file-nul")
			if resetErr != nil {
				return errors.Join(checkErr, guard.recoveryError(errors.Join(err, resetErr)))
			}
			if _, err := guard.git(ctx, nil, "apply", "--binary", "--whitespace=nowarn", patch); err != nil {
				return errors.Join(checkErr, guard.recoveryError(err))
			}
		}
	}
	if guard.directory != "" {
		if err := os.RemoveAll(guard.directory); err != nil {
			return errors.Join(checkErr, fmt.Errorf("removing staged isolation recovery: %w", err))
		}
	}
	if guard.index != "" {
		if err := os.Remove(guard.index); err != nil && !os.IsNotExist(err) {
			checkErr = errors.Join(checkErr, fmt.Errorf("removing staged isolation index: %w", err))
		}
	}
	return checkErr
}

// saveHookEdits writes every hook edit, relative to the staged tree, outside the recovery
// directory so a later run neither refuses nor overwrites it before the user has looked.
func (guard *StagedGuard) saveHookEdits(ctx context.Context) error {
	path := filepath.Join(guard.base, "hook-edits.patch")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("saving Git hook edits: %w", err)
	}
	runErr := guard.run(ctx, guard.index, nil, file, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--ignore-submodules", "--src-prefix=a/", "--dst-prefix=b/", guard.tree, "--")
	if err := errors.Join(runErr, file.Close()); err != nil {
		return fmt.Errorf("saving Git hook edits: %w", err)
	}
	return fmt.Errorf("Git hook edits conflicted with unstaged changes; the unstaged changes were kept and the hook edits were saved to %s", path)
}

// Git restore may remove an obstructing directory or replace a parent symlink. Neither belongs
// to the saved tracked-file patch, so refuse such paths instead of losing untracked content.
func (guard *StagedGuard) checkRestorePaths() error {
	// Changed paths share parents, so remember directories already confirmed as real ones.
	directories := map[string]struct{}{}
	for _, path := range bytes.Split(guard.paths, []byte{0}) {
		if len(path) == 0 {
			continue
		}
		parts := strings.Split(string(path), "/")
		current := guard.repository.Root
		for index, part := range parts {
			current = filepath.Join(current, part)
			last := index == len(parts)-1
			if _, ok := directories[current]; ok && !last {
				continue
			}
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return fmt.Errorf("checking staged restore path %q: %w", current, err)
			}
			if (!last && !info.IsDir()) || (last && info.IsDir()) {
				return fmt.Errorf("cannot restore staged path %q without replacing a directory or parent symlink", string(path))
			}
			if !last {
				directories[current] = struct{}{}
			}
		}
	}
	return nil
}

func (guard *StagedGuard) recoveryError(err error) error {
	return fmt.Errorf("restoring unstaged changes failed; recovery patch retained at %s: %w", filepath.Join(guard.directory, "unstaged.patch"), err)
}

func (guard *StagedGuard) save(name string, content []byte) error {
	file, err := os.OpenFile(filepath.Join(guard.directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("saving staged isolation recovery: %w", err)
	}
	_, writeErr := file.Write(content)
	syncErr := file.Sync()
	return errors.Join(writeErr, syncErr, file.Close())
}

// savePatch writes the unstaged binary patch into the recovery directory and returns its size.
func (guard *StagedGuard) savePatch(ctx context.Context) (int64, error) {
	file, err := os.OpenFile(filepath.Join(guard.directory, "unstaged.patch"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("saving staged isolation recovery: %w", err)
	}
	runErr := guard.run(ctx, guard.index, nil, file, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--ignore-submodules", "--src-prefix=a/", "--dst-prefix=b/", "--")
	if runErr != nil {
		runErr = fmt.Errorf("saving unstaged changes: %w", runErr)
	}
	var size int64
	info, statErr := file.Stat()
	if statErr == nil {
		size = info.Size()
	}
	if err := errors.Join(runErr, statErr, file.Sync(), file.Close()); err != nil {
		return 0, err
	}
	return size, nil
}

// parseNameStatus splits `git diff --name-status -z` output into NUL-terminated paths and
// reports whether any entry is an addition, which only intent-to-add entries produce here.
func parseNameStatus(output []byte) (paths []byte, intent bool) {
	fields := bytes.Split(output, []byte{0})
	for index := 0; index+1 < len(fields); index += 2 {
		status, path := fields[index], fields[index+1]
		if len(status) == 0 || len(path) == 0 {
			continue
		}
		if status[0] == 'A' {
			intent = true
		}
		paths = append(append(paths, path...), 0)
	}
	return paths, intent
}

// prepareIndex points the guard at a private index copy when the index has assume-unchanged
// entries, or skip-worktree entries present in the worktree. Git diff and checkout-index
// skip those entries, so their edits would otherwise stay visible to checks and be neither
// hidden nor restored. The user's index and its flags are never changed.
func (guard *StagedGuard) prepareIndex(ctx context.Context) error {
	var assumed, skipped []byte
	err := guard.scan(ctx, func(entry []byte) error {
		// `ls-files -v` tags assume-unchanged entries in lowercase and skip-worktree as S.
		if len(entry) < 3 {
			return nil
		}
		tag, path := entry[0], entry[2:]
		if tag == 'S' || tag == 's' {
			// A sparse checkout leaves skip-worktree entries absent; those are not edits.
			if _, err := os.Lstat(filepath.Join(guard.repository.Root, filepath.FromSlash(string(path)))); err != nil {
				return nil
			}
			skipped = append(append(skipped, path...), 0)
		}
		if tag >= 'a' && tag <= 'z' {
			assumed = append(append(assumed, path...), 0)
		}
		return nil
	}, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("listing index flags: %w", err)
	}
	if len(assumed) == 0 && len(skipped) == 0 {
		return nil
	}
	source, err := guard.git(ctx, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return fmt.Errorf("locating index: %w", err)
	}
	original := strings.TrimSpace(string(source))
	if !filepath.IsAbs(original) {
		original = filepath.Join(guard.repository.Root, original)
	}
	copied := filepath.Join(guard.base, "staged.index")
	if err := copyFile(original, copied); err != nil {
		return fmt.Errorf("copying index for staged isolation: %w", err)
	}
	guard.index = copied
	// update-index applies only its last flag option, so clear each kind separately.
	for _, clear := range []struct {
		flag  string
		paths []byte
	}{{"--no-assume-unchanged", assumed}, {"--no-skip-worktree", skipped}} {
		if len(clear.paths) == 0 {
			continue
		}
		if _, err := guard.git(ctx, clear.paths, "update-index", clear.flag, "-z", "--stdin"); err != nil {
			return fmt.Errorf("clearing index flags for staged isolation: %w", err)
		}
	}
	return nil
}

func copyFile(source, target string) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	_, err = io.Copy(output, input)
	return err
}

// scan streams NUL-terminated Git output to visit without buffering it, since index listings
// grow with the repository.
func (guard *StagedGuard) scan(ctx context.Context, visit func([]byte) error, args ...string) error {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := guard.run(ctx, "", nil, writer, args...)
		writer.CloseWithError(err)
		done <- err
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if index := bytes.IndexByte(data, 0); index >= 0 {
			return index + 1, data[:index], nil
		}
		if atEOF && len(data) != 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var visitErr error
	for visitErr == nil && scanner.Scan() {
		visitErr = visit(scanner.Bytes())
	}
	// Drain the rest so Git is never left blocked on a full pipe.
	_, _ = io.Copy(io.Discard, reader)
	return errors.Join(visitErr, scanner.Err(), <-done)
}

// writeTree records the real index, never the private copy: the commit uses the real one.
func (guard *StagedGuard) writeTree(ctx context.Context) (string, error) {
	var output bytes.Buffer
	if err := guard.run(ctx, "", nil, &output, "write-tree"); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

// differs runs a --quiet Git comparison and reports whether it found a difference.
func (guard *StagedGuard) differs(ctx context.Context, args ...string) (bool, error) {
	err := guard.run(ctx, guard.index, nil, io.Discard, args...)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

func (guard *StagedGuard) git(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	// Internal index and restore operations must not trigger another Git hook. Keep Git's
	// inherited index environment: commits such as `git commit -a` use an alternate index.
	var output bytes.Buffer
	if err := guard.run(ctx, guard.index, input, &output, args...); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// run executes Git in the worktree. A non-empty index overrides the inherited index file.
func (guard *StagedGuard) run(ctx context.Context, index string, input []byte, stdout io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "git", append([]string{"--literal-pathspecs", "-c", "core.hooksPath=" + os.DevNull, "-C", guard.repository.Root}, args...)...)
	if index != "" {
		command.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
	}
	command.Stdin = bytes.NewReader(input)
	command.Stdout, command.Stderr = stdout, io.Discard
	if err := command.Run(); err != nil {
		// Patch contents may contain secrets. Diagnostics deliberately omit both the patch
		// and Git's stderr, which can quote its contents on an apply failure.
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// StagedManifestDeclares reports whether the manifest a staged-isolated run would load binds
// the hook: the indexed version when tracked, otherwise the worktree file. Isolation is only
// worth its cost, and its intent-to-add and recovery refusals, when a workflow will run. An
// unreadable or invalid manifest reports true so the normal loading path surfaces the error.
func StagedManifestDeclares(ctx context.Context, repository Repository, hook string) bool {
	guard := &StagedGuard{repository: repository}
	data, err := guard.git(ctx, nil, "cat-file", "blob", ":"+ManifestPath)
	if err != nil {
		data, err = os.ReadFile(filepath.Join(repository.Root, filepath.FromSlash(ManifestPath)))
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			return true
		}
	}
	var manifest Manifest
	if err := yamlstrict.Decode(data, &manifest); err != nil {
		return true
	}
	_, ok := manifest.Hooks[hook]
	return ok
}
