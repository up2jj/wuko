package scriptsdk_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/scriptsdk"
	"github.com/up2jj/wuko/step"
)

// These opt-in benchmarks use the actual Node/Bash adapters and built Wuko
// executable (needed by Bash's fallback client), rather than a test substitute.
// Run through `just bench-script-sdk`; compilation is outside measured time.
func BenchmarkBridgeLifecycle(b *testing.B) {
	for _, size := range []int{1 << 10, 100 << 10, 1 << 20} {
		for _, adapter := range []string{"none", "node", "bash"} {
			b.Run(fmt.Sprintf("%s/%dKiB", adapter, size>>10), func(b *testing.B) {
				executable := benchmarkExecutable(b)
				request := benchmarkRequest(size)
				for b.Loop() {
					bridge, _ := benchmarkOpen(b, request, adapter, executable)
					if err := bridge.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkScriptInvocation(b *testing.B) {
	executable := benchmarkExecutable(b)
	for _, adapter := range []string{"node", "bash"} {
		runtimePath := benchmarkRuntime(b, adapter)
		for _, mode := range []string{"plain", "sdk_no_calls", "sdk_one_output"} {
			b.Run(adapter+"/"+mode, func(b *testing.B) {
				request := benchmarkRequest(1 << 10)
				source := ":"
				if adapter == "node" {
					source = "void 0;"
				}
				if mode == "sdk_one_output" {
					if adapter == "node" {
						source = "await wuko.output('result', 'hello');"
					} else {
						source = "wuko_output result hello"
					}
				}
				for b.Loop() {
					environment := maps.Clone(request.Env)
					var bridge *scriptsdk.Bridge
					if mode != "plain" {
						bridge, environment = benchmarkOpen(b, request, adapter, executable)
					}
					err := benchmarkRun(b.Context(), runtimePath, adapter, source, environment)
					if bridge != nil {
						if closeErr := bridge.Close(); closeErr != nil {
							b.Fatal(closeErr)
						}
						if mode == "sdk_one_output" && bridge.Result().Outputs["result"] != "hello" {
							b.Fatal("SDK output was not staged")
						}
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkSDKCall(b *testing.B) {
	executable := benchmarkExecutable(b)
	const calls = 200
	for _, adapter := range []string{"node", "bash"} {
		runtimePath := benchmarkRuntime(b, adapter)
		methods := []string{"output", "set_var", "helper"}
		if adapter == "bash" {
			methods = append(methods, "get")
		}
		for _, method := range methods {
			b.Run(adapter+"/"+method, func(b *testing.B) {
				request := benchmarkRequest(1 << 10)
				epoch := time.Now()
				var started, elapsed atomic.Int64
				request.Helpers = helper.Set{
					"benchmark_start": func(context.Context, []any) (any, error) {
						started.Store(time.Since(epoch).Nanoseconds())
						return nil, nil
					},
					"benchmark_end": func(context.Context, []any) (any, error) {
						elapsed.Store(time.Since(epoch).Nanoseconds() - started.Load())
						return nil, nil
					},
				}
				source := benchmarkCallSource(adapter, method, calls)
				var total int64
				for b.Loop() {
					bridge, environment := benchmarkOpen(b, request, adapter, executable)
					err := benchmarkRun(b.Context(), runtimePath, adapter, source, environment)
					if closeErr := bridge.Close(); closeErr != nil {
						b.Fatal(closeErr)
					}
					if err != nil {
						b.Fatal(err)
					}
					if elapsed.Load() <= 0 {
						b.Fatal("script did not finish the timed batch")
					}
					if method == "output" && bridge.Result().Outputs["result"] != "hello" {
						b.Fatal("SDK output was not staged")
					}
					if method == "set_var" && bridge.Result().Variables["result"] != "hello" {
						b.Fatal("SDK variable was not staged")
					}
					total += elapsed.Swap(0)
				}
				// Host timestamps exclude runtime/bridge startup, warmup and cleanup.
				// The interval includes the start response and end request (one extra
				// round trip per batch), intentionally left unsubtracted.
				b.ReportMetric(float64(total)/float64(b.N*calls), "ns/call")
			})
		}
	}
}

func benchmarkCallSource(adapter, method string, calls int) string {
	if adapter == "node" {
		operation := map[string]string{
			"output":  "await wuko.output('result', 'hello');",
			"set_var": "await wuko.setVar('result', 'hello');",
			"helper":  "if (await wuko.helpers.upper('hello') !== 'HELLO') throw new Error('bad helper');",
		}[method]
		return fmt.Sprintf("for (let i = 0; i < 10; i++) { %s }\nawait wuko.helpers.benchmark_start();\nfor (let i = 0; i < %d; i++) { %s }\nawait wuko.helpers.benchmark_end();", operation, calls, operation)
	}
	operation := map[string]string{
		"output":  "wuko_output result hello",
		"set_var": "wuko_set_var result hello",
		"helper":  "wuko_helper upper hello >/dev/null",
		"get":     "wuko_get vars.label >/dev/null",
	}[method]
	return fmt.Sprintf("set -e\nfor ((i=0; i<10; i++)); do %s; done\nwuko_helper benchmark_start >/dev/null\nfor ((i=0; i<%d; i++)); do %s; done\nwuko_helper benchmark_end >/dev/null", operation, calls, operation)
}

func benchmarkRequest(size int) step.Request {
	return step.Request{
		StepID: "benchmark", WorkflowName: "script-sdk-benchmark", OperationID: "benchmark",
		Vars:     map[string]any{"payload": strings.Repeat("x", size), "label": "hello"},
		Env:      map[string]string{"PATH": os.Getenv("PATH")},
		Bindings: map[string]any{"foreach": map[string]any{"item": "api", "index": 0}},
	}
}

func benchmarkOpen(b *testing.B, request step.Request, adapter, executable string) (*scriptsdk.Bridge, map[string]string) {
	b.Helper()
	bridge, err := scriptsdk.Open(b.Context(), request, nil)
	if err != nil {
		b.Fatal(err)
	}
	environment := maps.Clone(request.Env)
	switch adapter {
	case "node":
		err = bridge.InjectNode(environment)
	case "bash":
		err = bridge.InjectBash(environment, executable)
	}
	if err != nil {
		_ = bridge.Close()
		b.Fatal(err)
	}
	return bridge, environment
}

func benchmarkRun(ctx context.Context, runtimePath, adapter, source string, environment map[string]string) error {
	args := []string{"--noprofile", "--norc", "-c", source}
	if adapter == "node" {
		args = []string{"--input-type=module", "-e", source}
	}
	command := exec.CommandContext(ctx, runtimePath, args...)
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}
	command.Stdout = io.Discard
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", adapter, err, stderr.String())
	}
	return nil
}

func benchmarkExecutable(b *testing.B) string {
	b.Helper()
	path := os.Getenv("WUKO_BENCH_EXECUTABLE")
	if path == "" {
		b.Skip("run just bench-script-sdk to provide the built Wuko executable")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		b.Fatal(err)
	}
	return path
}

func benchmarkRuntime(b *testing.B, adapter string) string {
	b.Helper()
	path := os.Getenv("WUKO_BENCH_" + strings.ToUpper(adapter))
	if path == "" {
		var err error
		path, err = exec.LookPath(adapter)
		if err != nil {
			b.Skipf("%s unavailable: %v", adapter, err)
		}
	}
	if adapter == "node" {
		// Resolve version-manager shims once, outside measured time.
		output, err := exec.Command(path, "-p", "process.versions.node.split('.')[0] + '\\n' + process.execPath").Output()
		if err != nil {
			b.Fatal(err)
		}
		parts := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)
		major, _ := strconv.Atoi(parts[0])
		if len(parts) != 2 || major < 22 {
			b.Skip("Node.js 22+ required")
		}
		return parts[1]
	}
	return path
}
