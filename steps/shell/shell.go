package shell

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/ptyinteract"
	"github.com/up2jj/wuko/scriptsdk"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	"go.yaml.in/yaml/v3"
)

const ttyCaptureLimit = 1 << 20

// filesArgumentBudget bounds the arguments of one run when `files` splits a long list, well
// under the smallest common OS limit and the 128 KiB Linux allows for any single argument.
const filesArgumentBudget = 128 << 10

type Config struct {
	Command          string               `yaml:"command,omitempty"`
	Script           string               `yaml:"script,omitempty"`
	Shell            string               `yaml:"shell,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	Argv             *ArgvExpression      `yaml:"argv,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	User             string               `yaml:"user,omitempty"`
	Elevated         bool                 `yaml:"elevated,omitempty"`
	Stdin            string               `yaml:"stdin,omitempty"`
	TTY              bool                 `yaml:"tty,omitempty"`
	Replace          bool                 `yaml:"replace,omitempty"`
	Interactions     interactionsConfig   `yaml:"interactions,omitempty"`
	Interact         bool                 `yaml:"interact,omitempty"`
	Terminal         *terminalConfig      `yaml:"terminal,omitempty"`
	Stdout           string               `yaml:"stdout,omitempty"`
	Stderr           string               `yaml:"stderr,omitempty"`
	CaptureLimit     string               `yaml:"capture_limit,omitempty"`
	AllowedExitCodes []int                `yaml:"allowed_exit_codes,omitempty"`
	SDK              string               `yaml:"sdk,omitempty"`
	SDKArgs          map[string]any       `yaml:"sdk_args,omitempty"`
	Files            any                  `yaml:"files,omitempty"`
}

type ArgvExpression struct {
	Expr string `yaml:"expr"`
}

// bindingConfig describes the exit-code union accepted before Config is decoded.
type bindingConfig struct {
	Config           `yaml:",inline"`
	AllowedExitCodes any `yaml:"allowed_exit_codes"`
}

func (expression *ArgvExpression) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("argv must be an object containing expr")
	}
	if len(node.Content) != 2 || node.Content[0].Value != "expr" {
		return fmt.Errorf("argv must contain exactly the expr field")
	}
	if node.Content[1].Kind != yaml.ScalarNode || node.Content[1].Tag != "!!str" {
		return fmt.Errorf("argv expr must be a string")
	}
	expression.Expr = node.Content[1].Value
	return nil
}

type Runner struct {
	config          Config
	exitCodes       process.ExitCodePolicy
	stdoutPolicy    process.OutputPolicy
	stderrPolicy    process.OutputPolicy
	captureLimit    int64
	argvProgram     *vm.Program
	interactionExpr *vm.Program
	interactions    *ptyinteract.Plan
	hasInteractions bool
	sdkArgPrograms  map[string]*vm.Program
	files           step.PathList
}

func (*Runner) ExecutorAware() {}

// ReplacesProcess reports that the step ends the run by replacing the Wuko process.
func (r *Runner) ReplacesProcess() bool { return r.config.Replace }

type sdkRunner struct{ *Runner }

func (*sdkRunner) InlineWorkflowAware() {}

func shellOutputs(sdk, files bool) step.OutputSchema {
	schema := step.ClosedOutputs("stdout", "stderr", "exit_code", "stdout_truncated", "stderr_truncated")
	if sdk {
		schema.Fields["data"] = step.OpenObject()
	}
	if files {
		schema.Fields["runs"] = step.Scalar()
	}
	return schema
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("shell", step.Registration{
		Builder: New,
		Config:  bindingConfig{},
		Outputs: shellOutputs(true, true),
		ConfigOutputs: func(raw map[string]any) step.OutputSchema {
			if replace, _ := raw["replace"].(bool); replace {
				return step.ClosedOutputs()
			}
			sdk, _ := raw["sdk"].(string)
			_, files := raw["files"]
			return shellOutputs(sdk != "", files)
		},
		References: references,
	})
}

