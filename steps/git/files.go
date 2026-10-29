package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

const (
	filesSourceAll = "all"
	// textSniffBytes matches the prefix Git inspects when it decides whether a file is binary.
	textSniffBytes = 8000
)

type filesConfig struct {
	Source  string             `yaml:"source"`
	Paths   []string           `yaml:"paths,omitempty"`
	Exclude []string           `yaml:"exclude,omitempty"`
	Updates *updatesExpression `yaml:"updates,omitempty"`
}

type filesRunner struct {
	config         filesConfig
	exclude        []*regexp.Regexp
	updatesProgram *vm.Program
}

// fileChange is one path's net change in the selected source. A path is a candidate unless
// its latest change deletes it.
type fileChange struct {
	added   bool
	deleted bool
}

// NewFiles builds a git_files step, which selects the files a check should inspect.
func NewFiles(raw map[string]any) (step.Runner, error) {
	var config filesConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	// A templated source, such as one chosen per workflow target, is checked once the run
	// builds the step again with rendered values; validation sees the template.
	if !strings.Contains(config.Source, "{{") {
		switch config.Source {
		case diffSourceStaged, diffSourceUnstaged, diffSourcePushed, filesSourceAll:
		case "":
			return nil, fmt.Errorf("source is required")
		default:
			return nil, fmt.Errorf("source must be staged, unstaged, pushed, or all")
		}
		if config.Source != diffSourcePushed && config.Updates != nil {
			return nil, fmt.Errorf("updates is only supported with source pushed")
		}
	}
	if err := validatePathspecs(config.Paths); err != nil {
		return nil, err
	}
	exclude := make([]*regexp.Regexp, 0, len(config.Exclude))
	for index, pattern := range config.Exclude {
		if strings.TrimSpace(pattern) == "" {
			return nil, fmt.Errorf("exclude[%d] must not be blank", index)
		}
		// Validation builds the step before templates render; the run builds it again.
		if strings.Contains(pattern, "{{") {
			continue
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("exclude[%d]: invalid pattern: %w", index, err)
		}
		exclude = append(exclude, compiled)
	}
	program, err := compileUpdates(config.Updates)
	if err != nil {
		return nil, err
	}
	return &filesRunner{config: config, exclude: exclude, updatesProgram: program}, nil
}

func (*filesRunner) ExecutorAware()      {}
func (*filesRunner) ExecutorFileSystem() {}

