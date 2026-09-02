// Package process implements lifecycle-managed workflow services.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	processpkg "github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	Command          string               `yaml:"command,omitempty"`
	Script           string               `yaml:"script,omitempty"`
	Shell            string               `yaml:"shell,omitempty"`
	Args             []string             `yaml:"args,omitempty"`
	Argv             *ArgvExpression      `yaml:"argv,omitempty"`
	WorkingDirectory string               `yaml:"working_directory,omitempty"`
	Env              workflow.Environment `yaml:"env,omitempty"`
	User             string               `yaml:"user,omitempty"`
	Label            string               `yaml:"label,omitempty"`
	RPC              string               `yaml:"rpc,omitempty"`
	Stdout           string               `yaml:"stdout,omitempty"`
	Stderr           string               `yaml:"stderr,omitempty"`
	Readiness        *ReadinessConfig     `yaml:"readiness,omitempty"`
	Liveness         *LivenessConfig      `yaml:"liveness,omitempty"`
	Restart          RestartConfig        `yaml:"restart,omitempty"`
	AllowedExitCodes []int                `yaml:"allowed_exit_codes,omitempty"`
	ExitOnEnd        bool                 `yaml:"exit_on_end,omitempty"`
	ExitOnFailure    bool                 `yaml:"exit_on_failure,omitempty"`
	KeepAlive        bool                 `yaml:"keep_alive,omitempty"`
	Detached         bool                 `yaml:"detached,omitempty"`
	Shutdown         ShutdownConfig       `yaml:"shutdown,omitempty"`
}

type ReadinessConfig struct {
	Log  *LogProbe  `yaml:"log,omitempty"`
	Exec *ExecProbe `yaml:"exec,omitempty"`
	HTTP *HTTPProbe `yaml:"http,omitempty"`
}

type LivenessConfig struct {
	Exec *ExecProbe `yaml:"exec,omitempty"`
	HTTP *HTTPProbe `yaml:"http,omitempty"`
}

type LogProbe struct {
	Pattern string             `yaml:"pattern"`
	Timeout *workflow.Duration `yaml:"timeout,omitempty"`
}

type ProbeTiming struct {
	InitialDelay     *workflow.Duration `yaml:"initial_delay,omitempty"`
	Period           *workflow.Duration `yaml:"period,omitempty"`
	Timeout          *workflow.Duration `yaml:"timeout,omitempty"`
	SuccessThreshold int                `yaml:"success_threshold,omitempty"`
	FailureThreshold int                `yaml:"failure_threshold,omitempty"`
}

type ExecProbe struct {
	Command     string   `yaml:"command"`
	Args        []string `yaml:"args,omitempty"`
	ProbeTiming `yaml:",inline"`
}

type HTTPProbe struct {
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	ExpectedStatus []int             `yaml:"expected_status,omitempty"`
	ProbeTiming    `yaml:",inline"`
}

type RestartConfig struct {
	Policy      string             `yaml:"policy,omitempty"`
	Backoff     *workflow.Duration `yaml:"backoff,omitempty"`
	MaxRestarts int                `yaml:"max_restarts,omitempty"`
}

type ShutdownConfig struct {
	Signal     string             `yaml:"signal,omitempty"`
	ParentOnly bool               `yaml:"parent_only,omitempty"`
	Timeout    *workflow.Duration `yaml:"timeout,omitempty"`
	Command    *CommandConfig     `yaml:"command,omitempty"`
}

type CommandConfig struct {
	Command string   `yaml:"command,omitempty"`
	Script  string   `yaml:"script,omitempty"`
	Shell   string   `yaml:"shell,omitempty"`
	Args    []string `yaml:"args,omitempty"`
}

type ArgvExpression struct {
	Expr string `yaml:"expr"`
}

type expressionEnvironment struct {
	Inputs       map[string]any            `expr:"inputs"`
	Vars         map[string]any            `expr:"vars"`
	Env          map[string]string         `expr:"env"`
	Steps        map[string]any            `expr:"steps"`
	Dependencies map[string]map[string]any `expr:"dependencies"`
	Batch        map[string]any            `expr:"batch"`
	Foreach      map[string]any            `expr:"foreach"`
	Matrix       map[string]any            `expr:"matrix"`
	Workflow     step.WorkflowValue        `expr:"workflow"`
	Run          struct {
		Dir string `expr:"dir"`
	} `expr:"run"`
	Secret func(string) (string, error) `expr:"secret"`
}

