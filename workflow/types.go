package workflow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	workflowschedule "github.com/up2jj/wuko/schedule"
	"gopkg.in/yaml.v3"
)

var (
	identifierPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	httpMethodPattern  = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Z]+$")
)

// Definition is a fully loaded workflow document.
type Definition struct {
	Version     int                       `yaml:"version"`
	Name        string                    `yaml:"name"`
	Description string                    `yaml:"description,omitempty"`
	Invokable   *bool                     `yaml:"invokable,omitempty"`
	DependsOn   map[string]string         `yaml:"depends_on,omitempty"`
	Outputs     map[string]WorkflowOutput `yaml:"outputs,omitempty"`
	// Form is intentionally opaque to core workflow execution. The optional browser UI decodes it.
	Form        yaml.Node                     `yaml:"form,omitempty"`
	Cron        string                        `yaml:"cron,omitempty"`
	Timezone    string                        `yaml:"timezone,omitempty"`
	Templates   map[string]TemplateDefinition `yaml:"templates,omitempty"`
	Vars        map[string]any                `yaml:"vars,omitempty"`
	Env         Environment                   `yaml:"env,omitempty"`
	Steps       []Step                        `yaml:"steps"`
	Finally     []Step                        `yaml:"finally,omitempty"`
	Install     []Step                        `yaml:"install,omitempty"`
	Uninstall   []Step                        `yaml:"uninstall,omitempty"`
	Path        string                        `yaml:"-"`
	Dir         string                        `yaml:"-"`
	Location    diagnostic.Location           `yaml:"-"`
	sourceRoot  string
	sourceLabel string
}

// IsInvokable reports whether the workflow may be selected as a direct run or UI root.
// Workflows are invokable by default so existing definitions remain compatible.
func (definition *Definition) IsInvokable() bool {
	return definition != nil && (definition.Invokable == nil || *definition.Invokable)
}

// HasForm reports whether the workflow contains a non-null browser form declaration.
func (definition *Definition) HasForm() bool {
	return definition != nil && definition.Form.Kind != 0 && definition.Form.Tag != "!!null"
}

// Environment is a strictly string-valued environment overlay.
type Environment map[string]string

func (e *Environment) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("environment must be an object")
	}
	result := make(Environment, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		keyNode, valueNode := node.Content[i], node.Content[i+1]
		if keyNode.Tag != "!!str" || valueNode.Tag != "!!str" {
			return fmt.Errorf("environment names and values must be strings")
		}
		result[keyNode.Value] = valueNode.Value
	}
	*e = result
	return nil
}

// Condition is a boolean expression controlling whether a step runs.
type Condition string

func (c *Condition) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!bool") {
		return fmt.Errorf("if must be a boolean expression")
	}
	if strings.TrimSpace(node.Value) == "" {
		return fmt.Errorf("if must not be empty")
	}
	*c = Condition(node.Value)
	return nil
}

// Step declares a concrete step, a transparent conditional or working-directory block, a named
// worktree control, a return control, a composite action, or a local step-file requirement.
type Step struct {
	ID               string              `yaml:"id"`
	Type             string              `yaml:"type,omitempty"`
	Uses             ActionSource        `yaml:"uses,omitempty"`
	Require          *string             `yaml:"require,omitempty"`
	WorkingDirectory string              `yaml:"working_directory,omitempty"`
	Worktree         *WorktreeGroup      `yaml:"worktree,omitempty"`
	Executor         *ExecutorScope      `yaml:"executor,omitempty"`
	Steps            []Step              `yaml:"steps,omitempty"`
	Finally          []Step              `yaml:"finally,omitempty"`
	Defer            []Step              `yaml:"defer,omitempty"`
	Concurrent       *ConcurrentGroup    `yaml:"concurrent,omitempty"`
	Batch            *BatchGroup         `yaml:"batch,omitempty"`
	Foreach          *ForeachGroup       `yaml:"foreach,omitempty"`
	Matrix           *MatrixGroup        `yaml:"matrix,omitempty"`
	Loop             *LoopGroup          `yaml:"loop,omitempty"`
	Return           *ReturnControl      `yaml:"return,omitempty"`
	SHA256           string              `yaml:"sha256,omitempty"`
	If               Condition           `yaml:"if,omitempty"`
	Timeout          *Duration           `yaml:"timeout,omitempty"`
	Retry            *RetryPolicy        `yaml:"retry,omitempty"`
	With             map[string]any      `yaml:"with,omitempty"`
	Action           *Action             `yaml:"-"`
	Location         diagnostic.Location `yaml:"-"`
	hasWorkingDir    bool
	sourcePath       string
}

