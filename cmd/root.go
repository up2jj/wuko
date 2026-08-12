package cmd

import (
	"context"
	"encoding/json"
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
	luastep "github.com/up2jj/wuko/steps/lua"
	"github.com/up2jj/wuko/steps/prompt"
	"github.com/up2jj/wuko/steps/shell"
	"github.com/up2jj/wuko/workflow"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type dependencies struct {
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	cwd       func() (string, error)
	homeDir   func() (string, error)
	configDir func() (string, error)
	registry  *step.Registry
	loader    *workflow.Loader
}

func Execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return NewRootCmd().ExecuteContext(ctx)
}

func NewRootCmd() *cobra.Command {
	registry := step.NewRegistry()
	for _, register := range []func(*step.Registry) error{
		prompt.Register, choice.Register, luastep.Register, shell.Register, agent.Register,
	} {
		if err := register(registry); err != nil {
			panic(err)
		}
	}
	return newRootCmd(dependencies{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		cwd: os.Getwd, homeDir: os.UserHomeDir, configDir: os.UserConfigDir, registry: registry,
		loader: workflow.NewLoader(nil),
	})
}

func newRootCmd(deps dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "wuko",
		Short:         "Run trusted YAML workflows",
		Version:       fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(deps.stdin)
	root.SetOut(deps.stdout)
	root.SetErr(deps.stderr)
	root.AddCommand(newRunCmd(deps), newListCmd(deps), newValidateCmd(deps), newCompletionCmd())
	return root
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
