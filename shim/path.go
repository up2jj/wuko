package shim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/up2jj/wuko/internal/filelock"
	"github.com/up2jj/wuko/internal/fsutil"
)

// PathOptions supplies user locations explicitly so shell setup never depends on
// the process home or shell when embedded or tested.
type PathOptions struct {
	HomeDir       string
	ConfigDir     string
	Shell         string
	ZDotDir       string
	XDGConfigHome string
}

// PathSetup is a preflighted plan to append a shim directory to a shell's PATH.
// Apply rechecks the plan under the shared shell-configuration lock.
type PathSetup struct {
	directory string
	options   PathOptions
}

type pathEntry struct {
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Shell     string `json:"shell"`
}

type pathState struct {
	Version int         `json:"version"`
	Entries []pathEntry `json:"entries"`
}

type startupEdit struct {
	path string
	data []byte
	mode fs.FileMode
}

// PreparePath checks shell support, startup files and existing ownership before
// the caller installs a shim. It does not write anything.
func PreparePath(directory string, options PathOptions) (*PathSetup, error) {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(options.HomeDir) || !filepath.IsAbs(options.ConfigDir) {
		return nil, errors.New("shim PATH setup requires absolute bin, home, and configuration directories")
	}
	if strings.ContainsAny(directory, ":\x00") {
		return nil, errors.New("shim PATH directory cannot contain ':' or a NUL byte")
	}
	if options.Shell == "" {
		return nil, errors.New("cannot detect the shell for shim PATH setup; use --shell bash, zsh, or fish, or --no-path")
	}
	options.Shell = filepath.Base(options.Shell)
	setup := &PathSetup{directory: filepath.Clean(directory), options: options}
	_, _, err := setup.plan()
	if err != nil {
		return nil, err
	}
	return setup, nil
}

func pathStatePath(configDir string) string {
	return filepath.Join(configDir, "wuko", "shim-path.json")
}

func loadPathState(path string) (pathState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pathState{Version: 1}, nil
	}
	if err != nil {
		return pathState{}, fmt.Errorf("reading shim PATH ownership: %w", err)
	}
	var state pathState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decoding shim PATH ownership: %w", err)
	}
	if state.Version != 1 {
		return state, fmt.Errorf("unsupported shim PATH ownership version %d", state.Version)
	}
	seen := make(map[string]bool)
	for _, entry := range state.Entries {
		key := entry.Directory + "\x00" + entry.Path
		if !filepath.IsAbs(entry.Directory) || !filepath.IsAbs(entry.Path) || !slices.Contains([]string{"bash", "zsh", "fish"}, entry.Shell) || seen[key] {
			return state, errors.New("invalid shim PATH ownership entry")
		}
		seen[key] = true
	}
	return state, nil
}

func savePathState(path string, state pathState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o600)
}