func (workflowStep *Step) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("step must be an object")
	}
	if err := rejectUnknownFields(node, "step", map[string]bool{
		"id": true, "type": true, "uses": true, "require": true, "working_directory": true, "worktree": true, "executor": true, "steps": true, "finally": true, "defer": true, "concurrent": true,
		"batch": true, "foreach": true, "matrix": true, "loop": true, "return": true, "sha256": true, "if": true, "timeout": true,
		"retry": true, "with": true,
	}); err != nil {
		return err
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "working_directory" && (node.Content[i+1].Kind != yaml.ScalarNode || node.Content[i+1].Tag != "!!str") {
			return fmt.Errorf("working_directory must be a string path")
		}
		if node.Content[i].Value == "steps" && node.Content[i+1].Kind != yaml.SequenceNode {
			return fmt.Errorf("steps must be a list")
		}
		if node.Content[i].Value == "finally" && node.Content[i+1].Kind != yaml.SequenceNode {
			return fmt.Errorf("finally must be a list")
		}
		if node.Content[i].Value == "defer" && node.Content[i+1].Kind != yaml.SequenceNode {
			return fmt.Errorf("defer must be a list")
		}
	}
	type plain Step
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*workflowStep = Step(decoded)
	workflowStep.hasWorkingDir = hasMappingField(node, "working_directory")
	return nil
}

// IsConditionalBlock reports whether the step is an anonymous multi-step conditional.
func (workflowStep Step) IsConditionalBlock() bool {
	return workflowStep.Steps != nil && workflowStep.Loop == nil && !workflowStep.IsWorkingDirectoryBlock() && !workflowStep.IsExecutorBlock() && workflowStep.Return == nil
}

// IsLoop reports whether the step repeats its child sequence until a condition matches.
func (workflowStep Step) IsLoop() bool { return workflowStep.Loop != nil }

// IsWorkingDirectoryBlock reports whether the step scopes its children to a run directory.
func (workflowStep Step) IsWorkingDirectoryBlock() bool {
	return workflowStep.hasWorkingDir || workflowStep.WorkingDirectory != ""
}

// IsWorktreeBlock reports whether the step creates and scopes execution to a Git worktree.
func (workflowStep Step) IsWorktreeBlock() bool { return workflowStep.Worktree != nil }

// IsExecutorBlock reports whether the step temporarily selects an execution provider.
func (workflowStep Step) IsExecutorBlock() bool { return workflowStep.Executor != nil }

// ValidateBlock validates the intrinsic shape of a conditional or working-directory block.
// Parent-scope restrictions and child declarations are validated by Definition.ValidateStructure.
func (workflowStep Step) ValidateBlock() error {
	return workflowStep.validateBlock()
}

// validateBlock validates the intrinsic shape of a conditional or working-directory block.
// Parent-scope restrictions and child declarations are validated by the workflow walker.
func (workflowStep Step) validateBlock() error {
	switch {
	case workflowStep.IsWorktreeBlock():
		if err := workflowStep.Worktree.Validate(); err != nil {
			return err
		}
		if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Steps != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
			return fmt.Errorf("worktree block cannot be combined with other step fields")
		}
		return nil
	case workflowStep.IsWorkingDirectoryBlock():
		if strings.TrimSpace(workflowStep.WorkingDirectory) == "" {
			return fmt.Errorf("working_directory must be a non-empty path")
		}
		if len(workflowStep.Steps) == 0 {
			return fmt.Errorf("working_directory block must contain at least one step")
		}
		if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
			return fmt.Errorf("working_directory block cannot be combined with other step fields")
		}
	case workflowStep.IsConditionalBlock():
		if workflowStep.If == "" {
			return fmt.Errorf("conditional block must set if")
		}
		if len(workflowStep.Steps) == 0 {
			return fmt.Errorf("conditional block must contain at least one step")
		}
		if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
			return fmt.Errorf("conditional block cannot be combined with other step fields")
		}
	}
	return nil
}

// WorktreeGroup creates a detached Git worktree and executes child steps inside it.
type WorktreeGroup struct {
	Revision string           `yaml:"revision"`
	Path     string           `yaml:"path,omitempty"`
	Publish  *WorktreePublish `yaml:"publish,omitempty"`
	Steps    []Step           `yaml:"steps"`
}

// WorktreePublish describes the branch to create from the final worktree commit.
type WorktreePublish struct {
	Branch string `yaml:"branch"`
}

func (group *WorktreeGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("worktree must be an object")
	}
	if err := rejectUnknownFields(node, "worktree", map[string]bool{
		"revision": true, "path": true, "publish": true, "steps": true,
	}); err != nil {
		return err
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "revision" || node.Content[i].Value == "path" {
			value := node.Content[i+1]
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("worktree %s must be a string", node.Content[i].Value)
			}
		}
		if node.Content[i].Value == "steps" && node.Content[i+1].Kind != yaml.SequenceNode {
			return fmt.Errorf("worktree steps must be a list")
		}
	}
	type plain WorktreeGroup
	decoded := plain{Path: "auto"}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Publish != nil {
		if err := validateWorktreePublishNode(mappingValue(node, "publish")); err != nil {
			return err
		}
	}
	*group = WorktreeGroup(decoded)
	return nil
}