type Runner struct {
	config       Config
	stdoutPolicy processpkg.OutputPolicy
	stderrPolicy processpkg.OutputPolicy
	logPattern   *regexp.Regexp
	signal       syscall.Signal
	argvProgram  *vm.Program
	rpcRegistry  *rpcRegistry
	workerID     string
}

func (*Runner) ExecutorAware() {}

func Register(registry *step.Registry) error {
	rpc := newRPCRegistry()
	if err := registry.Register("process", func(raw map[string]any) (step.Runner, error) { return newProcess(raw, rpc) }); err != nil {
		return err
	}
	return registry.Register("process_call", func(raw map[string]any) (step.Runner, error) { return newCall(raw, rpc) })
}

func New(raw map[string]any) (step.Runner, error) {
	return newProcess(raw, nil)
}

func newProcess(raw map[string]any, rpcRegistry *rpcRegistry) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasArgv := raw["argv"]
	if hasArgv {
		if config.Argv == nil || strings.TrimSpace(config.Argv.Expr) == "" {
			return nil, fmt.Errorf("argv must contain a non-empty expr")
		}
		for _, field := range []string{"command", "script", "shell", "args"} {
			if _, exists := raw[field]; exists {
				return nil, fmt.Errorf("argv cannot be combined with %s", field)
			}
		}
	} else if err := validateCommand(config.Command, config.Script, config.Shell, "process"); err != nil {
		return nil, err
	}
	for name := range config.Env {
		if !workflow.ValidEnvironmentName(name) {
			return nil, fmt.Errorf("invalid environment name %q", name)
		}
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
	stdoutPolicy, err := streamPolicy("stdout", config.Stdout)
	if err != nil {
		return nil, err
	}
	stderrPolicy, err := streamPolicy("stderr", config.Stderr)
	if err != nil {
		return nil, err
	}
	if config.Restart.Policy == "" {
		config.Restart.Policy = "never"
	}
	if !slices.Contains([]string{"never", "on_failure", "always"}, config.Restart.Policy) {
		return nil, fmt.Errorf("restart policy must be never, on_failure, or always")
	}
	if config.Restart.MaxRestarts < 0 {
		return nil, fmt.Errorf("restart max_restarts cannot be negative")
	}
	if config.Detached && config.Shutdown.Command == nil {
		return nil, fmt.Errorf("detached requires shutdown.command")
	}
	if config.RPC != "" && config.RPC != "jsonl" {
		return nil, fmt.Errorf("rpc must be jsonl")
	}
	if config.RPC != "" {
		if config.Detached {
			return nil, fmt.Errorf("rpc cannot be combined with detached")
		}
		if config.Stdout != "" && config.Stdout != "inherit" {
			return nil, fmt.Errorf("rpc reserves stdout for protocol messages")
		}
	}
	if config.Shutdown.Command != nil {
		if err := validateCommand(config.Shutdown.Command.Command, config.Shutdown.Command.Script, config.Shutdown.Command.Shell, "shutdown command"); err != nil {
			return nil, err
		}
	}
	if config.Shutdown.Signal == "" {
		config.Shutdown.Signal = "SIGTERM"
	}
	signal, err := parseSignal(config.Shutdown.Signal)
	if err != nil {
		return nil, err
	}
	var pattern *regexp.Regexp
	if config.Readiness != nil {
		count := boolInt(config.Readiness.Log != nil) + boolInt(config.Readiness.Exec != nil) + boolInt(config.Readiness.HTTP != nil)
		if count != 1 {
			return nil, fmt.Errorf("readiness requires exactly one of log, exec, or http")
		}
		if config.Readiness.Log != nil {
			pattern, err = regexp.Compile(config.Readiness.Log.Pattern)
			if err != nil {
				return nil, fmt.Errorf("readiness log pattern: %w", err)
			}
			// A discarded stream never reaches the log matcher, so a readiness log that can
			// only ever time out is rejected here rather than after its timeout expires.
			if config.RPC != "" && !stderrPolicy.Streams() {
				return nil, fmt.Errorf("readiness log for rpc requires stderr to be inherit")
			}
			if config.RPC == "" && !stdoutPolicy.Streams() && !stderrPolicy.Streams() {
				return nil, fmt.Errorf("readiness log requires stdout or stderr to be inherit")
			}
		}
	}
	if config.Liveness != nil && boolInt(config.Liveness.Exec != nil)+boolInt(config.Liveness.HTTP != nil) != 1 {
		return nil, fmt.Errorf("liveness requires exactly one of exec or http")
	}
	if err := validateProbes(config); err != nil {
		return nil, err
	}
	var argvProgram *vm.Program
	if config.Argv != nil {
		argvProgram, err = wukoexpr.Compile(config.Argv.Expr, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling argv expr: %w", err)
		}
	}
	var workerID string
	if config.RPC != "" {
		workerID, err = opaqueID("worker")
		if err != nil {
			return nil, err
		}
	}
	return &Runner{config: config, stdoutPolicy: stdoutPolicy, stderrPolicy: stderrPolicy, logPattern: pattern, signal: signal, argvProgram: argvProgram,
		rpcRegistry: rpcRegistry, workerID: workerID}, nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if request.Services == nil {
		return step.Result{}, fmt.Errorf("managed service scope is unavailable")
	}
	if err := runner.checkRestartSupport(request); err != nil {
		return step.Result{}, err
	}
	label := runner.config.Label
	if label == "" {
		label = request.StepID
	}
	ready := make(chan error, 1)
	committed := make(chan struct{})
	// abandoned releases a service that became ready after Run had already given up, so a
	// lost race between readiness and cancellation cannot park the job for the whole run.
	abandoned := make(chan struct{})
	var abandonOnce sync.Once
	abandon := func() { abandonOnce.Do(func() { close(abandoned) }) }
	if runner.config.RPC != "" {
		if runner.rpcRegistry == nil {
			return step.Result{}, fmt.Errorf("process RPC registry is unavailable")
		}
		if err := runner.rpcRegistry.declare(runner.workerID); err != nil {
			return step.Result{}, err
		}
	}
	run := func(serviceCtx context.Context) error {
		err := runner.runLifecycle(serviceCtx, ctx, request, label, ready, committed, abandoned)
		if runner.config.RPC != "" {
			runner.rpcRegistry.forget(runner.workerID, err)
		}
		select {
		case <-committed:
			return err
		default:
		}
		if err != nil {
			select {
			case ready <- err:
			default:
			}
			return fmt.Errorf("%w: %w", step.ErrServiceAborted, err)
		}
		return step.ErrServiceAborted
	}
	err := request.Services.StartService(request.StepID, "process", step.ServiceOptions{
		KeepAlive: runner.config.KeepAlive, FailFast: runner.config.ExitOnFailure, ExitOnEnd: runner.config.ExitOnEnd,
	}, run)
	if err != nil {
		if runner.config.RPC != "" {
			runner.rpcRegistry.forget(runner.workerID, err)
		}
		return step.Result{}, err
	}
	select {
	case err := <-ready:
		if err != nil {
			abandon()
			return step.Result{}, err
		}
		close(committed)
	case <-ctx.Done():
		abandon()
		return step.Result{}, ctx.Err()
	}
	mode := "spawn"
	if runner.config.Readiness != nil {
		switch {
		case runner.config.Readiness.Log != nil:
			mode = "log"
		case runner.config.Readiness.Exec != nil:
			mode = "exec"
		default:
			mode = "http"
		}
	}
	outputs := map[string]any{"ready": true, "label": label, "detached": runner.config.Detached, "readiness": mode}
	if runner.config.RPC != "" {
		outputs["worker_id"] = runner.workerID
	}
	return step.Result{Outputs: outputs}, nil
}

