package stepadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/plugin/sdk/sdktest"
	"github.com/up2jj/wuko/step"
)

type serviceFixture struct {
	builds    atomic.Int32
	validates atomic.Int32
	cleanups  atomic.Int32
	started   chan string
	cleaned   chan step.Result
}

type fixtureRunner struct {
	fixture *serviceFixture
	config  map[string]any
}

func fixtureBuilder(fixture *serviceFixture) step.Builder {
	return func(config map[string]any) (step.Runner, error) {
		fixture.builds.Add(1)
		if value, ok := config["large"].(int); !ok || int64(value) != 9007199254740993 {
			return nil, fmt.Errorf("large = %#v (%T)", config["large"], config["large"])
		}
		return &fixtureRunner{fixture: fixture, config: config}, nil
	}
}

func (runner *fixtureRunner) ServiceOptions() (string, step.ServiceOptions) {
	kind, _ := runner.config["kind"].(string)
	if kind == "" {
		kind = "fixture"
	}
	keepAlive, _ := runner.config["keep_alive"].(bool)
	return kind, step.ServiceOptions{KeepAlive: keepAlive, FailFast: true}
}

func (runner *fixtureRunner) Validate(ctx context.Context, request step.Request) error {
	runner.fixture.validates.Add(1)
	if request.StepID != "server" || request.WorkflowName != "test" || request.Vars["count"] != int(7) {
		return fmt.Errorf("validation request = %+v", request)
	}
	if request.PreviousAttempt == nil || request.PreviousAttempt.Outputs["attempt"] != int(1) {
		return fmt.Errorf("previous attempt = %#v", request.PreviousAttempt)
	}
	return request.TemplateRenderer.ValidateContent("hello")
}

func (runner *fixtureRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	mode, _ := runner.config["mode"].(string)
	switch mode {
	case "runner_failure":
		return step.Result{}, fmt.Errorf("startup failed")
	case "missing":
		return step.Result{}, nil
	case "mismatch":
		err := request.Services.StartService(request.StepID, "different", step.ServiceOptions{FailFast: true}, func(context.Context) error { return nil })
		return step.Result{}, err
	case "duplicate":
		kind, options := runner.ServiceOptions()
		if err := request.Services.StartService(request.StepID, kind, options, func(serviceCtx context.Context) error {
			<-serviceCtx.Done()
			return serviceCtx.Err()
		}); err != nil {
			return step.Result{}, err
		}
		err := request.Services.StartService(request.StepID, kind, options, func(context.Context) error { return nil })
		return step.Result{}, err
	}

	rendered, err := request.TemplateRenderer.(step.DataTemplateRenderer).RenderContentWith("hello {{ .name }}", map[string]any{"name": "wuko"})
	if err != nil {
		return step.Result{}, err
	}
	secret, err := request.ResolveSecret("token")
	if err != nil {
		return step.Result{}, err
	}
	decorated, err := request.Helpers["decorate"](ctx, []any{json.Number("11")})
	if err != nil {
		return step.Result{}, err
	}
	if _, err := io.WriteString(request.Stdout, "service started\n"); err != nil {
		return step.Result{}, err
	}
	kind, options := runner.ServiceOptions()
	id, _ := runner.config["id"].(string)
	err = request.Services.StartService(request.StepID, kind, options, func(serviceCtx context.Context) error {
		runner.fixture.started <- id
		<-serviceCtx.Done()
		if mode == "verification_failure" {
			return errors.Join(serviceCtx.Err(), fmt.Errorf("verification failed"))
		}
		return serviceCtx.Err()
	})
	if err != nil {
		return step.Result{}, err
	}
	return step.Result{Outputs: map[string]any{
		"rendered": rendered, "secret": secret, "decorated": decorated, "count": request.Vars["count"], "id": id,
	}}, nil
}

func (runner *fixtureRunner) Cleanup(_ context.Context, result step.Result) error {
	runner.fixture.cleanups.Add(1)
	runner.fixture.cleaned <- result
	return nil
}

