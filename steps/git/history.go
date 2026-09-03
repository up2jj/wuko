package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const (
	defaultLogLimit     = 100
	maximumLogLimit     = 1000
	historyCaptureLimit = 16 << 20

	ancestryAll         = "all"
	ancestryFirstParent = "first_parent"

	mergesInclude = "include"
	mergesExclude = "exclude"
	mergesOnly    = "only"
)

const commitFormat = "%H%x00%h%x00%P%x00%aN%x00%aE%x00%aI%x00%cN%x00%cE%x00%cI%x00%s%x00%b%x00%B%x00%x00%x00"

type revisionConfig struct {
	Revision string `yaml:"revision,omitempty"`
}

type logConfig struct {
	After    string   `yaml:"after,omitempty"`
	Through  string   `yaml:"through,omitempty"`
	Paths    []string `yaml:"paths,omitempty"`
	Ancestry string   `yaml:"ancestry,omitempty"`
	Merges   string   `yaml:"merges,omitempty"`
	Limit    int      `yaml:"limit,omitempty"`
}

type revisionRunner struct {
	config      revisionConfig
	hasRevision bool
}

type logRunner struct {
	config     logConfig
	hasAfter   bool
	hasThrough bool
	hasPaths   bool
}

// NewRevision builds a git_revision step.
func NewRevision(raw map[string]any) (step.Runner, error) {
	var config revisionConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, configured := raw["revision"]
	if configured && strings.TrimSpace(config.Revision) == "" {
		return nil, fmt.Errorf("revision must not be blank")
	}
	if strings.ContainsRune(config.Revision, '\x00') {
		return nil, fmt.Errorf("revision must not contain NUL")
	}
	if !configured {
		config.Revision = "HEAD"
	}
	return &revisionRunner{config: config, hasRevision: configured}, nil
}

