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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/executor"
	workflowschedule "github.com/up2jj/wuko/schedule"
	"github.com/up2jj/wuko/step"
	agentstep "github.com/up2jj/wuko/steps/agent"
	assertstep "github.com/up2jj/wuko/steps/assert"
	cachestep "github.com/up2jj/wuko/steps/cache"
	changedstep "github.com/up2jj/wuko/steps/changed"
	"github.com/up2jj/wuko/steps/choice"
	"github.com/up2jj/wuko/steps/confirm"
	devenvstep "github.com/up2jj/wuko/steps/devenv"
	dockerstep "github.com/up2jj/wuko/steps/docker"
	extractstep "github.com/up2jj/wuko/steps/extract"
	filestep "github.com/up2jj/wuko/steps/file"
	gitstep "github.com/up2jj/wuko/steps/git"
	githubactionsstep "github.com/up2jj/wuko/steps/github_actions"
	githubprstep "github.com/up2jj/wuko/steps/github_pr"
	globstep "github.com/up2jj/wuko/steps/glob"
	httpstep "github.com/up2jj/wuko/steps/http"
	importvarsstep "github.com/up2jj/wuko/steps/import_vars"
	inputstep "github.com/up2jj/wuko/steps/input"
	jsonpathstep "github.com/up2jj/wuko/steps/jsonpath"
	keyvaluestep "github.com/up2jj/wuko/steps/key_value"
	logwaitstep "github.com/up2jj/wuko/steps/log_wait"
	luastep "github.com/up2jj/wuko/steps/lua"
	passwordstep "github.com/up2jj/wuko/steps/password"
	pathstep "github.com/up2jj/wuko/steps/path"
	requiretoolstep "github.com/up2jj/wuko/steps/require_tool"
	"github.com/up2jj/wuko/steps/review"
	semverstep "github.com/up2jj/wuko/steps/semver"
	setstep "github.com/up2jj/wuko/steps/set"
	"github.com/up2jj/wuko/steps/shell"
	tempstep "github.com/up2jj/wuko/steps/temp"
	watchstep "github.com/up2jj/wuko/steps/watch"
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

