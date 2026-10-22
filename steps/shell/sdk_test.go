package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
)

func sdkNode(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatal("Node.js is required for SDK integration tests")
		}
		t.Skip("Node.js is unavailable")
	}
	resolved, err := exec.Command(path, "-p", `process.versions.node.split('.')[0] + '\n' + process.execPath`).Output()
	if err != nil {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatalf("resolving Node runtime: %v", err)
		}
		t.Skipf("Node runtime is unavailable: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(resolved)), "\n", 2)
	major, _ := strconv.Atoi(parts[0])
	if len(parts) != 2 || major < 22 {
		if os.Getenv("WUKO_NODE_INTEGRATION") == "1" {
			t.Fatal("SDK integration tests require Node.js 22+")
		}
		t.Skip("SDK integration tests require Node.js 22+")
	}
	return parts[1]
}

func TestNodeSDKCommonJSAndESM(t *testing.T) {
	node := sdkNode(t)
	for _, extension := range []string{"cjs", "mjs"} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			source := `
const task = async () => {
  const fs = await import('node:fs');
  console.log('ordinary stdout');
  console.error('ordinary stderr');
  const value = await wuko.helpers.upper(wuko.args.title);
  const token = await wuko.secret('test:token');
  await wuko.output('value', {title: value, items: wuko.args.items, token, flag: false, empty: null});
  await wuko.output('descriptor', process.env.WUKO_SDK_DESCRIPTOR);
  await wuko.output('attempt', wuko.attempt.number);
  await wuko.output('env', wuko.env.CUSTOM);
  await wuko.output('preload', globalThis.existingPreload);
  await wuko.output('frozen', Object.isFrozen(wuko.vars));
  await wuko.setVar('release', true);
};
task().catch(error => { console.error(error); process.exitCode = 1; });
`
			file := filepath.Join(dir, "script with spaces."+extension)
			if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			preload := filepath.Join(dir, "existing preload.cjs")
			if err := os.WriteFile(preload, []byte(`globalThis.existingPreload = true; if (!globalThis.wuko) throw new Error('SDK must preload first');`), 0o600); err != nil {
				t.Fatal(err)
			}
			runner, err := New(map[string]any{
				"sdk": "node", "command": node, "args": []any{file},
				"sdk_args": map[string]any{"title": "release", "items": map[string]any{"expr": "vars.inventory"}},
				"env":      map[string]any{"CUSTOM": "step", "NODE_OPTIONS": `--require "` + preload + `"`},
			})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			result, err := runner.Run(t.Context(), step.Request{
				RunDir: dir, Attempt: 2, MaxAttempts: 3,
				Vars:   map[string]any{"inventory": []any{map[string]any{"count": 3}}},
				Secret: func(reference string) (string, error) { return "resolved:" + reference, nil },
				Stdout: &stdout, Stderr: &stderr,
			})
			if err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}
			data := result.Outputs["data"].(map[string]any)
			value := data["value"].(map[string]any)
			if value["title"] != "RELEASE" || value["token"] != "resolved:test:token" || value["flag"] != false || value["empty"] != nil {
				t.Fatalf("value = %#v", value)
			}
			if value["items"].([]any)[0].(map[string]any)["count"] != int64(3) {
				t.Fatalf("typed argument lost: %#v", value)
			}
			if data["attempt"] != int64(2) || data["env"] != "step" || data["preload"] != true || data["frozen"] != true || result.Variables["release"] != true {
				t.Fatalf("result = %#v", result)
			}
			if result.Outputs["stdout"] != "ordinary stdout\n" || stdout.String() != "ordinary stdout\n" || stderr.String() != "ordinary stderr\n" {
				t.Fatalf("streams: %#v %q %q", result.Outputs, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(data["descriptor"].(string)); !os.IsNotExist(err) {
				t.Fatalf("descriptor was not cleaned up: %v", err)
			}
		})
	}
}