func validateWorktreePublishNode(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("worktree publish must be an object")
	}
	if err := rejectUnknownFields(node, "worktree publish", map[string]bool{"branch": true}); err != nil {
		return err
	}
	branch := mappingValue(node, "branch")
	if branch != nil && (branch.Kind != yaml.ScalarNode || branch.Tag != "!!str") {
		return fmt.Errorf("worktree publish branch must be a string")
	}
	return nil
}

// Validate checks the worktree declaration independently of runtime Git state.
func (group WorktreeGroup) Validate() error {
	if strings.TrimSpace(group.Revision) == "" {
		return fmt.Errorf("worktree revision must be a non-empty expression string")
	}
	if strings.TrimSpace(group.Path) == "" {
		return fmt.Errorf("worktree path must be auto or a non-empty path")
	}
	if len(group.Steps) == 0 {
		return fmt.Errorf("worktree block must contain at least one step")
	}
	if group.Publish != nil && strings.TrimSpace(group.Publish.Branch) == "" {
		return fmt.Errorf("worktree publish branch must be a non-empty expression string")
	}
	return nil
}

// ExecutorScope selects one registered executor provider for its child steps.
type ExecutorScope struct {
	Type string         `yaml:"type"`
	With map[string]any `yaml:"with,omitempty"`
}

func (scope *ExecutorScope) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("executor must be an object")
	}
	if err := rejectUnknownFields(node, "executor", map[string]bool{"type": true, "with": true}); err != nil {
		return err
	}
	type plain ExecutorScope
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	if strings.TrimSpace(decoded.Type) == "" {
		return fmt.Errorf("executor type is required")
	}
	decoded.Type = strings.TrimSpace(decoded.Type)
	if decoded.With == nil {
		decoded.With = make(map[string]any)
	}
	*scope = ExecutorScope(decoded)
	return nil
}

// ConcurrentGroup runs independent child steps against one shared pre-group state snapshot.
type ConcurrentGroup struct {
	Steps          []Step    `yaml:"steps"`
	MaxConcurrency int       `yaml:"max_concurrency,omitempty"`
	Timeout        *Duration `yaml:"timeout,omitempty"`
	FailFast       bool      `yaml:"fail_fast"`
}

func (group *ConcurrentGroup) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("concurrent must be an object")
	}
	if err := rejectUnknownFields(node, "concurrent group", map[string]bool{
		"steps": true, "max_concurrency": true, "timeout": true, "fail_fast": true,
	}); err != nil {
		return err
	}
	type plainConcurrentGroup ConcurrentGroup
	decoded := plainConcurrentGroup{MaxConcurrency: 4, FailFast: true}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*group = ConcurrentGroup(decoded)
	return nil
}

// Duration is a YAML duration written using Go duration syntax, such as 500ms or 2m.
type Duration time.Duration

func (duration *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return fmt.Errorf("duration must be a string such as 500ms or 2m")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	*duration = Duration(parsed)
	return nil
}

// Value returns the duration as a standard-library time.Duration.
func (duration Duration) Value() time.Duration { return time.Duration(duration) }

// String returns the duration in Go duration syntax.
func (duration Duration) String() string { return duration.Value().String() }

// RetryPolicy controls repeated execution of one logical workflow step.
type RetryPolicy struct {
	MaxAttempts       int           `yaml:"max_attempts"`
	InitialDelay      Duration      `yaml:"initial_delay,omitempty"`
	BackoffMultiplier float64       `yaml:"backoff_multiplier,omitempty"`
	MaxDelay          Duration      `yaml:"max_delay,omitempty"`
	Jitter            float64       `yaml:"jitter,omitempty"`
	MaxElapsedTime    Duration      `yaml:"max_elapsed_time,omitempty"`
	OperationID       string        `yaml:"operation_id,omitempty"`
	Methods           []string      `yaml:"methods,omitempty"`
	Statuses          []StatusRange `yaml:"statuses,omitempty"`
	hasMethods        bool
	hasStatuses       bool
}

// StatusRange is one inclusive HTTP status-code range used by an HTTP retry policy.
type StatusRange struct {
	From int
	To   int
}

func (status *StatusRange) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("retry status must be an integer or range")
	}
	value := strings.TrimSpace(node.Value)
	parts := strings.Split(value, "-")
	if len(parts) > 2 || value == "" {
		return fmt.Errorf("retry status %q must be an integer or inclusive range", value)
	}
	from, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return fmt.Errorf("retry status %q must be an integer or inclusive range", value)
	}
	to := from
	if len(parts) == 2 {
		to, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return fmt.Errorf("retry status %q must be an integer or inclusive range", value)
		}
	}
	if from < 100 || from > 599 || to < 100 || to > 599 {
		return fmt.Errorf("retry status %q must be between 100 and 599", value)
	}
	if from > to {
		return fmt.Errorf("retry status range %q must be ascending", value)
	}
	*status = StatusRange{From: from, To: to}
	return nil
}