// NewLog builds a git_log step.
func NewLog(raw map[string]any) (step.Runner, error) {
	var config logConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasAfter := raw["after"]
	_, hasThrough := raw["through"]
	_, hasPaths := raw["paths"]
	_, hasAncestry := raw["ancestry"]
	_, hasMerges := raw["merges"]
	_, hasLimit := raw["limit"]
	if hasAfter && strings.TrimSpace(config.After) == "" {
		return nil, fmt.Errorf("after must not be blank")
	}
	if hasThrough && strings.TrimSpace(config.Through) == "" {
		return nil, fmt.Errorf("through must not be blank")
	}
	if !hasThrough {
		config.Through = "HEAD"
	}
	if !hasPaths && config.Paths == nil {
		config.Paths = []string{}
	}
	if hasPaths && len(config.Paths) == 0 {
		return nil, fmt.Errorf("paths must contain at least one pathspec")
	}
	if !hasAncestry {
		config.Ancestry = ancestryAll
	}
	if hasAncestry && strings.TrimSpace(config.Ancestry) == "" {
		return nil, fmt.Errorf("ancestry must not be blank")
	}
	if strings.Contains(config.Ancestry, "{{") {
		return nil, fmt.Errorf("ancestry must not be templated")
	}
	if config.Ancestry != ancestryAll && config.Ancestry != ancestryFirstParent {
		return nil, fmt.Errorf("ancestry must be all or first_parent")
	}
	if !hasMerges {
		config.Merges = mergesInclude
	}
	if hasMerges && strings.TrimSpace(config.Merges) == "" {
		return nil, fmt.Errorf("merges must not be blank")
	}
	if strings.Contains(config.Merges, "{{") {
		return nil, fmt.Errorf("merges must not be templated")
	}
	if config.Merges != mergesInclude && config.Merges != mergesExclude && config.Merges != mergesOnly {
		return nil, fmt.Errorf("merges must be include, exclude, or only")
	}
	if !hasLimit {
		config.Limit = defaultLogLimit
	}
	if config.Limit < 1 || config.Limit > maximumLogLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maximumLogLimit)
	}
	runner := &logRunner{config: config, hasAfter: hasAfter, hasThrough: hasThrough, hasPaths: hasPaths}
	if err := runner.validateValues(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func (*revisionRunner) ExecutorAware() {}
func (*logRunner) ExecutorAware()      {}

func (r *revisionRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if unresolvedHistoryValue(r.config.Revision) {
		return step.Result{}, fmt.Errorf("git_revision configuration contains an unresolved template")
	}
	if strings.TrimSpace(r.config.Revision) == "" || strings.ContainsRune(r.config.Revision, '\x00') {
		return step.Result{}, fmt.Errorf("revision must not be blank or contain NUL")
	}
	commitID, found, err := resolveCommit(ctx, request, r.config.Revision, !r.hasRevision)
	if err != nil {
		return step.Result{}, err
	}
	if !found {
		return step.Result{Outputs: emptyRevisionOutputs()}, nil
	}
	records, err := queryCommits(ctx, request, []string{"show", "-s", "--no-show-signature", "--format=" + commitFormat, commitID})
	if err != nil {
		return step.Result{}, fmt.Errorf("reading Git revision %q: %w", r.config.Revision, err)
	}
	if len(records) != 1 {
		return step.Result{}, fmt.Errorf("reading Git revision %q: expected one commit, got %d", r.config.Revision, len(records))
	}
	outputs := maps.Clone(records[0])
	outputs["found"] = true
	return step.Result{Outputs: outputs}, nil
}

func (r *logRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := r.validateValues(true); err != nil {
		return step.Result{}, err
	}
	throughID, found, err := resolveCommit(ctx, request, r.config.Through, !r.hasThrough)
	if err != nil {
		return step.Result{}, err
	}
	if !found {
		return step.Result{Outputs: logOutputs("", "", nil, false)}, nil
	}
	afterID := ""
	if r.hasAfter {
		afterID, _, err = resolveCommit(ctx, request, r.config.After, false)
		if err != nil {
			return step.Result{}, err
		}
		if err := requireAncestor(ctx, request, afterID, throughID, r.config.After, r.config.Through); err != nil {
			return step.Result{}, err
		}
	}

	args := []string{"log", "--no-show-signature", "--format=" + commitFormat, fmt.Sprintf("--max-count=%d", r.config.Limit+1)}
	if r.config.Ancestry == ancestryFirstParent {
		args = append(args, "--first-parent")
	}
	switch r.config.Merges {
	case mergesExclude:
		args = append(args, "--no-merges")
	case mergesOnly:
		args = append(args, "--merges")
	}
	revision := throughID
	if afterID != "" {
		revision = afterID + ".." + throughID
	}
	args = append(args, revision)
	if r.hasPaths {
		args = append(args, "--")
		args = append(args, r.config.Paths...)
	}
	records, err := queryCommits(ctx, request, args)
	if err != nil {
		return step.Result{}, fmt.Errorf("reading Git history: %w", err)
	}
	hasMore := len(records) > r.config.Limit
	if hasMore {
		records = records[:r.config.Limit]
	}
	return step.Result{Outputs: logOutputs(afterID, throughID, records, hasMore)}, nil
}

func (r *logRunner) validateValues(resolved bool) error {
	values := []struct {
		name  string
		value string
		set   bool
	}{
		{name: "after", value: r.config.After, set: r.hasAfter},
		{name: "through", value: r.config.Through, set: true},
	}
	for _, value := range values {
		if resolved && unresolvedHistoryValue(value.value) {
			return fmt.Errorf("git_log configuration contains an unresolved template")
		}
		if value.set && strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s must not be blank", value.name)
		}
		if strings.ContainsRune(value.value, '\x00') {
			return fmt.Errorf("%s must not contain NUL", value.name)
		}
	}
	for index, path := range r.config.Paths {
		if resolved && unresolvedHistoryValue(path) {
			return fmt.Errorf("git_log configuration contains an unresolved template")
		}
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("paths[%d] must not be blank", index)
		}
		if strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("paths[%d] must not contain NUL", index)
		}
	}
	return nil
}

func unresolvedHistoryValue(value string) bool { return strings.Contains(value, "{{") }

func resolveCommit(ctx context.Context, request step.Request, revision string, allowMissing bool) (string, bool, error) {
	result, err := runGitCapture(ctx, request, "rev-parse", "--verify", "--quiet", "--end-of-options", revision+"^{commit}")
	if err != nil {
		var exitErr *process.ExitError
		if allowMissing && errors.As(err, &exitErr) && exitErr.Code == 1 {
			return "", false, nil
		}
		return "", false, gitCommandError(fmt.Sprintf("resolving Git revision %q", revision), result, err)
	}
	commitID := strings.TrimSpace(result.Stdout)
	if commitID == "" || strings.ContainsAny(commitID, "\r\n\x00") {
		return "", false, fmt.Errorf("resolving Git revision %q: git returned an invalid object ID", revision)
	}
	return commitID, true, nil
}

func requireAncestor(ctx context.Context, request step.Request, afterID, throughID, after, through string) error {
	result, err := runGitCapture(ctx, request, "merge-base", "--is-ancestor", afterID, throughID)
	if err == nil {
		return nil
	}
	var exitErr *process.ExitError
	if errors.As(err, &exitErr) && exitErr.Code == 1 {
		return fmt.Errorf("Git revision %q is not an ancestor of %q", after, through)
	}
	return gitCommandError("checking Git history boundary", result, err)
}

