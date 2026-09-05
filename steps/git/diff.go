package git

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"gopkg.in/yaml.v3"
)

const (
	diffSourceCommit   = "commit"
	diffSourceStaged   = "staged"
	diffSourceUnstaged = "unstaged"
	diffSourcePushed   = "pushed"
)

type updatesExpression struct {
	Expr string `yaml:"expr"`
}

func (expression *updatesExpression) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 || node.Content[0].Value != "expr" {
		return fmt.Errorf("updates must be an object containing exactly the expr field")
	}
	if node.Content[1].Kind != yaml.ScalarNode || node.Content[1].Tag != "!!str" {
		return fmt.Errorf("updates expr must be a string")
	}
	expression.Expr = node.Content[1].Value
	return nil
}

type diffConfig struct {
	Source  string   `yaml:"source,omitempty"`
	From    string   `yaml:"from,omitempty"`
	Through string   `yaml:"through,omitempty"`
	Paths   []string `yaml:"paths,omitempty"`
	Renames *bool    `yaml:"renames,omitempty"`
	Copies  bool     `yaml:"copies,omitempty"`
}

type diffCheckConfig struct {
	Source  string             `yaml:"source"`
	From    string             `yaml:"from,omitempty"`
	Through string             `yaml:"through,omitempty"`
	Paths   []string           `yaml:"paths,omitempty"`
	Updates *updatesExpression `yaml:"updates,omitempty"`
}

type diffRunner struct {
	config  diffConfig
	renames bool
	hasFrom bool
}

type diffCheckRunner struct {
	config         diffCheckConfig
	hasFrom        bool
	updatesProgram *vm.Program
}

type diffComparison struct {
	source  string
	from    string
	through string
	root    bool
	paths   []string
}

type diffFile struct {
	status       string
	path         string
	previousPath string
	oldMode      string
	newMode      string
	oldOID       string
	newOID       string
	additions    int
	deletions    int
	binary       bool
	score        int
}

type pushUpdate struct {
	localRef  string
	localOID  string
	remoteOID string
}

// NewDiff builds a structured Git diff step.
func NewDiff(raw map[string]any) (step.Runner, error) {
	var config diffConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if _, configured := raw["source"]; !configured {
		config.Source = diffSourceCommit
	}
	if _, configured := raw["through"]; !configured && config.Source == diffSourceCommit {
		config.Through = "HEAD"
	}
	renames := true
	if config.Renames != nil {
		renames = *config.Renames
	}
	_, hasFrom := raw["from"]
	if err := validateDiffConfig(config.Source, config.From, config.Through, config.Paths, hasFrom, true); err != nil {
		return nil, err
	}
	if config.Copies && !renames {
		return nil, fmt.Errorf("copies requires renames")
	}
	return &diffRunner{config: config, renames: renames, hasFrom: hasFrom}, nil
}

// NewDiffCheck builds a Git diff policy check.
func NewDiffCheck(raw map[string]any) (step.Runner, error) {
	var config diffCheckConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Source) == "" {
		return nil, fmt.Errorf("source is required")
	}
	if _, configured := raw["through"]; !configured && config.Source == diffSourceCommit {
		config.Through = "HEAD"
	}
	_, hasFrom := raw["from"]
	if err := validateDiffConfig(config.Source, config.From, config.Through, config.Paths, hasFrom, false); err != nil {
		return nil, err
	}
	if config.Source != diffSourcePushed && config.Updates != nil {
		return nil, fmt.Errorf("updates is only supported with source pushed")
	}
	var program *vm.Program
	if config.Updates != nil {
		if strings.TrimSpace(config.Updates.Expr) == "" {
			return nil, fmt.Errorf("updates expr must be a non-empty string")
		}
		compiled, err := wukoexpr.Compile(config.Updates.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("compiling updates expr: %w", err)
		}
		program = compiled
	}
	return &diffCheckRunner{config: config, hasFrom: hasFrom, updatesProgram: program}, nil
}