// references reports the expressions that compute a command line or interaction script.
func references(raw map[string]any) (step.References, error) {
	argv, _ := raw["argv"].(map[string]any)
	interactions, _ := raw["interactions"].(map[string]any)
	argvExpr := step.ExpressionField(argv, "expr")
	argvExpr.Label = "argv expr"
	interactionsExpr := step.ExpressionField(interactions, "expr")
	interactionsExpr.Label = "interactions expr"
	references := step.References{Expressions: []step.ExpressionReference{argvExpr, interactionsExpr, step.PathListReference(raw, "files")}}
	args, _ := raw["sdk_args"].(map[string]any)
	for name, value := range args {
		binding, ok := value.(map[string]any)
		if ok && len(binding) == 1 {
			reference := step.ExpressionField(binding, "expr")
			reference.Label = "sdk_args." + name + ".expr"
			references.Expressions = append(references.Expressions, reference)
		}
	}
	return references, nil
}

func New(raw map[string]any) (step.Runner, error) {
	exitCodeConfig := raw
	if !step.ConfigKnown(raw, "allowed_exit_codes") {
		exitCodeConfig = maps.Clone(raw)
		delete(exitCodeConfig, "allowed_exit_codes")
	}
	decodedRaw, allowAnyExitCode, err := process.NormalizeAllowedExitCodes(exitCodeConfig)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := step.DecodeConfig(decodedRaw, &config); err != nil {
		return nil, err
	}
	if config.SDK != "" && config.SDK != "node" && config.SDK != "bash" {
		return nil, fmt.Errorf("sdk must be node or bash")
	}
	if _, present := raw["sdk_args"]; present && config.SDK == "" {
		return nil, fmt.Errorf("sdk_args requires sdk")
	}
	if step.ConfigKnown(raw, "elevated", "user") && (config.SDK != "" && (config.User != "" || config.Elevated)) {
		return nil, fmt.Errorf("sdk cannot be combined with user or elevated")
	}
	sdkArgPrograms := make(map[string]*vm.Program)
	for name, value := range config.SDKArgs {
		binding, ok := value.(map[string]any)
		if !ok || len(binding) != 1 {
			continue
		}
		source, exists := binding["expr"]
		if !exists {
			continue
		}
		text, ok := source.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("sdk_args.%s.expr must be a non-empty string", name)
		}
		program, err := wukoexpr.Compile(text, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("compiling sdk_args.%s.expr: %w", name, err)
		}
		sdkArgPrograms[name] = program
	}
	_, hasArgv := raw["argv"]
	if hasArgv {
		for _, field := range []string{"command", "script", "shell", "args"} {
			if _, exists := raw[field]; exists {
				return nil, fmt.Errorf("argv cannot be combined with %s", field)
			}
		}
		if config.Argv == nil {
			return nil, fmt.Errorf("argv must be an object containing expr")
		}
		if strings.TrimSpace(config.Argv.Expr) == "" {
			return nil, fmt.Errorf("argv expr must be a non-empty string")
		}
	} else if (config.Command == "") == (config.Script == "") {
		return nil, fmt.Errorf("exactly one of command or script is required")
	}
	if config.Script != "" && strings.TrimSpace(config.Script) == "" {
		return nil, fmt.Errorf("script cannot be blank")
	}
	if step.ConfigKnown(raw, "elevated", "user") && (config.Elevated && config.User != "") {
		return nil, fmt.Errorf("elevated cannot be combined with user")
	}
	if step.ConfigKnown(raw, "stdin", "tty") && (config.TTY && config.Stdin != "") {
		return nil, fmt.Errorf("tty and stdin cannot be combined")
	}
	if step.ConfigKnown(raw, "capture_limit", "stderr", "stdout", "tty") && (config.TTY && (config.Stdout != "" || config.Stderr != "" || config.CaptureLimit != "")) {
		return nil, fmt.Errorf("tty cannot be combined with stdout, stderr, or capture_limit")
	}
	_, hasInteractions := raw["interactions"]
	_, hasInteract := raw["interact"]
	if hasInteractions {
		if raw["interactions"] == nil {
			return nil, fmt.Errorf("interactions must be a list or an object containing expr")
		}
		if step.ConfigKnown(raw, "tty") && !config.TTY {
			return nil, fmt.Errorf("interactions require tty")
		}
	} else if hasInteract {
		return nil, fmt.Errorf("interact requires interactions")
	}
	if step.ConfigKnown(raw, "interact", "tty") && (config.Interact && !config.TTY) {
		return nil, fmt.Errorf("interact requires tty")
	}
	handoff := config.TTY && (!hasInteractions || config.Interact)
	if err := validateTerminalConfig(config.Terminal, config.TTY, handoff); step.ConfigKnown(raw, "terminal", "tty") && err != nil {
		return nil, err
	}
	if err := validateReplaceConfig(raw, config); err != nil {
		return nil, err
	}
	files, err := step.ParsePathList(raw, "files")
	if err != nil {
		return nil, err
	}
	if step.ConfigKnown(raw, "tty") && (files.Set() && (config.TTY || hasInteractions || config.SDK != "")) {
		return nil, fmt.Errorf("files cannot be combined with tty, interactions, or sdk")
	}
	_, exitCodesConfigured := exitCodeConfig["allowed_exit_codes"]
	exitCodes, err := process.NewExitCodePolicy(config.AllowedExitCodes, exitCodesConfigured, allowAnyExitCode)
	if err != nil {
		return nil, err
	}
	for key := range config.Env {
		if !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment name %q", key)
		}
	}
	stdoutPolicy, err := configuredOutputPolicy("stdout", config.Stdout)
	if err != nil {
		return nil, err
	}
	stderrPolicy, err := configuredOutputPolicy("stderr", config.Stderr)
	if err != nil {
		return nil, err
	}
	captureLimit := int64(0)
	if config.CaptureLimit != "" && !templated(config.CaptureLimit) {
		captureLimit, err = process.ParseCaptureLimit(config.CaptureLimit)
		if err != nil {
			return nil, fmt.Errorf("capture_limit %w", err)
		}
	}
	var argvProgram *vm.Program
	if config.Argv != nil {
		argvProgram, err = wukoexpr.Compile(config.Argv.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("compiling argv expr: %w", err)
		}
	}
	var interactions *ptyinteract.Plan
	var interactionExpr *vm.Program
	if config.Interactions.Expr != "" {
		interactionExpr, err = wukoexpr.Compile(config.Interactions.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return nil, fmt.Errorf("compiling interactions expr: %w", err)
		}
	} else if hasInteractions {
		interactions, err = compileInteractions(config.Interactions.Static)
		if err != nil {
			return nil, err
		}
	}
	runner := &Runner{
		config: config, exitCodes: exitCodes,
		stdoutPolicy: stdoutPolicy, stderrPolicy: stderrPolicy, captureLimit: captureLimit,
		argvProgram: argvProgram, interactionExpr: interactionExpr, interactions: interactions, hasInteractions: hasInteractions,
		sdkArgPrograms: sdkArgPrograms, files: files,
	}
	if config.SDK != "" {
		return &sdkRunner{runner}, nil
	}
	return runner, nil
}