const noInvokableWorkflowsHelp = `No directly invokable workflows found.

Use wuko list to inspect dependency-only workflows.
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
	executors     *executor.Registry
	loader        *workflow.Loader
	isInteractive func(io.Reader) bool
	now           func() time.Time
	waitUntil     func(context.Context, time.Time) error
	getenv        func(string) string
	openURL       func(string) error
	openEditor    func(context.Context, io.Reader, io.Writer, io.Writer, string) error
	confirm       func(context.Context, io.Reader, io.Writer, string, bool) (bool, error)
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
	executors := executor.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		inputstep.Register, passwordstep.Register, choice.Register, pathstep.Register, review.Register,
		confirm.Register, assertstep.Register, setstep.Register, importvarsstep.Register, jsonpathstep.Register, extractstep.Register, semverstep.Register, httpstep.Register, filestep.Register, tempstep.Register, globstep.Register, watchstep.Register, cachestep.Register, changedstep.Register, requiretoolstep.Register,
		dockerstep.Register, gitstep.Register, githubprstep.Register, githubactionsstep.Register, keyvaluestep.Register, luastep.Register, logwaitstep.Register, shell.Register, agentstep.Register, devenvstep.RegisterTask,
	} {
		if err := register(registry); err != nil {
			panic(err)
		}
	}
	if err := dockerstep.RegisterExecutor(executors); err != nil {
		panic(err)
	}
	if err := devenvstep.RegisterExecutor(executors); err != nil {
		panic(err)
	}
	return newRootCmd(dependencies{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		cwd: os.Getwd, environment: direnvEnvironment,
		homeDir: os.UserHomeDir, configDir: os.UserConfigDir, registry: registry, executors: executors,
		agentLookPath: exec.LookPath,
		loader:        workflow.NewLoader(nil), isInteractive: interactive,
		now: time.Now, waitUntil: workflowschedule.Wait,
		getenv:     os.Getenv,
		openEditor: openWorkflowEditor(os.Getenv),
		confirm:    tui.Confirm,
	})
}

func workflowEngine(deps dependencies) *engine.Engine {
	return engine.New(deps.registry, engine.WithExecutors(deps.executors))
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
	if deps.getenv == nil {
		deps.getenv = os.Getenv
	}
	if deps.openEditor == nil {
		deps.openEditor = openWorkflowEditor(deps.getenv)
	}
	if deps.confirm == nil {
		deps.confirm = tui.Confirm
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
	root.AddCommand(newRunCmd(deps), newUICmd(deps), newListCmd(deps), newTreeCmd(deps), newValidateCmd(deps), newAgentCmd(deps), newInstallCmd(deps), newUninstallCmd(deps), newCompletionCmd())
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
	discovered := len(sources)
	diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Attributes: []diagnostic.Attribute{diagnostic.Attr("workflows", fmt.Sprint(discovered))}})
	if !deps.isInteractive(command.InOrStdin()) {
		sources = slices.DeleteFunc(sources, func(source workflow.Source) bool { return !source.Invokable })
		for _, source := range sources {
			if err := writeWorkflowSource(command.OutOrStdout(), source); err != nil {
				return err
			}
		}
		return nil
	}
	sources = slices.DeleteFunc(sources, func(source workflow.Source) bool { return !source.Invokable })
	pickerState, stateErr := loadWorkflowPickerState(command.Context(), config)
	if stateErr != nil {
		fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot load workflow picker state: %v\n", stateErr)
	}
	if stateErr == nil && pickerState.reconcile(sources) {
		if err := saveWorkflowPickerState(command.Context(), config, pickerState); err != nil {
			fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
		}
	}
	if len(sources) == 0 {
		if discovered > 0 {
			fmt.Fprint(command.OutOrStdout(), noInvokableWorkflowsHelp)
			return nil
		}
		fmt.Fprint(command.OutOrStdout(), noWorkflowsHelp)
		return nil
	}

	sortMode := pickerState.sortMode()
	selectedPath := ""
	for {
		sortedSources := sortWorkflowSources(sources, pickerState, sortMode)
		options := workflowPickerOptions(sortedSources, pickerState, selectedPath)
		title := fmt.Sprintf("Workflows (sort: %s)", sortMode)
		selection, err := tui.SelectWithIntent(command.Context(), command.InOrStdin(), command.OutOrStdout(), title, options)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		source, ok := selection.Option.Value.(workflow.Source)
		if !ok {
			return fmt.Errorf("workflow selection did not contain a workflow")
		}
		selectedPath = workflowPickerSelectionKey(source)
		diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseSelection, Status: diagnostic.StatusSucceeded, Location: diagnostic.Location{Source: source.Path}, Message: workflowDisplayName(source)})
		switch selection.Intent {
		case tui.SelectionPrimary:
			runErr := runWorkflow(command, deps, nil, runWorkflowConfig{workflowFile: source.Path, targetName: source.Target})
			if runErr == nil {
				pickerState.markRecent(source.Path)
				if err := saveWorkflowPickerState(command.Context(), config, pickerState); err != nil {
					fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
				}
			}
			return runErr
		case tui.SelectionUI:
			if !source.HasForm {
				fmt.Fprintf(command.OutOrStdout(), "Workflow %s does not declare a form.\n", source.Name)
				continue
			}
			return runWorkflowUI(command, deps, nil, uiWorkflowConfig{workflowFile: source.Path, targetName: source.Target})
		case tui.SelectionEditor:
			if err := deps.openEditor(command.Context(), command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr(), source.Path); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Warning: %v\n", err)
			}
		case tui.SelectionTogglePin:
			pickerState.togglePinned(source.Path)
			if err := saveWorkflowPickerState(command.Context(), config, pickerState); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
			}
		case tui.SelectionToggleSort:
			if sortMode == workflowPickerSortName {
				sortMode = workflowPickerSortRecent
			} else {
				sortMode = workflowPickerSortName
			}
			pickerState.Sort = sortMode.String()
			if err := saveWorkflowPickerState(command.Context(), config, pickerState); err != nil {
				fmt.Fprintf(command.ErrOrStderr(), "Warning: cannot save workflow picker state: %v\n", err)
			}
		default:
			target := ""
			if source.Target != "" {
				target = " " + shellQuote(source.Target)
			}
			if source.Effective {
				fmt.Fprintf(command.OutOrStdout(), "wuko run %s%s\n", shellQuote(source.Name), target)
				return nil
			}
			fmt.Fprintf(command.OutOrStdout(), "wuko run --file %s%s\n", shellQuote(source.Path), target)
			return nil
		}
	}
}

func workflowPickerOption(source workflow.Source) tui.Option {
	return workflowPickerOptionWithState(source, false, false)
}

func writeWorkflowSource(writer io.Writer, source workflow.Source) error {
	if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s", source.Name, source.Scope, source.Description, source.Path); err != nil {
		return err
	}
	if dependencies := workflowDependencySummary(source.DependsOn); dependencies != "" {
		if _, err := fmt.Fprintf(writer, "\t%s", dependencies); err != nil {
			return err
		}
	}
	if !source.Invokable {
		if _, err := fmt.Fprint(writer, "\tnot directly invokable"); err != nil {
			return err
		}
	}
	if source.Target != "" {
		if _, err := fmt.Fprintf(writer, "\ttarget %s", source.Target); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(writer)
	return err
}

func workflowDependencySummary(dependencies map[string]string) string {
	if len(dependencies) == 0 {
		return ""
	}
	items := make([]string, 0, len(dependencies))
	for _, alias := range slices.Sorted(maps.Keys(dependencies)) {
		name := dependencies[alias]
		if alias == name {
			items = append(items, name)
			continue
		}
		items = append(items, alias+"="+name)
	}
	return "depends on " + strings.Join(items, ", ")
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