func queryCommits(ctx context.Context, request step.Request, args []string) ([]map[string]any, error) {
	result, err := runGitCapture(ctx, request, args...)
	if err != nil {
		return nil, gitCommandError("running Git history query", result, err)
	}
	return parseCommitRecords(result.Stdout)
}

func runGitCapture(ctx context.Context, request step.Request, args ...string) (process.Result, error) {
	executor := request.Executor
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	result, err := executor.Run(ctx, process.Options{
		Command: "git", Args: args, Dir: request.RunDir,
		Env:          step.ApplyAttemptEnvironment(maps.Clone(request.Env), request),
		CaptureLimit: historyCaptureLimit, StdoutPolicy: process.OutputCapture, StderrPolicy: process.OutputCapture,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		result.Stdout = ""
		result.Stderr = ""
		return result, fmt.Errorf("git %s: output exceeded %d MiB", strings.Join(args, " "), historyCaptureLimit>>20)
	}
	return result, nil
}

func parseCommitRecords(output string) ([]map[string]any, error) {
	if output == "" {
		return []map[string]any{}, nil
	}
	rawRecords := bytes.Split([]byte(output), []byte{0, 0, 0})
	records := make([]map[string]any, 0, len(rawRecords))
	for index, raw := range rawRecords {
		raw = bytes.TrimPrefix(raw, []byte("\r\n"))
		raw = bytes.TrimPrefix(raw, []byte("\n"))
		if len(raw) == 0 {
			continue
		}
		fields := bytes.Split(raw, []byte{0})
		if len(fields) != 12 {
			return nil, fmt.Errorf("decoding Git commit %d: got %d fields, want 12", index+1, len(fields))
		}
		record, err := commitRecord(fields)
		if err != nil {
			return nil, fmt.Errorf("decoding Git commit %d: %w", index+1, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func commitRecord(fields [][]byte) (map[string]any, error) {
	values := make([]string, len(fields))
	for index, field := range fields {
		values[index] = string(field)
	}
	for _, date := range []struct {
		name  string
		value string
	}{{"author date", values[5]}, {"committer date", values[8]}} {
		if _, err := time.Parse(time.RFC3339, date.value); err != nil {
			return nil, fmt.Errorf("invalid %s %q", date.name, date.value)
		}
	}
	if values[0] == "" || values[1] == "" {
		return nil, fmt.Errorf("commit object ID is missing")
	}
	parents := []any{}
	if values[2] != "" {
		for _, parent := range strings.Fields(values[2]) {
			parents = append(parents, parent)
		}
	}
	body := strings.TrimRight(values[10], "\r\n")
	message := strings.TrimRight(values[11], "\r\n")
	return map[string]any{
		"sha": values[0], "short_sha": values[1], "subject": values[9], "body": body, "message": message,
		"parents": parents, "is_merge": len(parents) > 1,
		"author":       map[string]any{"name": values[3], "email": values[4], "date": values[5]},
		"committer":    map[string]any{"name": values[6], "email": values[7], "date": values[8]},
		"conventional": conventionalHistoryOutputs(message),
	}, nil
}

func conventionalHistoryOutputs(message string) map[string]any {
	result, err := expression.InspectConventionalCommit(message, nil)
	classification := result.Classification
	if err != nil || classification == "" {
		classification = "other"
	}
	return map[string]any{
		"valid":          err == nil && result.Classification == "conventional",
		"classification": classification,
		"type":           result.Type, "scope": result.Scope, "subject": result.Subject,
		"breaking": result.Breaking, "body": result.Body,
	}
}

func emptyRevisionOutputs() map[string]any {
	return map[string]any{
		"found": false, "sha": "", "short_sha": "", "subject": "", "body": "", "message": "",
		"parents": []any{}, "is_merge": false,
		"author":    map[string]any{"name": "", "email": "", "date": ""},
		"committer": map[string]any{"name": "", "email": "", "date": ""},
		"conventional": map[string]any{
			"valid": false, "classification": "other", "type": "", "scope": "", "subject": "", "breaking": false, "body": "",
		},
	}
}

func logOutputs(after, through string, records []map[string]any, hasMore bool) map[string]any {
	commits := make([]any, len(records))
	for index, record := range records {
		commits[index] = record
	}
	return map[string]any{
		"after": after, "through": through, "count": len(commits), "has_more": hasMore, "commits": commits,
	}
}
