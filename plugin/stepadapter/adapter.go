package stepadapter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/step"
)

// ServiceRunner is a native step runner that declares the managed-service
// policy it will use when it starts its background work.
type ServiceRunner interface {
	step.Runner
	ServiceOptions() (string, step.ServiceOptions)
}

// ServiceRegistration describes one native managed-service step exposed by a
// plugin. Cleanup must be true when runners built by Builder implement
// step.Cleaner and require their successful results to be cleaned up.
type ServiceRegistration struct {
	Builder step.Builder
	Cleanup bool
}

// RegisterService registers a Wuko-native managed-service runner with a plugin.
// It adapts validation, host callbacks, readiness, shutdown, and optional
// cleanup while preserving the SDK's standard-library-only package boundary.
func RegisterService(plugin *sdk.Plugin, name string, registration ServiceRegistration, options ...sdk.StepOption) error {
	if registration.Builder == nil {
		return fmt.Errorf("service step %q requires a builder", name)
	}
	callbacks, err := sdk.DeclaredHostCallbacks(options...)
	if err != nil {
		return fmt.Errorf("service step %q: %w", name, err)
	}
	// The host renders with secrets enabled when host.function.call is declared, so the
	// secret-free renderer variant must refuse to render rather than reach them.
	secretsReachable := slices.Contains(callbacks, sdk.HostFunctionCall)
	handler := sdk.StepHandler[map[string]any]{
		Validate: func(ctx context.Context, request sdk.ValidateRequest[map[string]any]) error {
			runner, err := buildServiceRunner(name, registration.Builder, request.Config)
			if err != nil {
				return pluginError(err)
			}
			validator, ok := runner.(step.Validator)
			if !ok {
				return nil
			}
			return pluginError(validator.Validate(ctx, stepRequest(ctx, request.Context, request.Host, secretsReachable, nil, nil, nil)))
		},
		Service: func(_ context.Context, request sdk.ServiceRequest[map[string]any]) (sdk.Service, error) {
			runner, err := buildServiceRunner(name, registration.Builder, request.Config)
			if err != nil {
				return sdk.Service{}, pluginError(err)
			}
			kind, serviceOptions, err := declaredService(runner)
			if err != nil {
				return sdk.Service{}, pluginError(err)
			}
			return sdk.Service{
				Kind: kind, KeepAlive: serviceOptions.KeepAlive,
				FailFast: serviceOptions.FailFast, ExitOnEnd: serviceOptions.ExitOnEnd,
			}, nil
		},
		Run: func(ctx context.Context, request sdk.RunRequest[map[string]any]) (sdk.Result, error) {
			runner, err := buildServiceRunner(name, registration.Builder, request.Config)
			if err != nil {
				return sdk.Result{}, pluginError(err)
			}
			result, err := runService(ctx, request, runner, secretsReachable)
			return result, pluginError(err)
		},
	}
	if registration.Cleanup {
		handler.Cleanup = func(ctx context.Context, request sdk.CleanupRequest[map[string]any]) error {
			runner, err := buildServiceRunner(name, registration.Builder, request.Config)
			if err != nil {
				return pluginError(err)
			}
			cleaner, ok := runner.(step.Cleaner)
			if !ok {
				return pluginError(fmt.Errorf("service step %q declared cleanup but its runner does not implement step.Cleaner", name))
			}
			return pluginError(cleaner.Cleanup(ctx, step.Result{
				Outputs: normalizeMap(request.Result.Outputs), Variables: normalizeMap(request.Result.Variables),
			}))
		}
	}
	return sdk.RegisterStep(plugin, name, handler, options...)
}

func buildServiceRunner(name string, builder step.Builder, config map[string]any) (ServiceRunner, error) {
	runner, err := builder(normalizeMap(config))
	if err != nil {
		return nil, fmt.Errorf("building service step %q: %w", name, err)
	}
	service, ok := runner.(ServiceRunner)
	if !ok {
		return nil, fmt.Errorf("service step %q runner does not implement stepadapter.ServiceRunner", name)
	}
	return service, nil
}

func declaredService(runner ServiceRunner) (string, step.ServiceOptions, error) {
	kind, options := runner.ServiceOptions()
	if strings.TrimSpace(kind) == "" {
		return "", step.ServiceOptions{}, fmt.Errorf("managed service returned an empty kind")
	}
	return kind, options, nil
}

