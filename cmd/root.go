package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/agent"
	"github.com/up2jj/wuko/steps/choice"
	dockerstep "github.com/up2jj/wuko/steps/docker"
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/steps/prompt"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type dependencies struct {
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	cwd           func() (string, error)
	homeDir       func() (string, error)
	configDir     func() (string, error)
	registry      *step.Registry
	loader        *workflow.Loader
	isInteractive func(io.Reader) bool
}

func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return NewRootCmd().ExecuteContext(ctx)
}

func NewRootCmd() *cobra.Command {
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		prompt.Register, choice.Register, dockerstep.Register, luastep.Register, shell.Register, agent.Register,
	} {
		if err := register(registry); err != nil {
			panic(err)
		}
	}
	return newRootCmd(dependencies{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		cwd: os.Getwd, homeDir: os.UserHomeDir, configDir: os.UserConfigDir, registry: registry,
		loader: workflow.NewLoader(nil), isInteractive: interactive,
	})
}

func newRootCmd(deps dependencies) *cobra.Command {
	if deps.isInteractive == nil {
		deps.isInteractive = interactive
	}
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
	root.AddCommand(newRunCmd(deps), newListCmd(deps), newValidateCmd(deps), newCompletionCmd())
	return root
}

func runWorkflowPicker(command *cobra.Command, deps dependencies) error {
	cwd, home, config, err := directories(deps)
	if err != nil {
		return err
	}
	sources, err := workflow.DiscoverAll(cwd, home, config)
	if err != nil {
		return err
	}
	if !deps.isInteractive(command.InOrStdin()) {
		for _, source := range sources {
			fmt.Fprintf(command.OutOrStdout(), "%s\t%s\t%s\t%s\n", source.Name, source.Scope, source.Description, source.Path)
		}
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
	if source.Effective {
		fmt.Fprintf(command.OutOrStdout(), "wuko run %s\n", shellQuote(source.Name))
		return nil
	}
	fmt.Fprintf(command.OutOrStdout(), "wuko run --file %s\n", shellQuote(source.Path))
	return nil
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

func parseVars(values []string) (map[string]any, error) {
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