func testConfig(extra map[string]any) map[string]any {
	config := map[string]any{"large": json.Number("9007199254740993")}
	for name, value := range extra {
		config[name] = value
	}
	return config
}

func testStepContext(id string) sdk.StepContext {
	return sdk.StepContext{
		StepID: id, WorkflowName: "test", WorkflowSource: "/work/wuko.yaml", WorkflowDir: "/work",
		WorkflowTimezone: "Europe/Warsaw", RunDir: "/run", EnvironmentLoaders: []string{"dotenv"},
		LocalValueDir: "/local", GlobalValueDir: "/global", Vars: map[string]any{"count": 7},
		PresetVars: map[string]any{}, Inputs: map[string]any{}, Env: map[string]string{}, Steps: map[string]any{},
		Dependencies: map[string]map[string]any{}, Bindings: map[string]any{}, Providers: map[string]any{},
		Helpers: []string{"decorate"}, Attempt: 2, MaxAttempts: 3, OperationID: "operation",
		PreviousAttempt: &sdk.Result{Outputs: map[string]any{"attempt": 1}},
	}
}

func registerFixture(t *testing.T, fixture *serviceFixture, cleanup bool) *sdk.Plugin {
	t.Helper()
	plugin, err := sdk.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	err = RegisterService(plugin, "server", ServiceRegistration{Builder: fixtureBuilder(fixture), Cleanup: cleanup},
		sdk.WithHostCallbacks(sdk.HostTemplateValidate, sdk.HostTemplateRender, sdk.HostFunctionCall),
		sdk.WithOutputSchema(sdk.Object(map[string]sdk.OutputSchema{"id": sdk.Scalar()})))
	if err != nil {
		t.Fatal(err)
	}
	return plugin
}

func startFixture(t *testing.T, fixture *serviceFixture, cleanup bool) *sdktest.Harness {
	t.Helper()
	return sdktest.Start(t, registerFixture(t, fixture, cleanup), sdktest.WithCallbacks(sdktest.Callbacks{
		ValidateTemplate: func(_ context.Context, content string) error {
			if content != "hello" {
				return fmt.Errorf("content = %q", content)
			}
			return nil
		},
		RenderTemplate: func(_ context.Context, content string, extra map[string]any) (string, error) {
			return strings.ReplaceAll(content, "{{ .name }}", fmt.Sprint(extra["name"])), nil
		},
		CallFunction: func(_ context.Context, name string, args []any) (any, error) {
			switch name {
			case "secret":
				return "secret-value", nil
			case "decorate":
				return args[0], nil
			default:
				return nil, fmt.Errorf("unexpected function %q", name)
			}
		},
	}))
}

func newFixture() *serviceFixture {
	return &serviceFixture{started: make(chan string, 8), cleaned: make(chan step.Result, 1)}
}