func TestNodeSDKInlineAndInheritedChild(t *testing.T) {
	node := sdkNode(t)
	runner, err := New(map[string]any{
		"sdk": "node", "script": `"$1" --input-type=module <<'JS'
import { spawn } from 'node:child_process';
const child = spawn(process.execPath, ['--input-type=module', '-e', 'await wuko.output("child", true)'], {stdio: 'inherit'});
await new Promise((resolve, reject) => { child.on('error', reject); child.on('exit', code => code === 0 ? resolve() : reject(new Error('child failed'))); });
await wuko.output('parent', true);
JS`, "args": []any{node},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	data := result.Outputs["data"].(map[string]any)
	if data["parent"] != true || data["child"] != true {
		t.Fatalf("data = %#v", data)
	}
}

func TestNodeSDKRejectsUnsupportedValues(t *testing.T) {
	node := sdkNode(t)
	runner, err := New(map[string]any{"sdk": "node", "command": node, "args": []any{"--input-type=module", "-e", `
const cycle = {}; cycle.self = cycle;
for (const value of [undefined, NaN, Infinity, 9007199254740992, 1n, new Date(), () => {}, cycle, [undefined], {nested: undefined}]) {
  try { await wuko.output('invalid', value); throw new Error('unexpected success'); }
  catch (error) { if (!(error instanceof TypeError)) throw error; }
}
try { await wuko.helpers.doesNotExist(); throw new Error('unexpected success'); }
catch (error) { if (error.code !== 'host_error') throw error; }
await Promise.all(Array.from({length: 12}, (_, i) => wuko.output('item'+i, i)));
await wuko.output('__proto__', {safe: true});
await wuko.output('done', true);
`}})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	data := result.Outputs["data"].(map[string]any)
	if _, exists := data["invalid"]; exists {
		t.Fatal("invalid output was stored")
	}
	if data["done"] != true || data["item11"] != int64(11) || data["__proto__"].(map[string]any)["safe"] != true {
		t.Fatalf("data = %#v", data)
	}
}

func TestNodeSDKCancellationStopsHostCalls(t *testing.T) {
	node := sdkNode(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	runner, err := New(map[string]any{"sdk": "node", "command": node, "args": []any{"--input-type=module", "-e", `await wuko.helpers.wait()`}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(ctx, step.Request{RunDir: t.TempDir(), Helpers: helper.Set{
		"wait": func(ctx context.Context, _ []any) (any, error) {
			cancel()
			<-ctx.Done()
			defer close(finished)
			return nil, ctx.Err()
		},
	}})
	if err == nil {
		t.Fatal("canceled process succeeded")
	}
	select {
	case <-finished:
	default:
		t.Fatal("host call outlived shell step")
	}
}

func TestNodeSDKConfigurationAndOutputContracts(t *testing.T) {
	for _, raw := range []map[string]any{
		{"command": "node", "sdk": "python"},
		{"command": "node", "sdk_args": map[string]any{}},
		{"command": "node", "sdk": "node", "elevated": true},
		{"command": "node", "sdk": "node", "user": "root"},
		{"command": "node", "sdk": "node", "sdk_args": map[string]any{"bad": map[string]any{"expr": " "}}},
	} {
		if _, err := New(raw); err == nil {
			t.Fatalf("accepted invalid configuration: %#v", raw)
		}
	}
	runner, err := New(map[string]any{"command": "node", "sdk": "node"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.(step.Validator).Validate(t.Context(), step.Request{InsideExecutor: true}); err == nil || !strings.Contains(err.Error(), "executor") {
		t.Fatalf("executor restriction: %v", err)
	}
	registry := step.NewRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		raw := map[string]any{"command": "node"}
		if enabled {
			raw["sdk"] = "node"
		}
		schema, err := registry.OutputSchemaForConfig(t.Context(), "shell", raw)
		if err != nil {
			t.Fatal(err)
		}
		_, exists := schema.Fields["data"]
		if exists != enabled {
			t.Fatalf("schema data field = %v, SDK enabled = %v", exists, enabled)
		}
	}
}

func TestNodeSDKUsesExistingProjectDependencies(t *testing.T) {
	node := sdkNode(t)
	dir := t.TempDir()
	dependency := filepath.Join(dir, "node_modules", "fixture-dependency")
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":      `{"name":"fixture","type":"module","dependencies":{"fixture-dependency":"1.0.0"}}`,
		"package-lock.json": `{"name":"fixture","lockfileVersion":3,"packages":{"node_modules/fixture-dependency":{"version":"1.0.0"}}}`,
		"node_modules/fixture-dependency/package.json": `{"name":"fixture-dependency","version":"1.0.0","type":"module","exports":"./index.js"}`,
		"node_modules/fixture-dependency/index.js":     `export default value => 'dependency:' + value;`,
		"script.js": `import transform from 'fixture-dependency'; await wuko.output('value', transform(wuko.args.name));`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := New(map[string]any{"sdk": "node", "command": node, "args": []any{"script.js"}, "sdk_args": map[string]any{"name": "SDK"}})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{RunDir: dir, Stderr: &stderr})
	if err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	if result.Outputs["data"].(map[string]any)["value"] != "dependency:SDK" {
		t.Fatalf("result = %#v", result)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(content) != files[name] {
			t.Fatalf("%s changed: %v", name, err)
		}
	}
}

func TestNodeSDKPreservesStdinCaptureAndAllowedExitCodes(t *testing.T) {
	node := sdkNode(t)
	runner, err := New(map[string]any{
		"sdk": "node", "command": node,
		"args": []any{"--input-type=module", "-e", `
import {readFileSync} from 'node:fs';
await wuko.output('input', readFileSync(0, 'utf8'));
process.stdout.write('123456789'); process.stderr.write('abcdefghi');
process.exitCode = 7;
`},
		"stdin": "payload", "stdout": "capture", "stderr": "capture", "capture_limit": "4B", "allowed_exit_codes": []any{7},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir(), Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["exit_code"] != 7 || result.Outputs["stdout"] != "1234" || result.Outputs["stderr"] != "abcd" || result.Outputs["stdout_truncated"] != true || result.Outputs["stderr_truncated"] != true {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	if result.Outputs["data"].(map[string]any)["input"] != "payload" || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("streams = %#v %q %q", result.Outputs, stdout.String(), stderr.String())
	}
}

func TestNodeSDKAttemptsDoNotShareBridgeState(t *testing.T) {
	node := sdkNode(t)
	for i := range 4 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			runner, err := New(map[string]any{"sdk": "node", "command": node, "args": []any{"--input-type=module", "-e", `await wuko.output('value', wuko.args.value)`}, "sdk_args": map[string]any{"value": i}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outputs["data"].(map[string]any)["value"] != int64(i) {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}