func runService(ctx context.Context, request sdk.RunRequest[map[string]any], runner ServiceRunner, secretsReachable bool) (sdk.Result, error) {
	kind, options, err := declaredService(runner)
	if err != nil {
		return sdk.Result{}, err
	}
	serviceCtx, stopService := context.WithCancel(context.WithoutCancel(ctx))
	launcher := newServiceLauncher(serviceCtx, stopService, kind, options)
	result, runErr := runner.Run(ctx, stepRequest(ctx, request.Context, request.Host, secretsReachable, request.Stdout, request.Stderr, launcher))
	if runErr != nil {
		return sdk.Result{}, errors.Join(runErr, launcher.stopAndWait())
	}
	if err := launcher.registered(); err != nil {
		stopService()
		return sdk.Result{}, err
	}
	wire := sdk.Result{Outputs: result.Outputs, Variables: result.Variables}
	return finishService(ctx, launcher, wire, request.Ready)
}

func finishService(ctx context.Context, launcher *serviceLauncher, wire sdk.Result, ready func(sdk.Result) error) (sdk.Result, error) {
	// The runner returned successfully after registering its work, which is readiness in
	// process too. Work that already exited is reported after readiness, exactly as the
	// in-process supervisor observes it, so the outcome does not depend on scheduling.
	if err := ready(wire); err != nil {
		launcher.stop()
		return sdk.Result{}, errors.Join(err, <-launcher.done)
	}
	var serviceErr error
	select {
	case serviceErr = <-launcher.done:
		launcher.stop()
	case <-ctx.Done():
		launcher.stop()
		serviceErr = <-launcher.done
	}
	if ctx.Err() != nil && (serviceErr == nil || cancellationOnly(serviceErr)) {
		return wire, nil
	}
	return wire, serviceErr
}

type serviceLauncher struct {
	mu      sync.Mutex
	ctx     context.Context
	stop    context.CancelFunc
	kind    string
	options step.ServiceOptions
	done    chan error
	started bool
}

func newServiceLauncher(ctx context.Context, stop context.CancelFunc, kind string, options step.ServiceOptions) *serviceLauncher {
	return &serviceLauncher{ctx: ctx, stop: stop, kind: kind, options: options, done: make(chan error, 1)}
}

func (launcher *serviceLauncher) StartService(_ string, kind string, options step.ServiceOptions, run func(context.Context) error) error {
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if launcher.started {
		return fmt.Errorf("service registered managed work more than once")
	}
	if run == nil {
		return fmt.Errorf("service registered nil managed work")
	}
	if kind != launcher.kind || options != launcher.options {
		return fmt.Errorf("service policy changed between declaration and startup")
	}
	launcher.started = true
	go func() { launcher.done <- runManaged(launcher.ctx, run) }()
	return nil
}

// runManaged contains a panic in managed work. The work runs in its own goroutine, so an
// escaped panic would end the whole plugin process and every concurrent service it hosts.
func runManaged(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if failure := control.RecoveredPanic(recover()); failure != nil {
			err = failure
		}
	}()
	return run(ctx)
}

func (launcher *serviceLauncher) registered() error {
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	if !launcher.started {
		return fmt.Errorf("service step did not register managed work")
	}
	return nil
}

func (launcher *serviceLauncher) stopAndWait() error {
	launcher.mu.Lock()
	started := launcher.started
	launcher.mu.Unlock()
	launcher.stop()
	if !started {
		return nil
	}
	serviceErr := <-launcher.done
	if serviceAbortOnly(serviceErr) {
		return nil
	}
	return serviceErr
}

func pluginError(err error) error {
	if err == nil || cancellationOnly(err) {
		return err
	}
	var coded *sdk.Error
	if errors.As(err, &coded) && coded.Code != "" {
		return err
	}
	return sdk.Coded("step_failed", err)
}

func cancellationOnly(err error) bool {
	return errorOnly(err, func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	})
}

func serviceAbortOnly(err error) bool {
	return errorOnly(err, func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, step.ErrServiceAborted)
	})
}

func errorOnly(err error, allowed func(error) bool) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !errorOnly(child, allowed) {
				return false
			}
		}
		return true
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return errorOnly(wrapped, allowed)
	}
	return allowed(err)
}