func TestRegisterServiceLifecycle(t *testing.T) {
	fixture := newFixture()
	host := startFixture(t, fixture, true)
	initialization := host.Initialization()
	if len(initialization.Steps) != 1 {
		t.Fatalf("steps = %+v", initialization.Steps)
	}
	declaration := initialization.Steps[0]
	if declaration.Type != "fixture.server" || !declaration.Service || !declaration.Cleanup || len(declaration.HostCallbacks) != 3 || declaration.Outputs == nil {
		t.Fatalf("declaration = %+v", declaration)
	}

	config := testConfig(map[string]any{"keep_alive": true, "id": "one"})
	serviceResponse := host.CallStep(sdktest.StepRequest{Operation: sdktest.StepService, Type: "fixture.server", With: config}).Await()
	var policy sdk.Service
	if err := serviceResponse.DecodeResult(&policy); err != nil {
		t.Fatal(err)
	}
	if policy.Kind != "fixture" || !policy.KeepAlive || !policy.FailFast {
		t.Fatalf("policy = %+v", policy)
	}

	validation := host.CallStep(sdktest.StepRequest{
		Operation: sdktest.StepValidate, Type: "fixture.server", With: config, Context: testStepContext("server"),
	}).Await()
	if validation.Error != nil {
		t.Fatal(validation.Error)
	}

	call := host.CallStep(sdktest.StepRequest{Type: "fixture.server", With: config, Context: testStepContext("server")})
	ready := call.AwaitEvent("ready")
	var result sdk.Result
	if err := ready.DecodeResult(&result); err != nil {
		t.Fatal(err)
	}
	if result.Outputs["rendered"] != "hello wuko" || result.Outputs["secret"] != "secret-value" || result.Outputs["count"] != json.Number("7") {
		t.Fatalf("ready result = %#v", result)
	}
	if result.Outputs["decorated"] != json.Number("11") {
		t.Fatalf("decorated = %#v", result.Outputs["decorated"])
	}
	if output := call.AwaitEvent("stdout"); string(output.Data) != "service started\n" {
		t.Fatalf("stdout = %q", output.Data)
	}
	if started := <-fixture.started; started != "one" {
		t.Fatalf("started = %q", started)
	}
	call.Cancel()
	if response := call.Await(); response.Error != nil {
		t.Fatalf("run failed: %v", response.Error)
	}

	cleanup := host.CallStep(sdktest.StepRequest{
		Operation: sdktest.StepCleanup, Type: "fixture.server", With: config,
		Result: sdk.Result{Outputs: map[string]any{"count": 12}},
	}).Await()
	if cleanup.Error != nil {
		t.Fatal(cleanup.Error)
	}
	cleaned := <-fixture.cleaned
	if cleaned.Outputs["count"] != int(12) {
		t.Fatalf("cleanup result = %#v", cleaned)
	}
	if fixture.builds.Load() != 4 || fixture.validates.Load() != 1 || fixture.cleanups.Load() != 1 {
		t.Fatalf("counts: builds=%d validates=%d cleanups=%d", fixture.builds.Load(), fixture.validates.Load(), fixture.cleanups.Load())
	}
}

func TestRegisterServiceAdvertisesCleanupExplicitly(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			fixture := newFixture()
			host := startFixture(t, fixture, cleanup)
			if got := host.Initialization().Steps[0].Cleanup; got != cleanup {
				t.Fatalf("cleanup = %t, want %t", got, cleanup)
			}
		})
	}
}

func TestRegisterServiceRejectsInvalidLifecycle(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "runner failure", mode: "runner_failure", want: "startup failed"},
		{name: "missing registration", mode: "missing", want: "did not register managed work"},
		{name: "policy mismatch", mode: "mismatch", want: "policy changed"},
		{name: "duplicate registration", mode: "duplicate", want: "more than once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture()
			host := startFixture(t, fixture, false)
			response := host.CallStep(sdktest.StepRequest{
				Type: "fixture.server", With: testConfig(map[string]any{"mode": test.mode}), Context: testStepContext("server"),
			}).Await()
			if response.Error == nil || response.Error.Code != "step_failed" || !strings.Contains(response.Error.Message, test.want) {
				t.Fatalf("response error = %+v", response.Error)
			}
		})
	}
}

func TestRegisterServiceRejectsEmptyKind(t *testing.T) {
	fixture := newFixture()
	host := startFixture(t, fixture, false)
	response := host.CallStep(sdktest.StepRequest{
		Operation: sdktest.StepService, Type: "fixture.server", With: testConfig(map[string]any{"kind": " "}),
	}).Await()
	if response.Error == nil || response.Error.Code != "step_failed" || !strings.Contains(response.Error.Message, "empty kind") {
		t.Fatalf("response error = %+v", response.Error)
	}
}

func TestRegisterServicePreservesVerificationFailureOnCancellation(t *testing.T) {
	fixture := newFixture()
	host := startFixture(t, fixture, false)
	call := host.CallStep(sdktest.StepRequest{
		Type: "fixture.server", With: testConfig(map[string]any{"mode": "verification_failure"}), Context: testStepContext("server"),
	})
	_ = call.AwaitEvent("ready")
	<-fixture.started
	call.Cancel()
	response := call.Await()
	if response.Error == nil || response.Error.Code != "step_failed" || !strings.Contains(response.Error.Message, "verification failed") {
		t.Fatalf("response error = %+v", response.Error)
	}
}