func validateDiffConfig(source, from, through string, paths []string, hasFrom, inspection bool) error {
	allowed := source == diffSourceCommit || source == diffSourceStaged || source == diffSourceUnstaged
	if !inspection {
		allowed = allowed || source == diffSourcePushed
	}
	if strings.Contains(source, "{{") {
		return fmt.Errorf("source must not be templated")
	}
	if !allowed {
		if inspection {
			return fmt.Errorf("source must be commit, staged, or unstaged")
		}
		return fmt.Errorf("source must be commit, staged, unstaged, or pushed")
	}
	if source != diffSourceCommit && (hasFrom || through != "") {
		return fmt.Errorf("from and through are only supported with source commit")
	}
	for _, value := range []struct {
		name  string
		value string
		set   bool
	}{{"from", from, hasFrom}, {"through", through, source == diffSourceCommit}} {
		if value.set && strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s must not be blank", value.name)
		}
		if strings.ContainsRune(value.value, '\x00') {
			return fmt.Errorf("%s must not contain NUL", value.name)
		}
	}
	if paths == nil {
		return nil
	}
	if len(paths) == 0 {
		return fmt.Errorf("paths must contain at least one pathspec")
	}
	for index, path := range paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("paths[%d] must not be blank", index)
		}
		if strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("paths[%d] must not contain NUL", index)
		}
	}
	return nil
}

func (*diffRunner) ExecutorAware()      {}
func (*diffCheckRunner) ExecutorAware() {}

func (runner *diffRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := validateResolvedDiffValues(runner.config.From, runner.config.Through, runner.config.Paths); err != nil {
		return step.Result{}, err
	}
	comparison, err := resolveDiffComparison(ctx, request, runner.config.Source, runner.config.From, runner.config.Through, runner.hasFrom, runner.config.Paths)
	if err != nil {
		return step.Result{}, err
	}
	files, err := readDiff(ctx, request, comparison, runner.renames, runner.config.Copies)
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: diffOutputs(comparison, files)}, nil
}

func (runner *diffCheckRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := validateResolvedDiffValues(runner.config.From, runner.config.Through, runner.config.Paths); err != nil {
		return step.Result{}, err
	}
	if runner.config.Source == diffSourcePushed {
		return step.Result{}, runner.checkPushed(ctx, request)
	}
	comparison, err := resolveDiffComparison(ctx, request, runner.config.Source, runner.config.From, runner.config.Through, runner.hasFrom, runner.config.Paths)
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{}, checkDiff(ctx, request, comparison)
}

func validateResolvedDiffValues(from, through string, paths []string) error {
	for _, value := range append([]string{from, through}, paths...) {
		if strings.Contains(value, "{{") {
			return fmt.Errorf("Git diff configuration contains an unresolved template")
		}
	}
	return nil
}

func resolveDiffComparison(ctx context.Context, request step.Request, source, from, through string, hasFrom bool, paths []string) (diffComparison, error) {
	comparison := diffComparison{source: source, paths: append([]string(nil), paths...)}
	if source != diffSourceCommit {
		return comparison, nil
	}
	throughID, _, err := resolveCommit(ctx, request, through, false)
	if err != nil {
		return diffComparison{}, err
	}
	comparison.through = throughID
	if hasFrom {
		fromID, _, err := resolveCommit(ctx, request, from, false)
		if err != nil {
			return diffComparison{}, err
		}
		comparison.from = fromID
		return comparison, nil
	}
	parent, root, err := firstParent(ctx, request, throughID)
	if err != nil {
		return diffComparison{}, err
	}
	comparison.from, comparison.root = parent, root
	return comparison, nil
}

func firstParent(ctx context.Context, request step.Request, commit string) (string, bool, error) {
	result, err := runGitCapture(ctx, request, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return "", false, gitCommandError(fmt.Sprintf("reading parents of Git commit %q", commit), result, err)
	}
	fields := strings.Fields(result.Stdout)
	if len(fields) == 0 || fields[0] != commit {
		return "", false, fmt.Errorf("reading parents of Git commit %q: Git returned malformed output", commit)
	}
	if len(fields) == 1 {
		return "", true, nil
	}
	return fields[1], false, nil
}

func diffCommandArgs(comparison diffComparison, format string, renames, copies bool) []string {
	args := []string{"diff"}
	if comparison.source == diffSourceCommit {
		args = []string{"diff-tree", "--no-commit-id", "-r"}
		if comparison.root {
			args = append(args, "--root")
		}
	} else if comparison.source == diffSourceStaged {
		args = append(args, "--cached")
	}
	switch format {
	case "raw":
		args = append(args, "--no-color", "--raw", "--no-abbrev", "--full-index", "-z")
	case "numstat":
		args = append(args, "--no-color", "--numstat", "-z")
	case "check":
		args = append(args, "--no-color", "--check")
	}
	if renames {
		args = append(args, "-M")
	} else {
		// Porcelain `git diff` honours diff.renames, which defaults to true, so renames must be
		// disabled explicitly rather than by omitting -M.
		args = append(args, "--no-renames")
	}
	if copies {
		args = append(args, "-C", "--find-copies-harder")
	}
	if comparison.source == diffSourceCommit {
		if comparison.from != "" {
			args = append(args, comparison.from)
		}
		args = append(args, comparison.through)
	}
	if len(comparison.paths) > 0 {
		args = append(args, "--")
		args = append(args, comparison.paths...)
	}
	return args
}

func readDiff(ctx context.Context, request step.Request, comparison diffComparison, renames, copies bool) ([]diffFile, error) {
	rawResult, err := runGitCapture(ctx, request, diffCommandArgs(comparison, "raw", renames, copies)...)
	if err != nil {
		return nil, gitCommandError("reading Git diff metadata", rawResult, err)
	}
	files, err := parseRawDiff(rawResult.Stdout)
	if err != nil {
		return nil, err
	}
	statResult, err := runGitCapture(ctx, request, diffCommandArgs(comparison, "numstat", renames, copies)...)
	if err != nil {
		return nil, gitCommandError("reading Git diff line counts", statResult, err)
	}
	stats, err := parseNumstat(statResult.Stdout)
	if err != nil {
		return nil, err
	}
	if len(files) != len(stats) {
		return nil, fmt.Errorf("decoding Git diff: metadata contains %d files but line counts contain %d", len(files), len(stats))
	}
	for index := range files {
		if files[index].path != stats[index].path || files[index].previousPath != stats[index].previousPath {
			return nil, fmt.Errorf("decoding Git diff file %d: metadata and line-count paths differ", index+1)
		}
		files[index].additions = stats[index].additions
		files[index].deletions = stats[index].deletions
		files[index].binary = stats[index].binary
	}
	return coalesceUnmergedFiles(files)
}

func coalesceUnmergedFiles(files []diffFile) ([]diffFile, error) {
	result := make([]diffFile, 0, len(files))
	indexes := make(map[string]int, len(files))
	for _, file := range files {
		key := file.previousPath + "\x00" + file.path
		index, exists := indexes[key]
		if !exists {
			indexes[key] = len(result)
			result = append(result, file)
			continue
		}
		previous := result[index]
		if previous.status != "unmerged" && file.status != "unmerged" {
			return nil, fmt.Errorf("decoding Git diff: duplicate path %q", file.path)
		}
		if file.status == "unmerged" {
			file.additions += previous.additions
			file.deletions += previous.deletions
			file.binary = file.binary || previous.binary
			result[index] = file
			continue
		}
		previous.additions += file.additions
		previous.deletions += file.deletions
		previous.binary = previous.binary || file.binary
		result[index] = previous
	}
	return result, nil
}

func parseRawDiff(output string) ([]diffFile, error) {
	if output == "" {
		return []diffFile{}, nil
	}
	parts := strings.Split(output, "\x00")
	files := make([]diffFile, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		if parts[index] == "" {
			if index == len(parts)-1 {
				break
			}
			return nil, fmt.Errorf("decoding Git diff metadata: unexpected empty record")
		}
		header := strings.Fields(strings.TrimPrefix(parts[index], ":"))
		if len(header) != 5 || !strings.HasPrefix(parts[index], ":") {
			return nil, fmt.Errorf("decoding Git diff metadata record %d: malformed header", len(files)+1)
		}
		if !validGitMode(header[0]) || !validGitMode(header[1]) || !validObjectID(header[2]) || !validObjectID(header[3]) || len(header[2]) != len(header[3]) {
			return nil, fmt.Errorf("decoding Git diff metadata record %d: invalid mode or object ID", len(files)+1)
		}
		index++
		status, score, paths, err := parseRawStatus(header[4])
		if err != nil {
			return nil, fmt.Errorf("decoding Git diff metadata record %d: %w", len(files)+1, err)
		}
		if index+paths > len(parts) {
			return nil, fmt.Errorf("decoding Git diff metadata record %d: missing path", len(files)+1)
		}
		previousPath := ""
		path := parts[index]
		index++
		if paths == 2 {
			previousPath, path = path, parts[index]
			index++
		}
		if path == "" || paths == 2 && previousPath == "" {
			return nil, fmt.Errorf("decoding Git diff metadata record %d: path is empty", len(files)+1)
		}
		files = append(files, diffFile{
			status: status, path: path, previousPath: previousPath,
			oldMode: header[0], newMode: header[1], oldOID: header[2], newOID: header[3], score: score,
		})
	}
	return files, nil
}