// replaceIncompatibleFields lists the settings that need Wuko to keep running after the
// command starts, or that would reparse the command through a shell.
var replaceIncompatibleFields = []string{
	"script", "shell", "sdk", "sdk_args", "files", "stdin", "stdout", "stderr", "capture_limit",
	"allowed_exit_codes", "interactions", "interact", "terminal", "user", "elevated",
}

func validateReplaceConfig(raw map[string]any, config Config) error {
	if !config.Replace {
		return nil
	}
	if !config.TTY {
		return fmt.Errorf("replace requires tty")
	}
	for _, field := range replaceIncompatibleFields {
		if _, exists := raw[field]; exists {
			return fmt.Errorf("replace cannot be combined with %s", field)
		}
	}
	return nil
}

func (r *Runner) Validate(_ context.Context, request step.Request) error {
	if r.config.Replace {
		if request.InsideExecutor {
			return fmt.Errorf("replace is not supported inside executor blocks")
		}
		if request.ReplaceProcess == nil {
			return fmt.Errorf("replace is available only to the final step of a workflow run directly by wuko run; cron schedules, dependency workflows, run_workflow, actions, and the web UI cannot replace the process")
		}
	}
	if r.config.SDK != "" && request.InsideExecutor {
		return fmt.Errorf("sdk injection is not supported inside executor blocks")
	}
	if !r.config.Elevated {
		return nil
	}
	if !request.ElevationAllowed {
		return fmt.Errorf("elevated execution is allowed only for trusted local workflow files and local path actions")
	}
	if request.InsideExecutor {
		return fmt.Errorf("elevated execution is not supported inside executor blocks")
	}
	if request.ElevatedExecutor == nil {
		return fmt.Errorf("elevated execution is unavailable in this host")
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.Validate(ctx, request); err != nil {
		return step.Result{}, err
	}
	handoff := r.config.TTY && (!r.hasInteractions || r.config.Interact)
	if handoff && (!request.Interactive || request.Stdin == nil) {
		return step.Result{}, fmt.Errorf("tty requires an interactive terminal")
	}
	interactions, err := r.interactionPlan(request)
	if err != nil {
		return step.Result{}, err
	}
	command, args, err := r.command(request)
	if err != nil {
		return step.Result{}, err
	}
	dir := r.config.WorkingDirectory
	if dir == "" {
		dir = request.RunDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(request.RunDir, dir)
	}
	// An elevated command starts from a clean root baseline instead of the workflow environment,
	// so the inherited values are never cloned in the first place.
	inherited := request.Env
	if r.config.Elevated {
		inherited = nil
	}
	environment := make(map[string]string, len(inherited)+len(r.config.Env)+3)
	maps.Copy(environment, inherited)
	maps.Copy(environment, r.config.Env)
	environment = step.ApplyAttemptEnvironment(environment, request)
	if r.config.Replace {
		replacement, err := process.ResolveReplacement(append([]string{command}, args...), dir, environment)
		if err != nil {
			return step.Result{}, err
		}
		return step.Result{}, request.ReplaceProcess(replacement)
	}
	var bridge *scriptsdk.Bridge
	if r.config.SDK != "" {
		if request.InsideExecutor {
			// Validation is deferred inside some blocks, so enforce the local-only bridge here too.
			return step.Result{}, fmt.Errorf("sdk injection is not supported inside executor blocks")
		}
		sdkArgs := maps.Clone(r.config.SDKArgs)
		for name, program := range r.sdkArgPrograms {
			value, err := expr.Run(program, request.ExpressionEnvironment(nil))
			if err != nil {
				return step.Result{}, fmt.Errorf("evaluating sdk_args.%s.expr: %w", name, err)
			}
			sdkArgs[name] = value
		}
		bridgeRequest := request
		bridgeRequest.Env = environment
		bridge, err = scriptsdk.Open(ctx, bridgeRequest, sdkArgs)
		if err != nil {
			return step.Result{}, err
		}
		defer bridge.Close()
		switch r.config.SDK {
		case "node":
			err = bridge.InjectNode(environment)
		case "bash":
			var executable string
			executable, err = os.Executable()
			if err == nil {
				err = bridge.InjectBash(environment, executable)
			}
		}
		if err != nil {
			return step.Result{}, err
		}
	}
	executor := request.Executor
	if r.config.Elevated {
		executor = request.ElevatedExecutor
	}
	if executor == nil {
		executor = process.LocalExecutor{}
	}
	stdin := process.StringInput(r.config.Stdin)
	captureLimit := r.captureLimit
	if r.config.TTY {
		if handoff {
			stdin = request.Stdin
		} else {
			stdin = nil
		}
		captureLimit = ttyCaptureLimit
	}
	run := func(args []string) (process.Result, error) {
		return executor.Run(ctx, process.Options{
			Command: command, Args: args, Dir: dir, Env: environment, User: r.config.User,
			Stdin: stdin, Stdout: request.Stdout, Stderr: request.Stderr, TTY: r.config.TTY,
			PromptStdin: request.Stdin, PromptStderr: request.Stderr, Interactive: request.Interactive,
			Interactions: interactions, Interact: r.config.Interact, CaptureLimit: captureLimit,
			Terminal:     terminalAppearance(r.config.Terminal),
			StdoutPolicy: r.stdoutPolicy, StderrPolicy: r.stderrPolicy,
		})
	}
	if r.files.Set() {
		return r.runFiles(ctx, request, command, args, run)
	}
	result, err := run(args)
	outputs := map[string]any{
		"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode,
		"stdout_truncated": result.StdoutTruncated, "stderr_truncated": result.StderrTruncated,
	}
	var variables map[string]any
	if bridge != nil {
		closeErr := bridge.Close()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing script SDK: %w", closeErr))
		}
		staged := bridge.Result()
		outputs["data"] = staged.Outputs
		variables = staged.Variables
	}
	return step.Result{Outputs: outputs, Variables: variables}, r.failure(command, result, err)
}

