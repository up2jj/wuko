package process

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

type CallConfig struct {
	Worker      string             `yaml:"worker,omitempty"`
	Pool        string             `yaml:"pool,omitempty"`
	Payload     any                `yaml:"payload,omitempty"`
	PayloadExpr string             `yaml:"payload_expr,omitempty"`
	Timeout     *workflow.Duration `yaml:"timeout,omitempty"`
}

type CallRunner struct {
	config         CallConfig
	registry       *rpcRegistry
	poolProgram    *vm.Program
	payloadProgram *vm.Program
}

func (*CallRunner) ExecutorAware() {}

func newCall(raw map[string]any, registry *rpcRegistry) (step.Runner, error) {
	var config CallConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	hasWorker := strings.TrimSpace(config.Worker) != ""
	hasPool := strings.TrimSpace(config.Pool) != ""
	if hasWorker == hasPool {
		return nil, fmt.Errorf("exactly one of worker or pool is required")
	}
	_, hasPayload := raw["payload"]
	if hasPayload && strings.TrimSpace(config.PayloadExpr) != "" {
		return nil, fmt.Errorf("payload cannot be combined with payload_expr")
	}
	if hasPayload && !workflow.ActionDataValue(config.Payload) {
		return nil, fmt.Errorf("payload must be a YAML/JSON-compatible value")
	}
	if config.Timeout != nil && config.Timeout.Value() <= 0 {
		return nil, fmt.Errorf("timeout must be positive")
	}

	var poolProgram *vm.Program
	var err error
	if hasPool {
		poolProgram, err = wukoexpr.Compile(config.Pool, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling pool expression: %w", err)
		}
	}
	var payloadProgram *vm.Program
	if strings.TrimSpace(config.PayloadExpr) != "" {
		payloadProgram, err = wukoexpr.Compile(config.PayloadExpr, expr.Env(expressionEnvironment{}))
		if err != nil {
			return nil, fmt.Errorf("compiling payload expression: %w", err)
		}
	}
	return &CallRunner{config: config, registry: registry, poolProgram: poolProgram, payloadProgram: payloadProgram}, nil
}

func (runner *CallRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if runner.registry == nil {
		return step.Result{}, fmt.Errorf("process RPC registry is unavailable")
	}
	workerIDs, err := runner.workerIDs(request)
	if err != nil {
		return step.Result{}, err
	}
	payload := runner.config.Payload
	if runner.payloadProgram != nil {
		payload, err = expr.Run(runner.payloadProgram, expressionEnvironmentFor(request))
		if err != nil {
			return step.Result{}, fmt.Errorf("evaluating payload expression: %w", err)
		}
	}
	if !workflow.ActionDataValue(payload) {
		return step.Result{}, fmt.Errorf("payload expression returned %T, want YAML/JSON-compatible value", payload)
	}
	callCtx, cancel := context.WithTimeout(ctx, duration(runner.config.Timeout, 30*time.Second))
	defer cancel()
	result, workerID, err := runner.registry.call(callCtx, workerIDs, payload)
	if err != nil {
		return step.Result{}, err
	}
	if !workflow.ActionDataValue(result) {
		return step.Result{}, fmt.Errorf("process RPC result returned %T, want YAML/JSON-compatible value", result)
	}
	return step.Result{Outputs: map[string]any{"result": result, "worker_id": workerID}}, nil
}

func (runner *CallRunner) workerIDs(request step.Request) ([]string, error) {
	if runner.poolProgram == nil {
		worker := strings.TrimSpace(runner.config.Worker)
		if worker == "" {
			return nil, fmt.Errorf("worker rendered to an empty identifier")
		}
		return []string{worker}, nil
	}
	value, err := expr.Run(runner.poolProgram, expressionEnvironmentFor(request))
	if err != nil {
		return nil, fmt.Errorf("evaluating pool expression: %w", err)
	}
	return rpcWorkerIDs(value)
}

func rpcWorkerIDs(value any) ([]string, error) {
	list := reflect.ValueOf(value)
	if !list.IsValid() || (list.Kind() != reflect.Array && list.Kind() != reflect.Slice) || list.Len() == 0 {
		return nil, fmt.Errorf("pool expression must return a non-empty list")
	}
	result := make([]string, 0, list.Len())
	seen := make(map[string]struct{}, list.Len())
	for index := range list.Len() {
		item := list.Index(index)
		for item.IsValid() && item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("pool expression item %d is null", index)
			}
			item = item.Elem()
		}
		var id string
		switch {
		case item.IsValid() && item.Kind() == reflect.String:
			id = item.String()
		case item.IsValid() && item.CanInterface():
			if output, ok := item.Interface().(map[string]any); ok {
				id, _ = output["worker_id"].(string)
			}
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("pool expression item %d must be a worker ID or an object containing worker_id", index)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}