func parseRawStatus(value string) (string, int, int, error) {
	if value == "" {
		return "", 0, 0, fmt.Errorf("status is missing")
	}
	status := map[byte]string{
		'A': "added", 'C': "copied", 'D': "deleted", 'M': "modified", 'R': "renamed",
		'T': "type_changed", 'U': "unmerged", 'X': "unknown", 'B': "unknown",
	}[value[0]]
	if status == "" {
		status = "unknown"
	}
	paths := 1
	score := 0
	if value[0] == 'R' || value[0] == 'C' {
		paths = 2
		if len(value) == 1 {
			return "", 0, 0, fmt.Errorf("rename or copy score is missing")
		}
		parsed, err := strconv.Atoi(value[1:])
		if err != nil || parsed < 0 || parsed > 100 {
			return "", 0, 0, fmt.Errorf("invalid rename or copy score %q", value[1:])
		}
		score = parsed
	} else if len(value) != 1 {
		return "", 0, 0, fmt.Errorf("invalid status %q", value)
	}
	return status, score, paths, nil
}

func validGitMode(value string) bool {
	if len(value) != 6 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '7' {
			return false
		}
	}
	return true
}

func validObjectID(value string) bool {
	if len(value) < 4 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func parseNumstat(output string) ([]diffFile, error) {
	if output == "" {
		return []diffFile{}, nil
	}
	parts := strings.Split(output, "\x00")
	files := make([]diffFile, 0, len(parts))
	for index := 0; index < len(parts); {
		if parts[index] == "" {
			if index == len(parts)-1 {
				break
			}
			return nil, fmt.Errorf("decoding Git diff line counts: unexpected empty record")
		}
		fields := strings.SplitN(parts[index], "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("decoding Git diff line-count record %d: malformed record", len(files)+1)
		}
		index++
		file := diffFile{path: fields[2]}
		if fields[2] == "" {
			if index+2 > len(parts) {
				return nil, fmt.Errorf("decoding Git diff line-count record %d: missing rename paths", len(files)+1)
			}
			file.previousPath, file.path = parts[index], parts[index+1]
			index += 2
		}
		if file.path == "" || fields[2] == "" && file.previousPath == "" {
			return nil, fmt.Errorf("decoding Git diff line-count record %d: path is empty", len(files)+1)
		}
		if fields[0] == "-" && fields[1] == "-" {
			file.binary = true
		} else {
			additions, addErr := strconv.Atoi(fields[0])
			deletions, deleteErr := strconv.Atoi(fields[1])
			if addErr != nil || deleteErr != nil || additions < 0 || deletions < 0 {
				return nil, fmt.Errorf("decoding Git diff line-count record %d: invalid counts", len(files)+1)
			}
			file.additions, file.deletions = additions, deletions
		}
		files = append(files, file)
	}
	return files, nil
}

func diffOutputs(comparison diffComparison, files []diffFile) map[string]any {
	items := make([]any, len(files))
	additions, deletions, binaries := 0, 0, 0
	for index, file := range files {
		additions += file.additions
		deletions += file.deletions
		if file.binary {
			binaries++
		}
		items[index] = map[string]any{
			"status": file.status, "path": file.path, "previous_path": file.previousPath,
			"old_mode": file.oldMode, "new_mode": file.newMode, "old_oid": file.oldOID, "new_oid": file.newOID,
			"additions": file.additions, "deletions": file.deletions, "binary": file.binary, "score": file.score,
		}
	}
	return map[string]any{
		"source": comparison.source, "from": comparison.from, "through": comparison.through,
		"changed": len(files) > 0, "count": len(files), "additions": additions, "deletions": deletions,
		"binary_files": binaries, "files": items,
	}
}

func checkDiff(ctx context.Context, request step.Request, comparison diffComparison) error {
	// Rename detection matters for --check: without it a pure move is reported as a whole-file
	// addition, so every pre-existing whitespace error in the moved file is blamed on the change.
	result, err := runGitCapture(ctx, request, diffCommandArgs(comparison, "check", true, false)...)
	if err != nil {
		return gitCommandError("checking Git diff", result, err)
	}
	return nil
}

func (runner *diffCheckRunner) checkPushed(ctx context.Context, request step.Request) error {
	updates, err := runner.resolveUpdates(request)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{})
	for _, update := range updates {
		if zeroOID(update.localOID) {
			continue
		}
		// Merge commits introduce no changes of their own: comparing one with its first parent
		// would report every problem already present on the merged branch, including commits the
		// remote already has. Git's own `diff-tree --check` reports nothing for a merge too.
		args := []string{"rev-list", "--no-merges", update.localOID}
		if zeroOID(update.remoteOID) {
			args = append(args, "--not", "--remotes")
		} else {
			args = append(args, "^"+update.remoteOID)
		}
		result, runErr := runGitCapture(ctx, request, args...)
		if runErr != nil {
			return gitCommandError(fmt.Sprintf("enumerating commits for pushed ref %q", update.localRef), result, runErr)
		}
		for _, commit := range strings.Fields(result.Stdout) {
			if _, exists := seen[commit]; exists {
				continue
			}
			seen[commit] = struct{}{}
			comparison, resolveErr := resolveDiffComparison(ctx, request, diffSourceCommit, "", commit, false, runner.config.Paths)
			if resolveErr != nil {
				return resolveErr
			}
			if checkErr := checkDiff(ctx, request, comparison); checkErr != nil {
				return fmt.Errorf("pushed commit %s: %w", commit, checkErr)
			}
		}
	}
	return nil
}

func (runner *diffCheckRunner) resolveUpdates(request step.Request) ([]pushUpdate, error) {
	var value any
	if runner.updatesProgram != nil {
		resolved, err := expr.Run(runner.updatesProgram, request.ExpressionEnvironment(nil))
		if err != nil {
			return nil, fmt.Errorf("evaluating updates expr: %w", err)
		}
		value = resolved
	} else {
		gitContext, ok := request.Providers.Values["git"]
		if !ok {
			return nil, fmt.Errorf("source pushed requires updates or an active Git hook context")
		}
		hook, ok := gitContext["hook"].(map[string]any)
		if !ok || hook["name"] != "pre-push" {
			return nil, fmt.Errorf("source pushed requires a pre-push Git hook context")
		}
		payload, ok := hook["payload"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pre-push Git hook payload is unavailable")
		}
		value = payload["updates"]
	}
	return decodePushUpdates(value)
}

func decodePushUpdates(value any) ([]pushUpdate, error) {
	list := reflect.ValueOf(value)
	if !list.IsValid() || (list.Kind() != reflect.Array && list.Kind() != reflect.Slice) {
		return nil, fmt.Errorf("updates expression must return a list")
	}
	updates := make([]pushUpdate, 0, list.Len())
	for index := range list.Len() {
		item := list.Index(index)
		for item.IsValid() && item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("updates item %d must be an object", index)
			}
			item = item.Elem()
		}
		if !item.IsValid() || item.Kind() != reflect.Map {
			return nil, fmt.Errorf("updates item %d must be an object", index)
		}
		fields := make(map[string]string, 4)
		iterator := item.MapRange()
		for iterator.Next() {
			key, keyOK := iterator.Key().Interface().(string)
			field, fieldOK := iterator.Value().Interface().(string)
			if keyOK && fieldOK {
				fields[key] = field
			}
		}
		for _, name := range []string{"local_ref", "local_oid", "remote_ref", "remote_oid"} {
			if strings.TrimSpace(fields[name]) == "" || strings.ContainsAny(fields[name], "\x00\r\n") {
				return nil, fmt.Errorf("updates item %d field %s must be a non-empty single-line string", index, name)
			}
		}
		updates = append(updates, pushUpdate{
			localRef: fields["local_ref"], localOID: fields["local_oid"],
			remoteOID: fields["remote_oid"],
		})
	}
	return updates, nil
}

func zeroOID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character != '0' {
			return false
		}
	}
	return true
}