// checkRestartSupport rejects a restart policy the executor cannot honor. Restarting replaces
// an instance that must first be stopped; when canceling the run does not stop it, only a
// shutdown command can, and without one every restart would stack another live copy.
func (runner *Runner) checkRestartSupport(request step.Request) error {
	if runner.config.Restart.Policy == "never" || runner.config.Shutdown.Command != nil {
		return nil
	}
	policy, declared := request.Executor.(processpkg.CancelPolicy)
	if !declared || policy.CancelStopsProcess() {
		return nil
	}
	return fmt.Errorf("restart policy %q requires shutdown.command: this executor cannot stop a running process by cancellation", runner.config.Restart.Policy)
}

func (runner *Runner) processOptions(request step.Request, label string, started func(), matcher *logMatcher, rpc *rpcSession) (processpkg.Options, func() error, error) {
	command, args := buildCommand(runner.config.Command, runner.config.Script, runner.config.Shell, runner.config.Args)
	if runner.argvProgram != nil {
		value, err := expr.Run(runner.argvProgram, expressionEnvironmentFor(request))
		if err != nil {
			return processpkg.Options{}, nil, fmt.Errorf("evaluating argv expr: %w", err)
		}
		argv, err := argvStrings(value)
		if err != nil {
			return processpkg.Options{}, nil, err
		}
		command, args = argv[0], argv[1:]
	}
	dir, environment := runner.executionContext(request)
	stdout := prefixedWriter(request.Stdout, label, matcher).(*linePrefixWriter)
	stderr := prefixedWriter(request.Stderr, label, matcher).(*linePrefixWriter)
	var input io.Reader
	var output io.Writer = stdout
	flush := func() error { return errors.Join(stdout.Flush(), stderr.Flush()) }
	// An RPC session owns its request pipe for the whole lifecycle, so it is still open when
	// the worker exits and the executor must report that exit without draining stdin first.
	streamingInput := false
	if rpc != nil {
		input = rpc.reader
		output = rpc
		flush = stderr.Flush
		streamingInput = true
	}
	return processpkg.Options{Command: command, Args: args, Dir: dir, Env: environment, User: runner.config.User,
		Stdin: input, StdinOutlivesProcess: streamingInput,
		Stdout: output, Stderr: stderr, StdoutPolicy: runner.stdoutPolicy, StderrPolicy: runner.stderrPolicy,
		Started: started, TerminationSignal: runner.signal, TerminationParentOnly: runner.config.Shutdown.ParentOnly,
		TerminationGracePeriod: duration(runner.config.Shutdown.Timeout, 10*time.Second)}, flush, nil
}