func (runner *filesRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := validateResolvedDiffValues(runner.config.Source, "", append(slices.Clone(runner.config.Paths), runner.config.Exclude...)); err != nil {
		return step.Result{}, err
	}
	changes, err := runner.changes(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	filesystem, err := executor.FileSystemFor(request.Executor)
	if err != nil {
		return step.Result{}, err
	}
	paths := make([]string, 0, len(changes))
	for path := range changes {
		if !runner.excluded(path) {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	files, text, binary, added, deleted := []any{}, []any{}, []any{}, []any{}, []any{}
	for _, path := range paths {
		change := changes[path]
		if change.deleted {
			deleted = append(deleted, path)
			continue
		}
		location := filepath.Join(request.RunDir, filepath.FromSlash(path))
		info, err := filesystem.Stat(ctx, location)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return step.Result{}, fmt.Errorf("inspecting %s: %w", path, err)
		}
		// Symbolic links, directories, and submodules have no content of their own to check.
		if !info.Mode.IsRegular() {
			continue
		}
		prefix, err := filesystem.ReadFile(ctx, location, textSniffBytes)
		if err != nil {
			return step.Result{}, fmt.Errorf("reading %s: %w", path, err)
		}
		files = append(files, path)
		if isText(prefix) {
			text = append(text, path)
		} else {
			binary = append(binary, path)
		}
		if change.added {
			added = append(added, path)
		}
	}
	return step.Result{Outputs: map[string]any{
		"source": runner.config.Source, "count": len(files),
		"files": files, "text": text, "binary": binary, "added": added, "deleted": deleted,
	}}, nil
}

func (runner *filesRunner) excluded(path string) bool {
	for _, pattern := range runner.exclude {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// changes lists every path the source touches, relative to the run directory.
func (runner *filesRunner) changes(ctx context.Context, request step.Request) (map[string]fileChange, error) {
	pathspecs := func(args ...string) []string {
		if len(runner.config.Paths) > 0 {
			args = append(append(args, "--"), runner.config.Paths...)
		}
		return args
	}
	switch runner.config.Source {
	case filesSourceAll:
		result, err := runGitCapture(ctx, request, pathspecs("ls-files", "-z", "--cached")...)
		if err != nil {
			return nil, gitCommandError("listing tracked Git files", result, err)
		}
		changes := make(map[string]fileChange)
		for _, path := range strings.Split(result.Stdout, "\x00") {
			if path != "" {
				changes[path] = fileChange{}
			}
		}
		return changes, nil
	case diffSourcePushed:
		updates, err := resolvePushUpdates(ctx, runner.updatesProgram, request)
		if err != nil {
			return nil, err
		}
		changes := make(map[string]fileChange)
		for _, update := range updates {
			if zeroOID(update.localOID) {
				continue
			}
			// Name the same commits git_diff_check inspects: those the push sends, without
			// merges, newest first so each path's first status is its latest.
			args := []string{"log", "--no-merges", "--name-status", "-z", "--no-renames", "--relative", "--format=%x01", update.localOID}
			if zeroOID(update.remoteOID) {
				args = append(args, "--not", "--remotes")
			} else {
				args = append(args, "^"+update.remoteOID)
			}
			result, err := runGitCapture(ctx, request, pathspecs(args...)...)
			if err != nil {
				return nil, gitCommandError(fmt.Sprintf("listing files for pushed ref %q", update.localRef), result, err)
			}
			if err := mergeNameStatus(changes, result.Stdout); err != nil {
				return nil, err
			}
		}
		return changes, nil
	default:
		args := []string{"diff", "--name-status", "-z", "--no-renames", "--relative"}
		if runner.config.Source == diffSourceStaged {
			args = append(args, "--cached")
		}
		result, err := runGitCapture(ctx, request, pathspecs(args...)...)
		if err != nil {
			return nil, gitCommandError("listing changed Git files", result, err)
		}
		changes := make(map[string]fileChange)
		return changes, mergeNameStatus(changes, result.Stdout)
	}
}

// mergeNameStatus folds one `--name-status -z` listing, newest change first, into changes.
// Log output adds a \x01 marker record per commit and a newline before its first status.
// One listing can only add to what earlier listings recorded, so a path deleted by one
// pushed ref stays a candidate when another ref still has it.
func mergeNameStatus(changes map[string]fileChange, output string) error {
	seen := make(map[string]struct{})
	records := strings.Split(output, "\x00")
	for index := 0; index < len(records); index++ {
		status := strings.TrimPrefix(records[index], "\n")
		if status == "" || strings.HasPrefix(status, "\x01") {
			continue
		}
		if len(status) != 1 || index+1 >= len(records) || records[index+1] == "" {
			return fmt.Errorf("decoding Git file list: malformed record %q", status)
		}
		index++
		path := records[index]
		change, known := changes[path]
		if status == "A" {
			change.added = true
		}
		if _, latest := seen[path]; !latest {
			seen[path] = struct{}{}
			deleted := status == "D"
			change.deleted = deleted && (!known || change.deleted)
		}
		changes[path] = change
	}
	return nil
}

// isText applies Git's heuristic to a file prefix of up to textSniffBytes+1 bytes: text has
// no NUL byte and, unlike Git, must also be valid UTF-8. A rune cut by the limit is ignored.
func isText(prefix []byte) bool {
	if len(prefix) > textSniffBytes {
		prefix = prefix[:textSniffBytes]
		for cut := 1; cut <= utf8.UTFMax-1 && cut <= len(prefix); cut++ {
			if utf8.RuneStart(prefix[len(prefix)-cut]) {
				if !utf8.FullRune(prefix[len(prefix)-cut:]) {
					prefix = prefix[:len(prefix)-cut]
				}
				break
			}
		}
	}
	return bytes.IndexByte(prefix, 0) < 0 && utf8.Valid(prefix)
}
