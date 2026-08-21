// Package semver implements semantic-version parsing, comparison, constraint checks, and increments.
package semver

import (
	"context"
	"fmt"
	"math"
	"strings"

	masterminds "github.com/Masterminds/semver/v3"
	"github.com/up2jj/wuko/step"
)

const (
	operationParse     = "parse"
	operationCompare   = "compare"
	operationConstrain = "constrain"
	operationIncrement = "increment"
)

type Config struct {
	Operation  string `yaml:"operation"`
	Version    string `yaml:"version"`
	Other      string `yaml:"other,omitempty"`
	Constraint string `yaml:"constraint,omitempty"`
	Part       string `yaml:"part,omitempty"`
	Variable   string `yaml:"variable,omitempty"`
}

type Runner struct {
	config     Config
	version    *masterminds.Version
	other      *masterminds.Version
	constraint *masterminds.Constraints
}

func Register(registry *step.Registry) error { return registry.Register("semver", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Operation) == "" {
		return nil, fmt.Errorf("operation is required")
	}
	if strings.TrimSpace(config.Version) == "" {
		return nil, fmt.Errorf("version is required")
	}
	if config.Variable != "" && strings.TrimSpace(config.Variable) == "" {
		return nil, fmt.Errorf("variable must not be blank")
	}
	if templated(config.Operation) {
		return &Runner{config: config}, nil
	}

	runner := &Runner{config: config}
	if !templated(config.Version) {
		version, err := parseVersion(config.Version)
		if err != nil {
			return nil, fmt.Errorf("parsing version: %w", err)
		}
		runner.version = version
	}

	switch config.Operation {
	case operationParse:
		if config.Other != "" || config.Constraint != "" || config.Part != "" {
			return nil, fmt.Errorf("parse does not accept other, constraint, or part")
		}
	case operationCompare:
		if strings.TrimSpace(config.Other) == "" {
			return nil, fmt.Errorf("other is required for compare")
		}
		if config.Constraint != "" || config.Part != "" {
			return nil, fmt.Errorf("compare does not accept constraint or part")
		}
		if !templated(config.Other) {
			other, err := parseVersion(config.Other)
			if err != nil {
				return nil, fmt.Errorf("parsing other: %w", err)
			}
			runner.other = other
		}
	case operationConstrain:
		if strings.TrimSpace(config.Constraint) == "" {
			return nil, fmt.Errorf("constraint is required for constrain")
		}
		if config.Other != "" || config.Part != "" {
			return nil, fmt.Errorf("constrain does not accept other or part")
		}
		if !templated(config.Constraint) {
			constraint, err := masterminds.NewConstraint(config.Constraint)
			if err != nil {
				return nil, fmt.Errorf("parsing constraint: %w", err)
			}
			runner.constraint = constraint
		}
	case operationIncrement:
		if config.Other != "" || config.Constraint != "" {
			return nil, fmt.Errorf("increment does not accept other or constraint")
		}
		if !templated(config.Part) && config.Part != "major" && config.Part != "minor" && config.Part != "patch" {
			return nil, fmt.Errorf("part must be major, minor, or patch")
		}
	default:
		return nil, fmt.Errorf("operation must be parse, compare, constrain, or increment")
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if r.version == nil {
		return step.Result{}, fmt.Errorf("version was not resolved before execution")
	}

	outputs := map[string]any{"version": r.version.String()}
	var value any
	switch r.config.Operation {
	case operationParse:
		value = r.version.String()
		outputs["major"] = r.version.Major()
		outputs["minor"] = r.version.Minor()
		outputs["patch"] = r.version.Patch()
		outputs["prerelease"] = r.version.Prerelease()
		outputs["metadata"] = r.version.Metadata()
	case operationCompare:
		if r.other == nil {
			return step.Result{}, fmt.Errorf("other was not resolved before execution")
		}
		comparison := r.version.Compare(r.other)
		value = comparison
		outputs["other"] = r.other.String()
		outputs["comparison"] = comparison
		outputs["less"] = comparison < 0
		outputs["equal"] = comparison == 0
		outputs["greater"] = comparison > 0
	case operationConstrain:
		if r.constraint == nil {
			return step.Result{}, fmt.Errorf("constraint was not resolved before execution")
		}
		matched := r.constraint.Check(r.version)
		value = matched
		outputs["constraint"] = r.config.Constraint
		outputs["matched"] = matched
	case operationIncrement:
		next, err := increment(*r.version, r.config.Part)
		if err != nil {
			return step.Result{}, err
		}
		value = next.String()
		outputs["previous"] = r.version.String()
		outputs["version"] = next.String()
		outputs["part"] = r.config.Part
	default:
		return step.Result{}, fmt.Errorf("operation was not resolved before execution")
	}
	outputs["value"] = value

	result := step.Result{Outputs: outputs}
	if r.config.Variable != "" {
		result.Variables = map[string]any{r.config.Variable: value}
	}
	return result, nil
}

func parseVersion(value string) (*masterminds.Version, error) {
	value = strings.TrimPrefix(value, "v")
	return masterminds.StrictNewVersion(value)
}

func increment(version masterminds.Version, part string) (masterminds.Version, error) {
	switch part {
	case "major":
		if version.Major() == math.MaxUint64 {
			return masterminds.Version{}, fmt.Errorf("major version cannot be incremented beyond %d", uint64(math.MaxUint64))
		}
		return version.IncMajor(), nil
	case "minor":
		if version.Minor() == math.MaxUint64 {
			return masterminds.Version{}, fmt.Errorf("minor version cannot be incremented beyond %d", uint64(math.MaxUint64))
		}
		return version.IncMinor(), nil
	case "patch":
		if version.Prerelease() == "" && version.Patch() == math.MaxUint64 {
			return masterminds.Version{}, fmt.Errorf("patch version cannot be incremented beyond %d", uint64(math.MaxUint64))
		}
		return version.IncPatch(), nil
	default:
		return masterminds.Version{}, fmt.Errorf("part must be major, minor, or patch")
	}
}

func templated(value string) bool { return strings.Contains(value, "{{") }