func (runner *Runner) executionContext(request step.Request) (string, map[string]string) {
	dir := runner.config.WorkingDirectory
	if dir == "" {
		dir = request.RunDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(request.RunDir, dir)
	}
	environment := maps.Clone(request.Env)
	maps.Copy(environment, runner.config.Env)
	return dir, step.ApplyAttemptEnvironment(environment, request)
}

func buildCommand(command, script, shell string, args []string) (string, []string) {
	if command != "" {
		return command, append([]string(nil), args...)
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return shell, append([]string{"-c", script, "wuko-process"}, args...)
}

func validateCommand(command, script, shell, name string) error {
	if (strings.TrimSpace(command) == "") == (strings.TrimSpace(script) == "") {
		return fmt.Errorf("%s requires exactly one of command or script", name)
	}
	if command != "" && shell != "" {
		return fmt.Errorf("%s shell requires script", name)
	}
	return nil
}

func streamPolicy(field, value string) (processpkg.OutputPolicy, error) {
	if value == "" {
		value = "inherit"
	}
	policy, err := processpkg.ParseOutputPolicy(value)
	if err != nil {
		return 0, fmt.Errorf("%s %w", field, err)
	}
	if policy.Captures() {
		return 0, fmt.Errorf("%s for a process must be inherit or discard", field)
	}
	return policy, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func expressionEnvironmentFor(request step.Request) expressionEnvironment {
	value := expressionEnvironment{Inputs: request.Inputs, Vars: request.Vars, Env: request.Env, Steps: request.Steps, Dependencies: request.Dependencies, Secret: request.ResolveSecret,
		Batch: binding(request.Bindings, "batch"), Foreach: binding(request.Bindings, "foreach"), Matrix: binding(request.Bindings, "matrix"), Workflow: request.WorkflowValue()}
	value.Run.Dir = request.RunDir
	return value
}

func binding(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func argvStrings(value any) ([]string, error) {
	list := reflect.ValueOf(value)
	if !list.IsValid() || (list.Kind() != reflect.Array && list.Kind() != reflect.Slice) || list.Len() == 0 {
		return nil, fmt.Errorf("argv expr must return a non-empty list")
	}
	result := make([]string, list.Len())
	for index := range list.Len() {
		text, err := argvScalar(list.Index(index))
		if err != nil {
			return nil, fmt.Errorf("argv expr item %d: %w", index, err)
		}
		result[index] = text
	}
	if result[0] == "" {
		return nil, fmt.Errorf("argv expr returned an empty executable")
	}
	return result, nil
}

func argvScalar(value reflect.Value) (string, error) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return "", fmt.Errorf("null is not a scalar")
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return "", fmt.Errorf("null is not a scalar")
	}
	switch value.Kind() {
	case reflect.String:
		return value.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", fmt.Errorf("non-finite number is not a scalar")
		}
		return strconv.FormatFloat(number, 'g', -1, value.Type().Bits()), nil
	default:
		return "", fmt.Errorf("%s is not a scalar", value.Kind())
	}
}
