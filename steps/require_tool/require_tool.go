// Package requiretool verifies that an executable is available and optionally checks its version.
package requiretool

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	masterminds "github.com/Masterminds/semver/v3"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
)

const captureLimit = 64 * 1024

var semanticVersionPattern = regexp.MustCompile(`v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?`)

type Config struct {
	Tool        string   `yaml:"tool"`
	VersionArgs []string `yaml:"version_args,omitempty"`
	Constraint  string   `yaml:"constraint,omitempty"`
}

type Runner struct {
	config     Config
	constraint *masterminds.Constraints
}

func (*Runner) ExecutorAware() {}

func Register(registry *step.Registry) error { return registry.Register("require_tool", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if _, configured := raw["version_args"]; !configured {
		config.VersionArgs = []string{"--version"}
	}
	if strings.TrimSpace(config.Tool) == "" {
		return nil, fmt.Errorf("tool is required")
	}
	runner := &Runner{config: config}
	if config.Constraint != "" && !templated(config.Constraint) {
		constraint, err := masterminds.NewConstraint(config.Constraint)
		if err != nil {
			return nil, fmt.Errorf("parsing constraint: %w", err)
		}
		runner.constraint = constraint
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if err := r.validateResolvedConfig(); err != nil {
		return step.Result{}, err
	}

	executor := request.Executor
	local := executor == nil
	if local {
		executor = process.LocalExecutor{}
	}
	environment := step.ApplyAttemptEnvironment(maps.Clone(request.Env), request)
	result, err := executor.Run(ctx, process.Options{
		Command: r.config.Tool, Args: r.config.VersionArgs, Dir: request.RunDir,
		Env: environment, CaptureLimit: captureLimit,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return step.Result{}, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return step.Result{}, err
		}
		return step.Result{}, unavailableError(r.config.Tool, result, err)
	}

	outputs := map[string]any{"path": r.executablePath(local)}
	if r.constraint == nil {
		return step.Result{Outputs: outputs}, nil
	}
	version, err := findVersion(result.Stdout, result.Stderr)
	if err != nil {
		return step.Result{}, fmt.Errorf("required tool %q: %w", r.config.Tool, err)
	}
	if !r.constraint.Check(version) {
		return step.Result{}, fmt.Errorf("required tool %q version %s does not satisfy constraint %q", r.config.Tool, version, r.config.Constraint)
	}
	outputs["version"] = version.String()
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) validateResolvedConfig() error {
	if templated(r.config.Tool) || templated(r.config.Constraint) {
		return fmt.Errorf("require_tool configuration contains an unresolved template")
	}
	for _, argument := range r.config.VersionArgs {
		if templated(argument) {
			return fmt.Errorf("require_tool configuration contains an unresolved template")
		}
	}
	if r.config.Constraint != "" && r.constraint == nil {
		return fmt.Errorf("constraint was not resolved before execution")
	}
	return nil
}

func (r *Runner) executablePath(local bool) string {
	if !local {
		return r.config.Tool
	}
	path, err := exec.LookPath(r.config.Tool)
	if err != nil {
		return r.config.Tool
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return absolute
}

func findVersion(stdout, stderr string) (*masterminds.Version, error) {
	for _, output := range []string{stdout, stderr} {
		for _, candidate := range semanticVersionPattern.FindAllString(output, -1) {
			version, err := masterminds.StrictNewVersion(strings.TrimPrefix(candidate, "v"))
			if err == nil {
				return version, nil
			}
		}
	}
	return nil, fmt.Errorf("version output does not contain a semantic version")
}

func unavailableError(tool string, result process.Result, err error) error {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return fmt.Errorf("required tool %q is unavailable: %w", tool, err)
	}
	return fmt.Errorf("required tool %q is unavailable: %s: %w", tool, detail, err)
}

func templated(value string) bool { return strings.Contains(value, "{{") }
