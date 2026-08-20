// Package jsonpath implements RFC 9535 JSONPath selection.
package jsonpath

import (
	"context"
	"fmt"
	"strings"

	theoryjsonpath "github.com/theory/jsonpath"
	"github.com/up2jj/wuko/step"
)

const (
	resultAll = "all"
	resultOne = "one"
)

type Config struct {
	From     string `yaml:"from"`
	Query    string `yaml:"query"`
	Result   string `yaml:"result,omitempty"`
	Variable string `yaml:"variable,omitempty"`
}

type Runner struct {
	config Config
	path   *theoryjsonpath.Path
}

func Register(registry *step.Registry) error { return registry.Register("jsonpath", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.From) == "" {
		return nil, fmt.Errorf("from is required")
	}
	if !templated(config.From) {
		parts := strings.Split(config.From, ".")
		if len(parts) < 2 || parts[1] == "" || (parts[0] != "vars" && parts[0] != "steps") {
			return nil, fmt.Errorf("from must be a dotted path rooted at vars or steps")
		}
	}
	if strings.TrimSpace(config.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if config.Result == "" {
		config.Result = resultAll
	}
	if !templated(config.Result) && config.Result != resultAll && config.Result != resultOne {
		return nil, fmt.Errorf("result must be all or one")
	}

	var path *theoryjsonpath.Path
	if !templated(config.Query) {
		var err error
		path, err = theoryjsonpath.Parse(config.Query)
		if err != nil {
			return nil, fmt.Errorf("parsing query: %w", err)
		}
	}
	return &Runner{config: config, path: path}, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	input, err := step.Lookup(request, r.config.From)
	if err != nil {
		return step.Result{}, fmt.Errorf("resolving input: %w", err)
	}
	if r.path == nil {
		return step.Result{}, fmt.Errorf("query was not resolved before execution")
	}

	matches := r.path.SelectLocated(input)
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	values := make([]any, len(matches))
	paths := make([]any, len(matches))
	for i, match := range matches {
		values[i] = match.Node
		paths[i] = match.Path.String()
	}

	var value any = values
	switch r.config.Result {
	case resultAll:
	case resultOne:
		if len(values) != 1 {
			return step.Result{}, fmt.Errorf("query returned %d matches, want exactly one", len(values))
		}
		value = values[0]
	default:
		return step.Result{}, fmt.Errorf("result must be all or one")
	}

	result := step.Result{Outputs: map[string]any{
		"value": value,
		"paths": paths,
		"count": len(values),
	}}
	if r.config.Variable != "" {
		result.Variables = map[string]any{r.config.Variable: value}
	}
	return result, nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }
