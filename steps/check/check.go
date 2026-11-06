// Package check implements collected, non-fatal workflow checks.
package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

type Check struct {
	ID       string `yaml:"id"`
	Expr     string `yaml:"expr"`
	Message  string `yaml:"message"`
	Fix      string `yaml:"fix,omitempty"`
	Severity string `yaml:"severity,omitempty"`
}

type Config struct {
	Checks []Check `yaml:"checks"`
}

type Runner struct {
	checks   []Check
	programs []*vm.Program
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("check", step.Registration{Builder: New, Config: Config{}, Outputs: step.ClosedObject(map[string]step.OutputSchema{"checks": step.CheckSchema()}), References: references})
}

func references(raw map[string]any) (step.References, error) {
	var refs step.References
	items, _ := raw["checks"].([]any)
	for i, item := range items {
		mapping, _ := item.(map[string]any)
		reference := step.ExpressionField(mapping, "expr")
		reference.Label = fmt.Sprintf("checks[%d].expr", i)
		refs.Expressions = append(refs.Expressions, reference)
	}
	return refs, nil
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if step.ConfigKnown(raw, "checks") && len(config.Checks) == 0 {
		return nil, fmt.Errorf("checks must not be empty")
	}
	runner := &Runner{checks: config.Checks, programs: make([]*vm.Program, len(config.Checks))}
	for i := range runner.checks {
		check := &runner.checks[i]
		for _, field := range []struct{ name, value string }{{"id", check.ID}, {"expr", check.Expr}, {"message", check.Message}} {
			if step.ConfigKnown(raw, fmt.Sprintf("checks.%d.%s", i, field.name)) && strings.TrimSpace(field.value) == "" {
				return nil, fmt.Errorf("checks[%d].%s is required", i, field.name)
			}
		}
		if check.Severity == "" {
			check.Severity = "fail"
		}
		if step.ConfigKnown(raw, fmt.Sprintf("checks.%d.severity", i)) && !strings.Contains(check.Severity, "{{") && check.Severity != "fail" && check.Severity != "warn" {
			return nil, fmt.Errorf("checks[%d].severity must be fail or warn", i)
		}
		if !step.ConfigKnown(raw, fmt.Sprintf("checks.%d.expr", i)) || strings.Contains(check.Expr, "{{") {
			continue
		}
		program, err := wukoexpr.Compile(check.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables(), expr.AsBool())
		if err != nil {
			return nil, fmt.Errorf("checks[%d].expr: %w", i, err)
		}
		runner.programs[i] = program
	}
	return runner, nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	findings := make([]step.CheckFinding, 0, len(runner.checks))
	environment := request.ExpressionEnvironment(nil)
	for i, check := range runner.checks {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		if check.Severity != "fail" && check.Severity != "warn" {
			return step.Result{}, fmt.Errorf("check %q: severity must be fail or warn", check.ID)
		}
		program := runner.programs[i]
		if program == nil {
			// A literal binding may contain template delimiters as expression data.
			// It is intentionally left uncompiled during static template validation.
			var err error
			program, err = wukoexpr.Compile(check.Expr, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables(), expr.AsBool())
			if err != nil {
				return step.Result{}, fmt.Errorf("check %q: compiling expr: %w", check.ID, err)
			}
		}
		value, err := expr.Run(program, environment)
		if err != nil {
			return step.Result{}, fmt.Errorf("check %q: evaluating expr: %w", check.ID, err)
		}
		matched, ok := value.(bool)
		if !ok {
			return step.Result{}, fmt.Errorf("check %q: expression returned %T, want bool", check.ID, value)
		}
		status := check.Severity
		if matched {
			status = "ok"
		}
		findings = append(findings, step.CheckFinding{ID: check.ID, Status: status, Message: check.Message, Fix: check.Fix})
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{"checks": step.CheckValues(findings)}, Checks: findings}, nil
}
