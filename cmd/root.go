package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	workflowschedule "github.com/up2jj/wuko/schedule"
	"github.com/up2jj/wuko/step"
	agentstep "github.com/up2jj/wuko/steps/agent"
	assertstep "github.com/up2jj/wuko/steps/assert"
	cachestep "github.com/up2jj/wuko/steps/cache"
	changedstep "github.com/up2jj/wuko/steps/changed"
	"github.com/up2jj/wuko/steps/choice"
	"github.com/up2jj/wuko/steps/confirm"
	dockerstep "github.com/up2jj/wuko/steps/docker"
	filestep "github.com/up2jj/wuko/steps/file"
	globstep "github.com/up2jj/wuko/steps/glob"
	httpstep "github.com/up2jj/wuko/steps/http"
	importvarsstep "github.com/up2jj/wuko/steps/import_vars"
	inputstep "github.com/up2jj/wuko/steps/input"
	jsonpathstep "github.com/up2jj/wuko/steps/jsonpath"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	luastep "github.com/up2jj/wuko/steps/lua"
	passwordstep "github.com/up2jj/wuko/steps/password"
	semverstep "github.com/up2jj/wuko/steps/semver"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/steps/shell"
	tempstep "github.com/up2jj/wuko/steps/temp"
	"github.com/up2jj/wuko/tui"
	variablefile "github.com/up2jj/wuko/variables"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const shutdownGracePeriod = 10 * time.Second

// ErrForcedShutdown means graceful cancellation exceeded its budget or a second signal requested
// immediate termination.
var ErrForcedShutdown = errors.New("forced shutdown")

const noWorkflowsHelp = `No workflows found.

Create .wuko/workflows/hello.yaml:

  version: 1
  name: hello
  steps:
    - id: greet
      type: shell
      with:
        script: echo "Hello from Wuko"

Run it:
  wuko run hello

Run a file directly:
  wuko run --file ./workflow.yaml

Run a trusted remote workflow:
  wuko run https://example.com/workflow.yaml
  wuko run github:owner/repo@main:path/to/workflow.yaml

More help:
  wuko --help
`

type dependencies struct {
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	cwd           func() (string, error)
	environment   func(context.Context, string) (map[string]string, error)
	homeDir       func() (string, error)
	configDir     func() (string, error)
	agentLookPath func(string) (string, error)
	registry      *step.Registry
	loader        *workflow.Loader
	isInteractive func(io.Reader) bool
	now           func() time.Time
	waitUntil     func(context.Context, time.Time) error
	debug         *bool
}

func Execute() error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return executeWithSignals(signals, shutdownGracePeriod, func(ctx context.Context) error {
		return NewRootCmd().ExecuteContext(ctx)
	})
}

func executeWithSignals(signals <-chan os.Signal, gracePeriod time.Duration, execute func(context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- execute(ctx) }()

	select {
	case err := <-done:
		return err
	case <-signals:
		cancel()
	}

	timer := time.NewTimer(gracePeriod)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case received := <-signals:
		return fmt.Errorf("%w after receiving %s during graceful shutdown", ErrForcedShutdown, received)
	case <-timer.C:
		return fmt.Errorf("%w after graceful shutdown exceeded %s", ErrForcedShutdown, gracePeriod)
	}
}

func NewRootCmd() *cobra.Command {
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		inputstep.Register, passwordstep.Register, choice.Register,
		confirm.Register, assertstep.Register, setstep.Register, importvarsstep.Register, jsonpathstep.Register, semverstep.Register, httpstep.Register, filestep.Register, tempstep.Register, globstep.Register, cachestep.Register, changedstep.Register,
		dockerstep.Register, keyvaluestep.Register, luastep.Register, shell.Register, agentstep.Register,
	} {
		if err := register(registry); err != nil {
			panic(err)
		}
	}
	return newRootCmd(dependencies{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		cwd: os.Getwd, environment: direnvEnvironment,
		homeDir: os.UserHomeDir, configDir: os.UserConfigDir, registry: registry,
		agentLookPath: exec.LookPath,
		loader:        workflow.NewLoader(nil), isInteractive: interactive,
		now: time.Now, waitUntil: workflowschedule.Wait,
	})
}

