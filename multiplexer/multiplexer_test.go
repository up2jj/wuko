package multiplexer

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/up2jj/wuko/process"
)

type commandCall struct {
	command string
	args    []string
}

type fakeExecutor struct {
	calls   []commandCall
	results []process.Result
	errors  []error
}

func (executor *fakeExecutor) Run(_ context.Context, options process.Options) (process.Result, error) {
	executor.calls = append(executor.calls, commandCall{command: options.Command, args: slices.Clone(options.Args)})
	index := len(executor.calls) - 1
	var result process.Result
	if index < len(executor.results) {
		result = executor.results[index]
	}
	if index < len(executor.errors) {
		return result, executor.errors[index]
	}
	return result, nil
}

func TestControllerDetectsInnermostProviderAndAllowsOverride(t *testing.T) {
	environment := map[string]string{
		"TMUX": "socket", "TMUX_PANE": "%3",
		"HERDR_ENV": "1", "HERDR_PANE_ID": "pane:herdr",
		"CMUX_SURFACE_ID": "surface:4", "CMUX_WORKSPACE_ID": "workspace:2",
	}
	controller := New(&fakeExecutor{})
	target, active := controller.Detect(environment, ProviderAuto)
	if !active || target.Provider != ProviderTmux || target.ID != "%3" {
		t.Fatalf("auto target = %#v, %t", target, active)
	}
	target, active = controller.Detect(environment, ProviderCmux)
	if !active || target.Provider != ProviderCmux || target.ID != "surface:4" || target.Workspace != "workspace:2" {
		t.Fatalf("cmux target = %#v, %t", target, active)
	}
	if _, active := controller.Detect(map[string]string{}, ProviderAuto); active {
		t.Fatal("empty environment detected a multiplexer")
	}
}