func TestRegisterServiceRunsConcurrentInstances(t *testing.T) {
	fixture := newFixture()
	host := startFixture(t, fixture, false)
	calls := make(map[string]*sdktest.Call)
	for _, id := range []string{"first", "second"} {
		calls[id] = host.CallStep(sdktest.StepRequest{
			Type: "fixture.server", With: testConfig(map[string]any{"id": id}), Context: testStepContext(id),
		})
	}
	for id, call := range calls {
		var result sdk.Result
		if err := call.AwaitEvent("ready").DecodeResult(&result); err != nil {
			t.Fatal(err)
		}
		if result.Outputs["id"] != id {
			t.Fatalf("%s result = %#v", id, result)
		}
		call.Cancel()
	}
	for id, call := range calls {
		if response := call.Await(); response.Error != nil {
			t.Fatalf("%s failed: %v", id, response.Error)
		}
	}
	started := map[string]bool{<-fixture.started: true, <-fixture.started: true}
	if !started["first"] || !started["second"] {
		t.Fatalf("started = %#v", started)
	}
}

func TestFinishServiceJoinsReadinessAndShutdownFailures(t *testing.T) {
	serviceCtx, stop := context.WithCancel(context.Background())
	launcher := newServiceLauncher(serviceCtx, stop, "fixture", step.ServiceOptions{})
	verificationErr := errors.New("verification failed")
	if err := launcher.StartService("server", "fixture", step.ServiceOptions{}, func(ctx context.Context) error {
		<-ctx.Done()
		return errors.Join(ctx.Err(), verificationErr)
	}); err != nil {
		t.Fatal(err)
	}
	readyErr := errors.New("sending readiness")
	_, err := finishService(t.Context(), launcher, sdk.Result{}, func(sdk.Result) error { return readyErr })
	if !errors.Is(err, readyErr) || !errors.Is(err, verificationErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestNormalizeValuePreservesNumericMeaningAndInputs(t *testing.T) {
	source := map[string]any{
		"integer": json.Number("42"), "unsigned": json.Number("18446744073709551615"),
		"overflow": json.Number("18446744073709551616"), "decimal": json.Number("1.25"),
		"items": []any{json.Number("2")},
	}
	result := normalizeMap(source)
	if result["integer"] != int(42) || result["unsigned"] != uint64(18446744073709551615) || result["decimal"] != 1.25 {
		t.Fatalf("result = %#v", result)
	}
	if result["overflow"] != json.Number("18446744073709551616") || result["items"].([]any)[0] != int(2) {
		t.Fatalf("result = %#v", result)
	}
	if source["integer"] != json.Number("42") || source["items"].([]any)[0] != json.Number("2") {
		t.Fatalf("source was mutated: %#v", source)
	}
}

func TestRegisterServiceValidatesRegistrationAndRunner(t *testing.T) {
	plugin, err := sdk.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterService(plugin, "missing", ServiceRegistration{}); err == nil || !strings.Contains(err.Error(), "requires a builder") {
		t.Fatalf("nil builder error = %v", err)
	}
	notService := func(map[string]any) (step.Runner, error) {
		return runnerFunc(func(context.Context, step.Request) (step.Result, error) { return step.Result{}, nil }), nil
	}
	if err := RegisterService(plugin, "ordinary", ServiceRegistration{Builder: notService}); err != nil {
		t.Fatal(err)
	}
	host := sdktest.Start(t, plugin)
	response := host.CallStep(sdktest.StepRequest{Operation: sdktest.StepService, Type: "fixture.ordinary"}).Await()
	if response.Error == nil || !strings.Contains(response.Error.Message, "does not implement") {
		t.Fatalf("response error = %+v", response.Error)
	}
}

type runnerFunc func(context.Context, step.Request) (step.Result, error)

func (run runnerFunc) Run(ctx context.Context, request step.Request) (step.Result, error) {
	return run(ctx, request)
}

func TestCleanupRequiresCleanerWhenDeclared(t *testing.T) {
	plugin, err := sdk.New("fixture")
	if err != nil {
		t.Fatal(err)
	}
	builder := func(map[string]any) (step.Runner, error) { return serviceRunnerFunc{}, nil }
	if err := RegisterService(plugin, "server", ServiceRegistration{Builder: builder, Cleanup: true}); err != nil {
		t.Fatal(err)
	}
	host := sdktest.Start(t, plugin)
	response := host.CallStep(sdktest.StepRequest{Operation: sdktest.StepCleanup, Type: "fixture.server"}).Await()
	if response.Error == nil || response.Error.Code != "step_failed" || !strings.Contains(response.Error.Message, "step.Cleaner") {
		t.Fatalf("response error = %+v", response.Error)
	}
}

type serviceRunnerFunc struct{}

func (serviceRunnerFunc) Run(context.Context, step.Request) (step.Result, error) {
	return step.Result{}, nil
}

func (serviceRunnerFunc) ServiceOptions() (string, step.ServiceOptions) {
	return "fixture", step.ServiceOptions{}
}

func TestServiceLauncherRejectsNilAndPreservesDetachedContext(t *testing.T) {
	type key struct{}
	requestCtx, cancelRequest := context.WithCancel(context.WithValue(t.Context(), key{}, "value"))
	serviceCtx, stop := context.WithCancel(context.WithoutCancel(requestCtx))
	launcher := newServiceLauncher(serviceCtx, stop, "fixture", step.ServiceOptions{})
	if err := launcher.StartService("server", "fixture", step.ServiceOptions{}, nil); err == nil || !strings.Contains(err.Error(), "nil managed work") {
		t.Fatalf("nil work error = %v", err)
	}
	seen := make(chan any, 1)
	if err := launcher.StartService("server", "fixture", step.ServiceOptions{}, func(ctx context.Context) error {
		seen <- ctx.Value(key{})
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	if value := <-seen; value != "value" {
		t.Fatalf("context value = %#v", value)
	}
	select {
	case err := <-launcher.done:
		t.Fatalf("service followed request cancellation: %v", err)
	default:
	}
	stop()
	if err := <-launcher.done; !errors.Is(err, context.Canceled) {
		t.Fatalf("service error = %v", err)
	}
}

func TestPluginErrorClassifiesJoinedCancellation(t *testing.T) {
	if err := pluginError(fmt.Errorf("wrapped: %w", context.Canceled)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	} else {
		var coded *sdk.Error
		if errors.As(err, &coded) {
			t.Fatalf("pure cancellation was coded: %v", err)
		}
	}
	err := pluginError(errors.Join(context.Canceled, errors.New("verification failed")))
	var coded *sdk.Error
	if !errors.As(err, &coded) || coded.Code != "step_failed" {
		t.Fatalf("joined error = %v", err)
	}
	if serviceAbortOnly(errors.Join(step.ErrServiceAborted, errors.New("verification failed"))) {
		t.Fatal("serviceAbortOnly discarded a verification failure")
	}
}

func TestConcurrentRegistrationIsRejectedOnce(t *testing.T) {
	serviceCtx, stop := context.WithCancel(t.Context())
	defer stop()
	launcher := newServiceLauncher(serviceCtx, stop, "fixture", step.ServiceOptions{})
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			err := launcher.StartService("server", "fixture", step.ServiceOptions{}, func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
			if err == nil {
				successes.Add(1)
			}
		})
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful registrations = %d", successes.Load())
	}
	stop()
	<-launcher.done
}

func TestFinishServiceReportsEarlyExitAfterReadiness(t *testing.T) {
	serviceFailure := errors.New("service failed")
	tests := []struct {
		name string
		exit error
		want error
	}{
		{name: "success", exit: nil, want: nil},
		{name: "failure", exit: serviceFailure, want: serviceFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serviceCtx, stop := context.WithCancel(context.Background())
			launcher := newServiceLauncher(serviceCtx, stop, "fixture", step.ServiceOptions{})
			exited := make(chan struct{})
			if err := launcher.StartService("server", "fixture", step.ServiceOptions{}, func(context.Context) error {
				defer close(exited)
				return test.exit
			}); err != nil {
				t.Fatal(err)
			}
			<-exited
			readied := false
			_, err := finishService(t.Context(), launcher, sdk.Result{}, func(sdk.Result) error {
				readied = true
				return nil
			})
			if !readied {
				t.Fatal("readiness was not reported for work that exited before the check")
			}
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceLauncherContainsPanics(t *testing.T) {
	serviceCtx, stop := context.WithCancel(context.Background())
	launcher := newServiceLauncher(serviceCtx, stop, "fixture", step.ServiceOptions{})
	if err := launcher.StartService("server", "fixture", step.ServiceOptions{}, func(context.Context) error {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if err := launcher.stopAndWait(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginErrorPreservesExistingCode(t *testing.T) {
	err := pluginError(fmt.Errorf("rendering: %w", sdk.Coded("template_invalid", errors.New("bad"))))
	var coded *sdk.Error
	if !errors.As(err, &coded) || coded.Code != "template_invalid" {
		t.Fatalf("error = %#v", err)
	}
}

type renderHost struct {
	sdk.Host
	rendered atomic.Int32
}

func (host *renderHost) ValidateTemplate(context.Context, string) error { return nil }

func (host *renderHost) RenderTemplate(_ context.Context, content string, _ map[string]any) (string, error) {
	host.rendered.Add(1)
	return content, nil
}

func TestWithoutSecretsFailsClosedWhenHostRenderingReachesSecrets(t *testing.T) {
	tests := []struct {
		name             string
		secretsReachable bool
		wantRender       bool
	}{
		{name: "host already denies secrets", secretsReachable: false, wantRender: true},
		{name: "host function call declared", secretsReachable: true, wantRender: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &renderHost{}
			request := stepRequest(context.Background(), testStepContext("render"), host, test.secretsReachable, nil, nil, nil)
			renderer := request.TemplateRenderer.(step.DataTemplateRenderer).WithoutSecrets()
			if err := renderer.ValidateContent("{{ .vars.name }}"); err != nil {
				t.Fatalf("ValidateContent() error = %v", err)
			}
			renders := []func() (string, error){
				func() (string, error) { return renderer.Render("x") },
				func() (string, error) { return renderer.RenderContent("x") },
				func() (string, error) { return renderer.RenderWith("x", nil) },
				func() (string, error) { return renderer.RenderContentWith("x", nil) },
				func() (string, error) { return renderer.Snapshot().RenderContent("x") },
				func() (string, error) { return renderer.WithoutSecrets().RenderContent("x") },
			}
			for index, render := range renders {
				_, err := render()
				if test.wantRender && err != nil {
					t.Fatalf("render %d error = %v", index, err)
				}
				if !test.wantRender && !errors.Is(err, errSecretFreeRendering) {
					t.Fatalf("render %d error = %v, want %v", index, err, errSecretFreeRendering)
				}
			}
			if got, want := host.rendered.Load() > 0, test.wantRender; got != want {
				t.Fatalf("host rendered = %v, want %v", got, want)
			}
		})
	}
}

func TestRegisterServiceRejectsInvalidStepOptions(t *testing.T) {
	plugin, err := sdk.New("acme")
	if err != nil {
		t.Fatal(err)
	}
	err = RegisterService(plugin, "server", ServiceRegistration{Builder: fixtureBuilder(newFixture())}, sdk.WithHostCallbacks("host.unknown"))
	if err == nil || !strings.Contains(err.Error(), "unsupported host callback") {
		t.Fatalf("RegisterService() error = %v", err)
	}
}
