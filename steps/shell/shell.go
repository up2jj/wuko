package shell

import (
	"context"
	"fmt"
	"maps"
	"math"
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
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
	"gopkg.in/yaml.v3"
)

const ttyCaptureLimit = 1 << 20

type Config struct {
	Command          string               `yaml:"command,omitempty"`
	Script           string               `yaml:"script,omitempty"`
	Shell            string               `yaml:"shell,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	Argv             *ArgvExpression      `yaml:"argv,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	User             string               `yaml:"user,omitempty"`
	Stdin            string               `yaml:"stdin,omitempty"`
	TTY              bool                 `yaml:"tty,omitempty"`
	Interactions     interactionsConfig   `yaml:"interactions,omitempty"`
	Interact         bool                 `yaml:"interact,omitempty"`
	Terminal         *terminalConfig      `yaml:"terminal,omitempty"`
	Stdout           string               `yaml:"stdout,omitempty"`
	Stderr           string               `yaml:"stderr,omitempty"`
	CaptureLimit     string               `yaml:"capture_limit,omitempty"`
	AllowedExitCodes []int                `yaml:"allowed_exit_codes,omitempty"`
}

type ArgvExpression struct {
	Expr string `yaml:"expr"`
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

type expressionEnvironment struct {
	Inputs       map[string]any               `expr:"inputs"`
	Vars         map[string]any               `expr:"vars"`
	Env          map[string]string            `expr:"env"`
	Steps        map[string]any               `expr:"steps"`
	Dependencies map[string]map[string]any    `expr:"dependencies"`
	Batch        map[string]any               `expr:"batch"`
	Foreach      map[string]any               `expr:"foreach"`
	Matrix       map[string]any               `expr:"matrix"`
	Observe      map[string]any               `expr:"observe"`
	Finally      map[string]any               `expr:"finally"`
	Error        map[string]any               `expr:"error"`
	Workflow     step.WorkflowValue           `expr:"workflow"`
	Run          step.RunValue                `expr:"run"`
	Secret       func(string) (string, error) `expr:"secret"`
}

type Runner struct {
	config          Config
	stdoutPolicy    process.OutputPolicy
	stderrPolicy    process.OutputPolicy
	captureLimit    int64
	argvProgram     *vm.Program
	interactionExpr *vm.Program
	interactions    *ptyinteract.Plan
	hasInteractions bool
}

func (*Runner) ExecutorAware() {}

func Register(registry *step.Registry) error { return registry.Register("shell", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
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
	if config.TTY && config.Stdin != "" {
		return nil, fmt.Errorf("tty and stdin cannot be combined")
	}
	if config.TTY && (config.Stdout != "" || config.Stderr != "" || config.CaptureLimit != "") {
		return nil, fmt.Errorf("tty cannot be combined with stdout, stderr, or capture_limit")
	}
	_, hasInteractions := raw["interactions"]
	_, hasInteract := raw["interact"]
	if hasInteractions {
		if raw["interactions"] == nil {
			return nil, fmt.Errorf("interactions must be a list or an object containing expr")
		}
		if !config.TTY {
			return nil, fmt.Errorf("interactions require tty")
		}
	} else if hasInteract {
		return nil, fmt.Errorf("interact requires interactions")
	}
	if config.Interact && !config.TTY {
		return nil, fmt.Errorf("interact requires tty")
	}
	handoff := config.TTY && (!hasInteractions || config.Interact)
	if err := validateTerminalConfig(config.Terminal, config.TTY, handoff); err != nil {
		return nil, err
	}
	if _, configured := raw["allowed_exit_codes"]; !configured {
		config.AllowedExitCodes = []int{0}
	} else if len(config.AllowedExitCodes) == 0 {
		return nil, fmt.Errorf("allowed_exit_codes must contain at least one exit code")
	}
	for _, code := range config.AllowedExitCodes {
		if code < 0 || code > 255 {
			return nil, fmt.Errorf("allowed_exit_codes must contain only exit codes from 0 through 255")
		}
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
		argvProgram, err = wukoexpr.Compile(config.Argv.Expr, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling argv expr: %w", err)
		}
	}
	var interactions *ptyinteract.Plan
	var interactionExpr *vm.Program
	if config.Interactions.Expr != "" {
		interactionExpr, err = wukoexpr.Compile(config.Interactions.Expr, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling interactions expr: %w", err)
		}
	} else if hasInteractions {
		interactions, err = compileInteractions(config.Interactions.Static)
		if err != nil {
			return nil, err
		}
	}
	return &Runner{
		config: config, stdoutPolicy: stdoutPolicy, stderrPolicy: stderrPolicy, captureLimit: captureLimit,
		argvProgram: argvProgram, interactionExpr: interactionExpr, interactions: interactions, hasInteractions: hasInteractions,
	}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
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
	environment := maps.Clone(request.Env)
	maps.Copy(environment, r.config.Env)
	environment = step.ApplyAttemptEnvironment(environment, request)
	executor := request.Executor
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
	result, err := executor.Run(ctx, process.Options{
		Command: command, Args: args, Dir: dir, Env: environment, User: r.config.User,
		Stdin: stdin, Stdout: request.Stdout, Stderr: request.Stderr, TTY: r.config.TTY,
		Interactions: interactions, Interact: r.config.Interact, CaptureLimit: captureLimit,
		Terminal:     terminalAppearance(r.config.Terminal),
		StdoutPolicy: r.stdoutPolicy, StderrPolicy: r.stderrPolicy,
	})
	outputs := map[string]any{
		"stdout": result.Stdout, "stderr": result.Stderr, "exit_code": result.ExitCode,
		"stdout_truncated": result.StdoutTruncated, "stderr_truncated": result.StderrTruncated,
	}
	if err != nil {
		exitErr, isExitError := soleProcessExitError(err)
		if isExitError && exitErr.Code == result.ExitCode && slices.Contains(r.config.AllowedExitCodes, result.ExitCode) {
			return step.Result{Outputs: outputs}, nil
		}
		return step.Result{Outputs: outputs}, err
	}
	if !slices.Contains(r.config.AllowedExitCodes, result.ExitCode) {
		return step.Result{Outputs: outputs}, &process.ExitError{Command: command, Code: result.ExitCode}
	}
	return step.Result{Outputs: outputs}, nil
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
		return argv[0], argv[1:], nil
	}
	if r.config.Command != "" {
		return r.config.Command, r.config.Args, nil
	}
	shell := r.config.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"-c", r.config.Script, "wuko"}
	args = append(args, r.config.Args...)
	return shell, args, nil
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

func expressionEnvironmentFor(request step.Request) expressionEnvironment {
	return expressionEnvironment{
		Inputs:       request.Inputs,
		Vars:         request.Vars,
		Env:          request.Env,
		Steps:        request.Steps,
		Dependencies: request.Dependencies,
		Batch:        bindingRoot(request.Bindings, "batch"),
		Foreach:      bindingRoot(request.Bindings, "foreach"),
		Matrix:       bindingRoot(request.Bindings, "matrix"),
		Observe:      bindingRoot(request.Bindings, "observe"),
		Finally:      bindingRoot(request.Bindings, "finally"),
		Error:        bindingRoot(request.Bindings, "error"),
		Workflow:     request.WorkflowValue(),
		Run:          request.RunValue(),
		Secret:       request.ResolveSecret,
	}
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

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}