func TestControllerReturnsInactiveWithoutRunningACommand(t *testing.T) {
	executor := &fakeExecutor{}
	result, err := New(executor).Execute(t.Context(), nil, Request{Operation: OperationTitle, Title: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Active || result.Changed || result.Operation != OperationTitle || len(executor.calls) != 0 {
		t.Fatalf("result = %#v, calls = %#v", result, executor.calls)
	}
}

func TestTmuxAdapterUsesTargetPaneAndNormalizesZoom(t *testing.T) {
	environment := map[string]string{"TMUX": "socket", "TMUX_PANE": "%7"}
	executor := &fakeExecutor{results: []process.Result{{}, {Stdout: "0\n"}, {}}}
	controller := New(executor)
	if _, err := controller.Execute(t.Context(), environment, Request{Operation: OperationTitle, Title: "tests"}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Execute(t.Context(), environment, Request{Operation: OperationZoom, Mode: "on"}); err != nil {
		t.Fatal(err)
	}
	want := []commandCall{
		{command: "tmux", args: []string{"select-pane", "-t", "%7", "-T", "tests"}},
		{command: "tmux", args: []string{"display-message", "-p", "-t", "%7", "#{window_zoomed_flag}"}},
		{command: "tmux", args: []string{"resize-pane", "-t", "%7", "-Z"}},
	}
	if !callsEqual(executor.calls, want) {
		t.Fatalf("calls = %#v, want %#v", executor.calls, want)
	}
}

func TestHerdrAdapterBuildsDeterministicMetadataArguments(t *testing.T) {
	environment := map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": "pane-9"}
	executor := &fakeExecutor{}
	request := Request{
		Operation: OperationMetadata, Source: "wuko.test", Title: "Build", DisplayAgent: "Wuko",
		StateLabels: map[string]string{"working": "Running", "done": "Complete"},
		Tokens:      map[string]string{"stage": "test", "branch": "main"}, ClearTokens: []string{"old"}, TTLMilliseconds: 5000,
	}
	if _, err := New(executor).Execute(t.Context(), environment, request); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pane", "report-metadata", "pane-9", "--source", "wuko.test", "--title", "Build", "--display-agent", "Wuko",
		"--state-label", "done=Complete", "--state-label", "working=Running",
		"--token", "branch=main", "--token", "stage=test", "--clear-token", "old", "--ttl-ms", "5000",
	}
	if len(executor.calls) != 1 || executor.calls[0].command != "herdr" || !slices.Equal(executor.calls[0].args, want) {
		t.Fatalf("calls = %#v", executor.calls)
	}
}

func TestCmuxAdapterUsesLegacyTitleFallbackAndAdvertisedCapabilities(t *testing.T) {
	environment := map[string]string{"CMUX_SURFACE_ID": "surface:4", "CMUX_WORKSPACE_ID": "workspace:2"}
	executor := &fakeExecutor{results: []process.Result{{Stdout: "Commands:\n  rename-tab <title>\n  notify --title <text>\n  set-status <key> <value>\n  clear-status <key>\n  set-progress <value>\n  clear-progress\n  log <message>\n  clear-log\n"}, {}}}
	result, err := New(executor).Execute(t.Context(), environment, Request{Operation: OperationTitle, Title: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Active || !result.Changed || result.Target != "surface:4" {
		t.Fatalf("result = %#v", result)
	}
	want := []commandCall{
		{command: "cmux", args: []string{"--help"}},
		{command: "cmux", args: []string{"rename-tab", "--surface", "surface:4", "--title", "build"}},
	}
	if !callsEqual(executor.calls, want) {
		t.Fatalf("calls = %#v, want %#v", executor.calls, want)
	}
}

func TestCmuxAdapterReportsUnsupportedInstalledCapability(t *testing.T) {
	environment := map[string]string{"CMUX_SURFACE_ID": "surface:4"}
	executor := &fakeExecutor{results: []process.Result{{Stdout: "Commands:\n  rename-tab <title>\n  notify --title <text>\n"}}}
	_, err := New(executor).Execute(t.Context(), environment, Request{Operation: OperationProgress, Progress: 0.5})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Provider != ProviderCmux || unsupported.Operation != OperationProgress {
		t.Fatalf("error = %v", err)
	}
}

func TestAdaptersKeepFlagShapedDisplayTextAsOperands(t *testing.T) {
	cmuxHelp := process.Result{Stdout: "Commands:\n  rename-tab <title>\n  notify --title <text>\n  set-status <key> <value>\n  clear-status <key>\n  set-progress <value>\n  clear-progress\n  log <message>\n  clear-log\n"}
	tests := []struct {
		name        string
		environment map[string]string
		results     []process.Result
		request     Request
		want        []string
	}{
		{
			name:        "herdr title",
			environment: map[string]string{"HERDR_PANE_ID": "pane-9"},
			request:     Request{Operation: OperationTitle, Title: "--clear"},
			want:        []string{"pane", "rename", "pane-9", "--", "--clear"},
		},
		{
			name:        "herdr notify",
			environment: map[string]string{"HERDR_PANE_ID": "pane-9"},
			request:     Request{Operation: OperationNotify, Title: "--body", Body: "deploy finished"},
			want:        []string{"notification", "show", "--body", "--body", "deploy finished"},
		},
		{
			name:        "tmux notify",
			environment: map[string]string{"TMUX": "socket", "TMUX_PANE": "%7"},
			request:     Request{Operation: OperationNotify, Title: "-p"},
			want:        []string{"display-message", "-t", "%7", "--", "-p"},
		},
		{
			name:        "cmux status",
			environment: map[string]string{"CMUX_SURFACE_ID": "surface:4"},
			results:     []process.Result{cmuxHelp, {}},
			request:     Request{Operation: OperationStatus, Key: "--icon", Value: "--color", Icon: "rocket"},
			want:        []string{"set-status", "--icon", "rocket", "--", "--icon", "--color"},
		},
		{
			name:        "cmux clear status",
			environment: map[string]string{"CMUX_SURFACE_ID": "surface:4"},
			results:     []process.Result{cmuxHelp, {}},
			request:     Request{Operation: OperationClearStatus, Key: "--all"},
			want:        []string{"clear-status", "--", "--all"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &fakeExecutor{results: test.results}
			if _, err := New(executor).Execute(t.Context(), test.environment, test.request); err != nil {
				t.Fatal(err)
			}
			call := executor.calls[len(executor.calls)-1]
			if !slices.Equal(call.args, test.want) {
				t.Fatalf("args = %#v, want %#v", call.args, test.want)
			}
		})
	}
}

func TestCommandFailureIncludesCapturedDetail(t *testing.T) {
	environment := map[string]string{"TMUX": "socket", "TMUX_PANE": "%1"}
	executor := &fakeExecutor{results: []process.Result{{Stderr: "server unavailable\n"}}, errors: []error{errors.New("exit 1")}}
	_, err := New(executor).Execute(t.Context(), environment, Request{Operation: OperationClearTitle})
	if err == nil || !strings.Contains(err.Error(), "server unavailable") || !strings.Contains(err.Error(), "exit 1") {
		t.Fatalf("error = %v", err)
	}
}

func callsEqual(left, right []commandCall) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].command != right[index].command || !slices.Equal(left[index].args, right[index].args) {
			return false
		}
	}
	return true
}