func newRootCmd(deps dependencies) *cobra.Command {
	if deps.isInteractive == nil {
		deps.isInteractive = interactive
	}
	if deps.agentLookPath == nil {
		deps.agentLookPath = exec.LookPath
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.waitUntil == nil {
		deps.waitUntil = workflowschedule.Wait
	}
	debug := false
	deps.debug = &debug
	root := &cobra.Command{
		Use:           "wuko",
		Short:         "Run trusted YAML workflows",
		Version:       fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			return runWorkflowPicker(command, deps)
		},
	}
	root.SetIn(deps.stdin)
	root.SetOut(deps.stdout)
	root.SetErr(deps.stderr)
	root.PersistentFlags().BoolVar(&debug, "debug", false, "trace workflow loading, validation, and execution to stderr (may expose non-secret configuration)")
	root.AddCommand(newRunCmd(deps), newListCmd(deps), newTreeCmd(deps), newValidateCmd(deps), newAgentCmd(deps), newCompletionCmd())
	return root
}

func runWorkflowPicker(command *cobra.Command, deps dependencies) error {
	cwd, home, config, err := directories(deps)
	if err != nil {
		return err
	}
	reporter := diagnosticsFor(command, deps, cwd)
	diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "workflow picker", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd)}})
	discoveryStarted := time.Now()
	diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: discoveryStarted, Message: "discovering workflows"})
	sources, err := workflow.DiscoverAll(cwd, home, config)
	if err != nil {
		diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
		return err
	}
	diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Attributes: []diagnostic.Attribute{diagnostic.Attr("workflows", fmt.Sprint(len(sources)))}})
	if !deps.isInteractive(command.InOrStdin()) {
		for _, source := range sources {
			fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\t%s\n", source.Name, source.Scope, source.Description, source.Path)
		}
		return nil
	}
	if len(sources) == 0 {
		fmt.Fprint(command.OutOrStdout(), noWorkflowsHelp)
		return nil
	}

	options := make([]tui.Option, len(sources))
	for i, source := range sources {
		description := source.Description
		if description == "" {
			description = "(no description)"
		}
		options[i] = tui.Option{
			Label:       source.Name,
			Description: fmt.Sprintf("%s • %s • %s", source.Scope, description, source.Path),
			Value:       source,
		}
	}
	selected, err := tui.Select(command.Context(), command.InOrStdin(), command.OutOrStdout(), "Workflows", options)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	source, ok := selected.Value.(workflow.Source)
	if !ok {
		return fmt.Errorf("workflow selection did not contain a workflow")
	}
	diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseSelection, Status: diagnostic.StatusSucceeded, Location: diagnostic.Location{Source: source.Path}, Message: source.Name})
	if source.Effective {
		fmt.Fprintf(command.OutOrStdout(), "wuko run %s\n", shellQuote(source.Name))
		return nil
	}
	fmt.Fprintf(command.OutOrStdout(), "wuko run --file %s\n", shellQuote(source.Path))
	return nil
}

func diagnosticsFor(command *cobra.Command, deps dependencies, runDir string) diagnostic.Reporter {
	if deps.debug == nil || !*deps.debug {
		return nil
	}
	return tui.NewDebug(command.ErrOrStderr(), runDir).Report
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r == '/' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func directories(deps dependencies) (string, string, string, error) {
	cwd, err := deps.cwd()
	if err != nil {
		return "", "", "", fmt.Errorf("finding current directory: %w", err)
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", "", "", fmt.Errorf("finding home directory: %w", err)
	}
	config, err := deps.configDir()
	if err != nil {
		return "", "", "", fmt.Errorf("finding user config directory: %w", err)
	}
	return cwd, home, config, nil
}

func interactive(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func colorEnabled(writer io.Writer) bool {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return false
	}
	file, ok := writer.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func parseVars(ctx context.Context, baseDir string, files, values []string) (map[string]any, error) {
	result, err := variablefile.LoadFiles(ctx, baseDir, files)
	if err != nil {
		return nil, err
	}
	overrides, err := parseVarOverrides(values)
	if err != nil {
		return nil, err
	}
	maps.Copy(result, overrides)
	return result, nil
}

func parseVarOverrides(values []string) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for _, entry := range values {
		key, raw, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid variable %q; expected key=value", entry)
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil || hasMoreJSON(decoder) {
			value = raw
		}
		result[key] = value
	}
	return result, nil
}

func hasMoreJSON(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) != io.EOF
}

func parseEnv(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !workflow.ValidEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment override %q; expected KEY=value", entry)
		}
		result[key] = value
	}
	return result, nil
}