func (setup *PathSetup) startupPaths() ([]string, error) {
	options := setup.options
	switch options.Shell {
	case "zsh":
		root := options.ZDotDir
		if root == "" {
			root = options.HomeDir
		}
		if !filepath.IsAbs(root) {
			return nil, errors.New("ZDOTDIR must be absolute for shim PATH setup")
		}
		return []string{filepath.Join(root, ".zshrc")}, nil
	case "bash":
		profile := filepath.Join(options.HomeDir, ".bash_profile")
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			candidate := filepath.Join(options.HomeDir, name)
			if _, err := os.Stat(candidate); err == nil {
				profile = candidate
				break
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
		return []string{filepath.Join(options.HomeDir, ".bashrc"), profile}, nil
	case "fish":
		root := options.XDGConfigHome
		if root == "" {
			root = filepath.Join(options.HomeDir, ".config")
		}
		if !filepath.IsAbs(root) {
			return nil, errors.New("XDG_CONFIG_HOME must be absolute for shim PATH setup")
		}
		return []string{filepath.Join(root, "fish", "config.fish")}, nil
	default:
		return nil, fmt.Errorf("unsupported shell %q for shim PATH setup; use --shell bash, zsh, or fish, or --no-path", options.Shell)
	}
}

func pathMarker(directory string) string {
	sum := sha256.Sum256([]byte(directory))
	return "wuko shim PATH " + hex.EncodeToString(sum[:8])
}

func (entry pathEntry) block() string {
	marker := pathMarker(entry.Directory)
	var body string
	if entry.Shell == "fish" {
		// Fish single quotes escape both backslashes and apostrophes.
		quoted := "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(entry.Directory) + "'"
		body = "if not contains -- " + quoted + " $PATH\n    set -gx PATH $PATH " + quoted + "\nend\n"
	} else {
		body = "case \":${PATH-}:\" in\n    *" + shellQuote(":"+entry.Directory+":") + "*) ;;\n    *) export PATH=\"${PATH:+$PATH:}\"" + shellQuote(entry.Directory) + " ;;\nesac\n"
	}
	return "\n# >>> " + marker + " >>>\n" + body + "# <<< " + marker + " <<<\n"
}

// readStartup follows a user's dotfile symlink and returns its target so atomic
// replacement preserves the link itself. A missing file can be created later.
func readStartup(path string) (startupEdit, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, linkErr := os.Lstat(path); linkErr == nil {
			return startupEdit{}, fmt.Errorf("startup file %s is a dangling symlink", path)
		}
		resolved, err := resolveStartupPath(path)
		return startupEdit{path: resolved, mode: 0o644}, err
	}
	if err != nil {
		return startupEdit{}, err
	}
	if !info.Mode().IsRegular() {
		return startupEdit{}, fmt.Errorf("startup file %s is not a regular file", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return startupEdit{}, err
	}
	data, err := os.ReadFile(resolved)
	return startupEdit{path: resolved, data: data, mode: info.Mode().Perm()}, err
}

// Resolve existing ancestors even before creating a dotfile, so ownership uses
// the same path before and after creation (including macOS /var symlinks).
func resolveStartupPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return resolved, err
	}
	parent, err := resolveStartupPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func (setup *PathSetup) plan() (pathState, []startupEdit, error) {
	paths, err := setup.startupPaths()
	if err != nil {
		return pathState{}, nil, err
	}
	state, err := loadPathState(pathStatePath(setup.options.ConfigDir))
	if err != nil {
		return state, nil, err
	}
	// Include previously recorded files to recover a partially completed setup.
	for _, entry := range state.Entries {
		if entry.Directory == setup.directory {
			paths = append(paths, entry.Path)
		}
	}
	seen := make(map[string]bool)
	var edits []startupEdit
	for _, path := range paths {
		edit, err := readStartup(path)
		if err != nil {
			return state, nil, fmt.Errorf("reading startup file %s: %w", path, err)
		}
		if seen[edit.path] {
			continue
		}
		seen[edit.path] = true
		entry := pathEntry{Directory: setup.directory, Path: edit.path, Shell: setup.options.Shell}
		owned := false
		for _, previous := range state.Entries {
			if previous.Directory == entry.Directory && previous.Path == entry.Path {
				entry, owned = previous, true
				break
			}
		}
		block := entry.block()
		content := string(edit.data)
		if strings.Contains(content, pathMarker(entry.Directory)) {
			if !owned || strings.Count(content, block) != 1 || strings.Count(content, pathMarker(entry.Directory)) != 2 {
				return state, nil, fmt.Errorf("PATH entry in %s was modified or is not owned by Wuko; leaving it unchanged", edit.path)
			}
			continue
		}
		if !owned {
			state.Entries = append(state.Entries, entry)
		}
		edit.data = append(edit.data, block...)
		edits = append(edits, edit)
	}
	return state, edits, nil
}

// Apply records ownership before editing startup files. If a write fails, the
// shim remains installed and both a retry and CleanupPath can recover safely.
func (setup *PathSetup) Apply(ctx context.Context) (resultErr error) {
	path := pathStatePath(setup.options.ConfigDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Release()) }()
	state, edits, err := setup.plan()
	if err != nil || len(edits) == 0 {
		return err
	}
	if err := savePathState(path, state); err != nil {
		return fmt.Errorf("recording shim PATH ownership: %w", err)
	}
	for _, edit := range edits {
		if err := os.MkdirAll(filepath.Dir(edit.path), 0o755); err != nil {
			return err
		}
		if err := fsutil.WriteFileAtomic(edit.path, edit.data, edit.mode); err != nil {
			return fmt.Errorf("updating PATH in %s: %w", edit.path, err)
		}
	}
	return nil
}

// CleanupPath removes unchanged owned blocks only after the last shim in a bin
// directory is gone. Modified blocks and their recovery records are preserved.
func CleanupPath(ctx context.Context, directory, configDir string) (resultErr error) {
	path := pathStatePath(configDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Release()) }()
	shims, err := List(directory)
	if err != nil || len(shims) != 0 {
		return err
	}
	state, err := loadPathState(path)
	if err != nil {
		return err
	}
	var remaining []pathEntry
	for _, entry := range state.Entries {
		if entry.Directory != directory {
			remaining = append(remaining, entry)
			continue
		}
		edit, readErr := readStartup(entry.Path)
		if readErr == nil {
			content := string(edit.data)
			if strings.Contains(content, pathMarker(directory)) {
				if strings.Count(content, entry.block()) != 1 || strings.Count(content, pathMarker(directory)) != 2 {
					readErr = fmt.Errorf("PATH entry in %s was modified; leaving it unchanged", entry.Path)
				} else {
					before, after, _ := strings.Cut(content, entry.block())
					// A user may append configuration after our block when the
					// original file had no final newline. Keep those lines apart.
					if before != "" && after != "" && !strings.HasSuffix(before, "\n") && !strings.HasPrefix(after, "\n") {
						before += "\n"
					}
					readErr = fsutil.WriteFileAtomic(edit.path, []byte(before+after), edit.mode)
				}
			}
		}
		if readErr != nil {
			remaining = append(remaining, entry)
			resultErr = errors.Join(resultErr, readErr)
		}
	}
	state.Entries = remaining
	return errors.Join(resultErr, savePathState(path, state))
}