// failure reports whether one run failed, honouring allowed_exit_codes.
func (r *Runner) failure(command string, result process.Result, err error) error {
	if err != nil {
		exitErr, isExitError := soleProcessExitError(err)
		if isExitError && exitErr.Code == result.ExitCode && r.exitCodes.Allows(result.ExitCode) {
			return nil
		}
		return err
	}
	if !r.exitCodes.Allows(result.ExitCode) {
		return &process.ExitError{Command: command, Code: result.ExitCode}
	}
	return nil
}

// runFiles appends the configured files to the command, splitting them across runs that stay
// within filesArgumentBudget. Every run happens even after one fails, so a formatter still
// reaches every file; the first failure is reported. Without files the command does not run,
// since many tools would read stdin instead.
func (r *Runner) runFiles(ctx context.Context, request step.Request, command string, args []string, run func([]string) (process.Result, error)) (step.Result, error) {
	paths, err := r.files.Resolve(request)
	if err != nil {
		return step.Result{}, err
	}
	var stdout, stderr strings.Builder
	exitCode, runs := 0, 0
	stdoutTruncated, stderrTruncated := false, false
	var failure error
	for _, chunk := range chunkPaths(command, args, paths) {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		result, err := run(append(slices.Clone(args), chunk...))
		runs++
		stdout.WriteString(result.Stdout)
		stderr.WriteString(result.Stderr)
		stdoutTruncated = stdoutTruncated || result.StdoutTruncated
		stderrTruncated = stderrTruncated || result.StderrTruncated
		if chunkFailure := r.failure(command, result, err); chunkFailure != nil && failure == nil {
			failure, exitCode = chunkFailure, result.ExitCode
		}
	}
	outputs := map[string]any{
		"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": exitCode,
		"stdout_truncated": stdoutTruncated, "stderr_truncated": stderrTruncated, "runs": runs,
	}
	return step.Result{Outputs: outputs}, failure
}