func (policy *RetryPolicy) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("retry must be an object")
	}
	allowed := map[string]bool{
		"max_attempts": true, "initial_delay": true, "backoff_multiplier": true,
		"max_delay": true, "jitter": true, "max_elapsed_time": true,
		"operation_id": true, "methods": true, "statuses": true,
	}
	for i := 0; i < len(node.Content); i += 2 {
		if !allowed[node.Content[i].Value] {
			return fmt.Errorf("field %s not found in retry policy", node.Content[i].Value)
		}
	}
	type plainRetryPolicy RetryPolicy
	decoded := plainRetryPolicy{
		MaxAttempts: 3, InitialDelay: Duration(time.Second), BackoffMultiplier: 2,
		MaxDelay: Duration(30 * time.Second), Jitter: 0.2,
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*policy = RetryPolicy(decoded)
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "methods":
			policy.hasMethods = true
		case "statuses":
			policy.hasStatuses = true
		}
	}
	return nil
}

// ActionSource identifies an action loaded from HTTPS, a local path, or a command.
type ActionSource struct {
	URL     string
	Path    string
	Command string
	Args    []string
}

func (source *ActionSource) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("uses must be a non-empty HTTPS URL, relative path, or command object")
		}
		if strings.HasPrefix(node.Value, "https://") || strings.HasPrefix(node.Value, "http://") {
			source.URL = node.Value
		} else {
			source.Path = node.Value
		}
		return nil
	case yaml.MappingNode:
		allowed := map[string]bool{"command": true, "args": true}
		for i := 0; i < len(node.Content); i += 2 {
			if !allowed[node.Content[i].Value] {
				return fmt.Errorf("field %s not found in action command source", node.Content[i].Value)
			}
		}
		var raw struct {
			Command string   `yaml:"command"`
			Args    []string `yaml:"args,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return err
		}
		if strings.TrimSpace(raw.Command) == "" {
			return fmt.Errorf("uses command is required")
		}
		source.Command, source.Args = raw.Command, raw.Args
		return nil
	default:
		return fmt.Errorf("uses must be a non-empty HTTPS URL, relative path, or command object")
	}
}

// Empty reports whether no action source was declared.
func (source ActionSource) Empty() bool {
	return source.URL == "" && source.Path == "" && source.Command == ""
}

// Display returns a safe description that excludes command arguments and URL query strings.
func (source ActionSource) Display() string {
	if source.URL != "" {
		parsed, err := url.Parse(source.URL)
		if err == nil {
			parsed.RawQuery, parsed.Fragment = "", ""
			return parsed.String()
		}
		return source.URL
	}
	if source.Path != "" {
		return source.Path
	}
	return source.Command
}

// Load reads and validates the workflow-level schema. Step-specific validation is performed by
// the step registry.
func Load(path string) (*Definition, error) {
	runDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("finding run directory: %w", err)
	}
	return NewLoader(nil).Load(context.Background(), path, LoadOptions{RunDir: runDir})
}

func loadLocal(path string) (*Definition, error) {
	return loadLocalWithDiagnostics(path, nil, "", "")
}

func loadLocalWithDiagnostics(path string, reporter diagnostic.Reporter, sourceRoot, sourceLabel string) (*Definition, error) {
	displaySource := path
	if sourceLabel != "" {
		displaySource = remapSource(path, sourceRoot, sourceLabel)
	}
	loadStarted := traceStart(reporter, diagnostic.PhaseDecode, diagnostic.Location{Source: displaySource}, "", "", "", "decoding workflow")
	data, err := os.ReadFile(path)
	if err != nil {
		traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, "", "", "", "", err)
		return nil, fmt.Errorf("reading workflow %s: %w", path, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, "", "", "", "", err)
		return nil, fmt.Errorf("decoding workflow %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			multipleErr := fmt.Errorf("multiple YAML documents are not supported")
			traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, "", "", "", "", multipleErr)
			return nil, fmt.Errorf("decoding workflow %s: multiple YAML documents are not supported", path)
		}
		traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, "", "", "", "", err)
		return nil, fmt.Errorf("decoding workflow %s: %w", path, err)
	}
	if err := validateDefinitionHeader(&definition); err != nil {
		traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, definition.Name, "", "", "", err)
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving workflow path %s: %w", path, err)
	}
	definition.Path = abs
	definition.Dir = filepath.Dir(abs)
	definition.sourceRoot = sourceRoot
	definition.sourceLabel = sourceLabel
	if err := resolveTemplateFiles(definition.Templates, definition.Dir, nil, sourceRoot); err != nil {
		return nil, fmt.Errorf("loading workflow templates from %s: %w", displaySource, err)
	}
	annotateDefinitionLocations(data, &definition, abs)
	if sourceLabel != "" {
		definition.Location.Source = remapSource(definition.Location.Source, sourceRoot, sourceLabel)
	}
	traceFinish(reporter, loadStarted, diagnostic.PhaseDecode, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil, countAttr("steps", len(definition.Steps)))
	requireStarted := traceStart(reporter, diagnostic.PhaseRequire, definition.Location, definition.Name, "", "", "expanding required step files")
	definition.Steps, err = expandRequiredSteps(definition.Steps, abs, nil)
	if err != nil {
		traceFinish(reporter, requireStarted, diagnostic.PhaseRequire, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, fmt.Errorf("loading workflow %s: %w", path, err)
	}
	definition.Finally, err = expandRequiredSteps(definition.Finally, abs, nil)
	if err != nil {
		traceFinish(reporter, requireStarted, diagnostic.PhaseRequire, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, fmt.Errorf("loading workflow %s finally: %w", path, err)
	}
	definition.Install, err = expandRequiredSteps(definition.Install, abs, nil)
	if err != nil {
		traceFinish(reporter, requireStarted, diagnostic.PhaseRequire, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, fmt.Errorf("loading workflow %s install: %w", path, err)
	}
	definition.Uninstall, err = expandRequiredSteps(definition.Uninstall, abs, nil)
	if err != nil {
		traceFinish(reporter, requireStarted, diagnostic.PhaseRequire, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, fmt.Errorf("loading workflow %s uninstall: %w", path, err)
	}
	if sourceLabel != "" {
		remapStepLocations(definition.Steps, sourceRoot, sourceLabel)
		remapStepLocations(definition.Finally, sourceRoot, sourceLabel)
		remapStepLocations(definition.Install, sourceRoot, sourceLabel)
		remapStepLocations(definition.Uninstall, sourceRoot, sourceLabel)
	}
	traceFinish(reporter, requireStarted, diagnostic.PhaseRequire, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil, countAttr("steps", len(definition.Steps)))
	validationStarted := traceStart(reporter, diagnostic.PhaseValidation, definition.Location, definition.Name, "", "", "validating workflow schema")
	if err := definition.ValidateStructure(); err != nil {
		traceFinish(reporter, validationStarted, diagnostic.PhaseValidation, diagnostic.StatusFailed, validationLocation(&definition, err), definition.Name, "", "", "", err)
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}
	if _, err := NewRenderer(definition.Templates); err != nil {
		traceFinish(reporter, validationStarted, diagnostic.PhaseValidation, diagnostic.StatusFailed, validationLocation(&definition, err), definition.Name, "", "", "", err)
		return nil, fmt.Errorf("validating workflow %s: %w", path, err)
	}
	traceFinish(reporter, validationStarted, diagnostic.PhaseValidation, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil)
	if definition.Vars == nil {
		definition.Vars = make(map[string]any)
	}
	if definition.Env == nil {
		definition.Env = make(Environment)
	}
	return &definition, nil
}

// ValidateStructure validates the workflow declaration independently of templates and runtime context.
func (definition *Definition) ValidateStructure() error {
	return validateDefinitionStructure(definition, true)
}

func validateDefinitionStructure(definition *Definition, allowActions bool) error {
	if err := validateDefinitionHeader(definition); err != nil {
		return err
	}
	if err := definition.ValidateOutputContract(); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(definition.Steps)+len(definition.Finally))
	if err := collectScopeIDs(definition.Steps, seen); err != nil {
		return err
	}
	if err := collectScopeIDs(definition.Finally, seen); err != nil {
		return fmt.Errorf("finally: %w", err)
	}
	if err := validateSteps(definition.Steps, allowActions, scopeTop, seen); err != nil {
		return err
	}
	if err := validateSteps(definition.Finally, allowActions, scopeFinally, seen); err != nil {
		return fmt.Errorf("finally: %w", err)
	}
	if err := validateLifecycleSteps(definition.Install, allowActions, "install"); err != nil {
		return err
	}
	if err := validateLifecycleSteps(definition.Uninstall, allowActions, "uninstall"); err != nil {
		return err
	}
	for name := range definition.Env {
		if !environmentPattern.MatchString(name) {
			return fmt.Errorf("invalid environment name %q", name)
		}
	}
	for alias, name := range definition.DependsOn {
		if !identifierPattern.MatchString(alias) {
			return fmt.Errorf("invalid dependency alias %q", alias)
		}
		if !ValidWorkflowName(name) {
			return fmt.Errorf("dependency %q has invalid workflow name %q", alias, name)
		}
	}
	return nil
}

func validateLifecycleSteps(steps []Step, allowActions bool, name string) error {
	if len(steps) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(steps))
	if err := collectScopeIDs(steps, seen); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := validateSteps(steps, allowActions, scopeTop, seen); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

type stepScope uint8

const (
	scopeTop stepScope = iota
	scopeControl
	scopeConcurrent
	scopeFinally
	scopeDefer
	scopeExecutor
	scopeExecutorControl
	scopeExecutorFinally
	scopeExecutorDefer
)

func validateStepScope(steps []Step, allowActions bool, scope stepScope, inherited map[string]struct{}) error {
	seen := make(map[string]struct{}, len(inherited)+len(steps))
	for id := range inherited {
		seen[id] = struct{}{}
	}
	if err := collectScopeIDs(steps, seen); err != nil {
		return err
	}
	return validateSteps(steps, allowActions, scope, seen)
}

func collectScopeIDs(steps []Step, seen map[string]struct{}) error {
	for i, workflowStep := range steps {
		if workflowStep.IsLoop() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			continue
		}
		container := workflowStep.IsExecutorBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsWorktreeBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil
		if container {
			if workflowStep.IsWorktreeBlock() {
				if !identifierPattern.MatchString(workflowStep.ID) {
					return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
				}
				if _, exists := seen[workflowStep.ID]; exists {
					return fmt.Errorf("duplicate step id %q", workflowStep.ID)
				}
				seen[workflowStep.ID] = struct{}{}
			}
			for _, child := range workflowStep.ChildSequences() {
				if err := collectScopeIDs(child.Steps, seen); err != nil {
					context := fmt.Sprintf("step %d", i+1)
					if workflowStep.IsExecutorBlock() {
						context += " executor"
						if child.Role == ChildFinally {
							context += " finally"
						}
					}
					if child.Role == ChildDefer {
						context += " defer"
					}
					return fmt.Errorf("%s: %w", context, err)
				}
			}
			continue
		}
		if workflowStep.Return != nil {
			continue
		}
		if workflowStep.Require != nil {
			continue
		}
		if !identifierPattern.MatchString(workflowStep.ID) {
			return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
		}
		if _, exists := seen[workflowStep.ID]; exists {
			return fmt.Errorf("duplicate step id %q", workflowStep.ID)
		}
		seen[workflowStep.ID] = struct{}{}
		if err := collectScopeIDs(workflowStep.Defer, seen); err != nil {
			return fmt.Errorf("step %q defer: %w", workflowStep.ID, err)
		}
	}
	return nil
}

func validateSteps(steps []Step, allowActions bool, scope stepScope, seen map[string]struct{}) error {
	for i, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() {
			if err := validateExecutorBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			if err := validateWorkingDirectoryBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsWorktreeBlock() {
			if err := validateWorktreeEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			if err := validateConditionalBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Require != nil {
			return fmt.Errorf("step %d: require is only supported in workflow step files", i+1)
		}
		if workflowStep.Return != nil {
			if err := validateReturnEntry(workflowStep, scope); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			if err := validateConcurrentEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil {
			if workflowStep.Loop != nil {
				if err := validateLoopEntry(workflowStep, scope, allowActions, seen); err != nil {
					return fmt.Errorf("step %q: %w", workflowStep.ID, err)
				}
				continue
			}
			if err := validateControlEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.Finally != nil {
			return fmt.Errorf("step %q: finally is only supported by executor blocks", workflowStep.ID)
		}
		if workflowStep.Defer != nil {
			if len(workflowStep.Defer) == 0 {
				return fmt.Errorf("step %q: defer must contain at least one step", workflowStep.ID)
			}
			deferScope := scopeDefer
			switch scope {
			case scopeTop:
			case scopeExecutor:
				deferScope = scopeExecutorDefer
			case scopeConcurrent, scopeControl, scopeExecutorControl:
				return fmt.Errorf("step %q: defer is only supported in sequential scopes", workflowStep.ID)
			case scopeFinally, scopeDefer, scopeExecutorFinally, scopeExecutorDefer:
				return fmt.Errorf("step %q: defer is not supported inside cleanup", workflowStep.ID)
			}
			if err := validateSteps(workflowStep.Defer, allowActions, deferScope, seen); err != nil {
				return fmt.Errorf("step %q defer: %w", workflowStep.ID, err)
			}
		}
		if executorStepScope(scope) && !workflowStep.Uses.Empty() {
			return fmt.Errorf("step %q: actions are not supported inside executor blocks", workflowStep.ID)
		}
		if (workflowStep.Type == "") == workflowStep.Uses.Empty() {
			return fmt.Errorf("step %q must set exactly one of type or uses", workflowStep.ID)
		}
		if !workflowStep.Uses.Empty() && !allowActions {
			return fmt.Errorf("step %q: nested composite actions are not supported", workflowStep.ID)
		}
		if workflowStep.Uses.Empty() && workflowStep.SHA256 != "" {
			return fmt.Errorf("step %q: sha256 requires uses", workflowStep.ID)
		}
		if err := workflowStep.ValidateExecutionPolicy(); err != nil {
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		if workflowStep.With == nil {
			workflowStep.With = make(map[string]any)
			steps[i] = workflowStep
		}
	}
	return nil
}

func validateWorkingDirectoryBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.validateBlock(); err != nil {
		return err
	}
	return validateSteps(workflowStep.Steps, allowActions, scope, seen)
}

func validateWorktreeEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.ValidateBlock(); err != nil {
		return err
	}
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("worktree block requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.WorkingDirectory != "" || workflowStep.Steps != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
		return fmt.Errorf("worktree block cannot be combined with other step fields")
	}
	return validateSteps(workflowStep.Worktree.Steps, allowActions, scope, seen)
}

func validateConditionalBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.validateBlock(); err != nil {
		return err
	}
	if scope == scopeConcurrent {
		return fmt.Errorf("conditional blocks are not supported inside concurrent groups")
	}
	for _, child := range workflowStep.Steps {
		if child.IsConditionalBlock() {
			return fmt.Errorf("nested conditional blocks are not supported")
		}
	}
	return validateSteps(workflowStep.Steps, allowActions, scope, seen)
}

func validateConcurrentEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsConditionalBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
		return fmt.Errorf("concurrent cannot be combined with other step fields")
	}
	if scope == scopeConcurrent {
		return fmt.Errorf("nested concurrent groups are not supported")
	}
	if executorStepScope(scope) {
		return fmt.Errorf("concurrent groups are not supported inside executor blocks")
	}
	group := workflowStep.Concurrent
	if err := group.Validate(); err != nil {
		return err
	}
	return validateSteps(group.Steps, allowActions, scopeConcurrent, seen)
}

func executorStepScope(scope stepScope) bool {
	return scope == scopeExecutor || scope == scopeExecutorControl || scope == scopeExecutorFinally || scope == scopeExecutorDefer
}

func validateControlEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	controlCount := 0
	for _, present := range []bool{workflowStep.Batch != nil, workflowStep.Foreach != nil, workflowStep.Matrix != nil} {
		if present {
			controlCount++
		}
	}
	if controlCount != 1 {
		return fmt.Errorf("must set exactly one of batch, foreach or matrix")
	}
	kind := "foreach"
	var children []Step
	var validationErr error
	if workflowStep.Batch != nil {
		kind = "batch"
		children = workflowStep.Batch.Steps
		validationErr = workflowStep.Batch.Validate()
	} else if workflowStep.Matrix != nil {
		kind = "matrix"
		children = workflowStep.Matrix.Steps
		validationErr = workflowStep.Matrix.Validate()
	} else {
		children = workflowStep.Foreach.Steps
		validationErr = workflowStep.Foreach.Validate()
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
		return fmt.Errorf("%s cannot be combined with ordinary step fields", kind)
	}
	if scope != scopeTop && scope != scopeExecutor {
		return fmt.Errorf("nested %s controls are not supported", kind)
	}
	if validationErr != nil {
		return validationErr
	}
	childScope := scopeControl
	if scope == scopeExecutor {
		if maxConcurrency := controlMaxConcurrency(workflowStep); maxConcurrency != 1 {
			return fmt.Errorf("%s inside executor must use max_concurrency 1", kind)
		}
		childScope = scopeExecutorControl
	}
	return validateStepScope(children, allowActions, childScope, enclosing)
}

func validateLoopEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("loop requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil || workflowStep.If != "" {
		return fmt.Errorf("loop cannot be combined with ordinary step fields")
	}
	if scope != scopeTop && scope != scopeExecutor {
		return fmt.Errorf("nested loops are not supported")
	}
	if err := workflowStep.Loop.Validate(); err != nil {
		return err
	}
	if err := controlpkg.ValidateExpression(string(workflowStep.Loop.Until)); err != nil {
		return fmt.Errorf("loop until: %w", err)
	}
	childScope := scopeControl
	if scope == scopeExecutor {
		childScope = scopeExecutorControl
	}
	return validateStepScope(workflowStep.Loop.Steps, allowActions, childScope, enclosing)
}

func controlMaxConcurrency(workflowStep Step) int {
	if workflowStep.Batch != nil {
		return workflowStep.Batch.MaxConcurrency
	}
	if workflowStep.Foreach != nil {
		return workflowStep.Foreach.MaxConcurrency
	}
	return workflowStep.Matrix.MaxConcurrency
}

func validateExecutorBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if scope != scopeTop {
		return fmt.Errorf("executor blocks are only supported in sequential workflow scopes")
	}
	if !allowActions {
		return fmt.Errorf("executor blocks are not supported in composite actions")
	}
	if workflowStep.Executor == nil || strings.TrimSpace(workflowStep.Executor.Type) == "" {
		return fmt.Errorf("executor type is required")
	}
	if len(workflowStep.Steps) == 0 {
		return fmt.Errorf("executor block must contain at least one step")
	}
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Defer != nil || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Timeout != nil || workflowStep.Retry != nil || workflowStep.With != nil {
		return fmt.Errorf("executor block cannot be combined with other step fields")
	}
	if err := validateSteps(workflowStep.Steps, allowActions, scopeExecutor, seen); err != nil {
		return fmt.Errorf("executor steps: %w", err)
	}
	if err := validateSteps(workflowStep.Finally, allowActions, scopeExecutorFinally, seen); err != nil {
		return fmt.Errorf("executor finally: %w", err)
	}
	return nil
}

// Validate checks the safety limits of a concurrent group.
func (group ConcurrentGroup) Validate() error {
	if len(group.Steps) < 2 {
		return fmt.Errorf("concurrent group must contain at least two steps")
	}
	if group.MaxConcurrency < 1 || group.MaxConcurrency > 100 {
		return fmt.Errorf("concurrent max_concurrency must be between 1 and 100")
	}
	if group.Timeout != nil && group.Timeout.Value() <= 0 {
		return fmt.Errorf("concurrent timeout must be greater than zero")
	}
	return nil
}

// ValidateExecutionPolicy validates retry and timeout settings for a step.
func (workflowStep Step) ValidateExecutionPolicy() error {
	if workflowStep.Timeout != nil && workflowStep.Timeout.Value() <= 0 {
		return fmt.Errorf("timeout must be greater than zero")
	}
	policy := workflowStep.Retry
	if policy == nil {
		return nil
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > 100 {
		return fmt.Errorf("retry max_attempts must be between 1 and 100")
	}
	if policy.InitialDelay.Value() < 0 {
		return fmt.Errorf("retry initial_delay cannot be negative")
	}
	if math.IsNaN(policy.BackoffMultiplier) || math.IsInf(policy.BackoffMultiplier, 0) || policy.BackoffMultiplier < 1 {
		return fmt.Errorf("retry backoff_multiplier must be at least 1")
	}
	if policy.MaxDelay.Value() < policy.InitialDelay.Value() {
		return fmt.Errorf("retry max_delay cannot be less than initial_delay")
	}
	if math.IsNaN(policy.Jitter) || math.IsInf(policy.Jitter, 0) || policy.Jitter < 0 || policy.Jitter > 1 {
		return fmt.Errorf("retry jitter must be between 0 and 1")
	}
	if policy.MaxElapsedTime.Value() < 0 {
		return fmt.Errorf("retry max_elapsed_time cannot be negative")
	}
	if policy.OperationID != "" && strings.TrimSpace(policy.OperationID) == "" {
		return fmt.Errorf("retry operation_id cannot be blank")
	}
	if workflowStep.Type != "http" && (policy.hasMethods || policy.hasStatuses || len(policy.Methods) > 0 || len(policy.Statuses) > 0) {
		return fmt.Errorf("retry methods and statuses are only supported for http steps")
	}
	if policy.hasMethods && len(policy.Methods) == 0 {
		return fmt.Errorf("retry methods must contain at least one HTTP method")
	}
	if policy.hasStatuses && len(policy.Statuses) == 0 {
		return fmt.Errorf("retry statuses must contain at least one HTTP status or range")
	}
	seenMethods := make(map[string]bool, len(policy.Methods))
	for i, method := range policy.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || !httpMethodPattern.MatchString(method) {
			return fmt.Errorf("retry method %q is not a valid HTTP method", policy.Methods[i])
		}
		if seenMethods[method] {
			return fmt.Errorf("retry method %q is duplicated", method)
		}
		seenMethods[method] = true
		policy.Methods[i] = method
	}
	for i, status := range policy.Statuses {
		if status.From < 100 || status.From > 599 || status.To < 100 || status.To > 599 || status.From > status.To {
			return fmt.Errorf("retry status range %d-%d must be ascending and between 100 and 599", status.From, status.To)
		}
		for _, previous := range policy.Statuses[:i] {
			if status.From <= previous.To && previous.From <= status.To {
				return fmt.Errorf("retry status ranges %d-%d and %d-%d overlap", previous.From, previous.To, status.From, status.To)
			}
		}
	}
	return nil
}

func validateDefinitionHeader(definition *Definition) error {
	if definition.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", definition.Version)
	}
	if definition.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(definition.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	if definition.Cron == "" {
		if definition.Timezone != "" {
			return fmt.Errorf("timezone requires cron")
		}
		return nil
	}
	if _, err := workflowschedule.Parse(definition.Cron, definition.Timezone); err != nil {
		return err
	}
	return nil
}

// ValidEnvironmentName reports whether name is a portable POSIX-style environment name.
func ValidEnvironmentName(name string) bool { return environmentPattern.MatchString(name) }

// ValidWorkflowName reports whether name can be resolved through workflow discovery.
func ValidWorkflowName(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}