// chunkPaths splits paths so that each run's command line stays within filesArgumentBudget,
// counting each argument with its terminating NUL. A path too long for any budget runs alone.
func chunkPaths(command string, args, paths []string) [][]string {
	fixed := len(command) + 1
	for _, arg := range args {
		fixed += len(arg) + 1
	}
	var chunks [][]string
	var current []string
	size := fixed
	for _, path := range paths {
		if len(current) > 0 && size+len(path)+1 > filesArgumentBudget {
			chunks = append(chunks, current)
			current, size = nil, fixed
		}
		current = append(current, path)
		size += len(path) + 1
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func soleProcessExitError(err error) (*process.ExitError, bool) {
	if exitErr, ok := err.(*process.ExitError); ok {
		return exitErr, true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return soleProcessExitError(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 1 {
			return soleProcessExitError(causes[0])
		}
	}
	return nil, false
}

func configuredOutputPolicy(field, value string) (process.OutputPolicy, error) {
	if templated(value) {
		return process.OutputTee, nil
	}
	policy, err := process.ParseOutputPolicy(value)
	if err != nil {
		return process.OutputTee, fmt.Errorf("%s %w", field, err)
	}
	return policy, nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }

func (r *Runner) command(request step.Request) (string, []string, error) {
	if r.argvProgram != nil {
		value, err := expr.Run(r.argvProgram, expressionEnvironmentFor(request))
		if err != nil {
			return "", nil, fmt.Errorf("evaluating argv expr: %w", err)
		}
		argv, err := argvStrings(value)
		if err != nil {
			return "", nil, err
		}
		return r.sdkCommand(argv[0], argv[1:])
	}
	if r.config.Command != "" {
		return r.sdkCommand(r.config.Command, r.config.Args)
	}
	shell := r.config.Shell
	if shell == "" {
		shell = "/bin/sh"
		if r.config.SDK == "bash" {
			shell = "bash"
		}
	}
	args := []string{"-c", r.config.Script, "wuko"}
	args = append(args, r.config.Args...)
	return r.sdkCommand(shell, args)
}

func (r *Runner) sdkCommand(command string, args []string) (string, []string, error) {
	if r.config.SDK != "bash" {
		return command, args, nil
	}
	if filepath.Base(command) != "bash" {
		return "", nil, fmt.Errorf("sdk: bash requires running Bash directly (command, shell, or argv)")
	}
	// Bash keeps parsing options after -c until the first operand, so the scan
	// must not stop at -c: `bash -c -i SOURCE` is still interactive.
	source := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || len(arg) < 2 || (arg[0] != '-' && arg[0] != '+') {
			if arg != "--" && arg != "-" {
				source = true // A script file operand.
			} else if i+1 < len(args) {
				source = true
			}
			break
		}
		switch arg {
		case "--posix":
			return "", nil, fmt.Errorf("sdk: bash does not support POSIX mode")
		case "--restricted":
			return "", nil, fmt.Errorf("sdk: bash requires unrestricted Bash to run the Wuko executable")
		case "--rcfile", "--init-file":
			i++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			continue
		}
		for _, option := range arg[1:] {
			if arg[0] == '-' && (option == 'i' || option == 'p') {
				return "", nil, fmt.Errorf("sdk: bash requires noninteractive Bash without privileged mode")
			}
			if arg[0] == '-' && option == 'r' {
				return "", nil, fmt.Errorf("sdk: bash requires unrestricted Bash to run the Wuko executable")
			}
			if option == 'c' {
				source = true
			}
			if option == 'o' || option == 'O' {
				i++ // Named options may also appear in groups such as -euo pipefail.
				if arg[0] == '-' && option == 'o' && i < len(args) && args[i] == "posix" {
					return "", nil, fmt.Errorf("sdk: bash does not support POSIX mode")
				}
			}
		}
	}
	if r.config.TTY && !source {
		// Without -c or a script operand, Bash reading a terminal is interactive and skips BASH_ENV.
		return "", nil, fmt.Errorf("sdk: bash with tty requires Bash -c or a script file")
	}
	return command, args, nil
}

func (r *Runner) interactionPlan(request step.Request) (*ptyinteract.Plan, error) {
	if r.interactionExpr == nil {
		if r.hasInteractions && r.interactions == nil {
			return nil, fmt.Errorf("interactions contain unresolved templates")
		}
		return r.interactions, nil
	}
	value, err := expr.Run(r.interactionExpr, expressionEnvironmentFor(request))
	if err != nil {
		return nil, fmt.Errorf("evaluating interactions expr: %w", err)
	}
	configs, err := decodeInteractionExpressionResult(value)
	if err != nil {
		return nil, err
	}
	plan, err := compileInteractions(configs)
	if err != nil {
		return nil, fmt.Errorf("interactions expr result: %w", err)
	}
	return plan, nil
}

func expressionEnvironmentFor(request step.Request) map[string]any {
	return request.ExpressionEnvironment(nil)
}

func argvStrings(value any) ([]string, error) {
	list := reflect.ValueOf(value)
	if !list.IsValid() || (list.Kind() != reflect.Array && list.Kind() != reflect.Slice) {
		return nil, fmt.Errorf("argv expr returned %T, want a list", value)
	}
	if list.Len() == 0 {
		return nil, fmt.Errorf("argv expr returned an empty list")
	}
	argv := make([]string, list.Len())
	for i := range list.Len() {
		argument, err := argvScalar(list.Index(i))
		if err != nil {
			return nil, fmt.Errorf("argv expr item %d: %w", i, err)
		}
		argv[i] = argument
	}
	if argv[0] == "" {
		return nil, fmt.Errorf("argv expr returned an empty executable")
	}
	return argv, nil
}

func argvScalar(value reflect.Value) (string, error) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return "", fmt.Errorf("null is not a scalar argument")
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return "", fmt.Errorf("null is not a scalar argument")
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return "", fmt.Errorf("null is not a scalar argument")
	}
	switch value.Kind() {
	case reflect.String:
		return value.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		float := value.Float()
		if math.IsNaN(float) || math.IsInf(float, 0) {
			return "", fmt.Errorf("non-finite number is not a scalar argument")
		}
		return strconv.FormatFloat(float, 'g', -1, value.Type().Bits()), nil
	default:
		return "", fmt.Errorf("%s is not a scalar argument", value.Kind())
	}
}
