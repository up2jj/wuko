package lua

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/internal/steptest"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestLuaRequiresWorkflowRelativeModules(t *testing.T) {
	for _, entry := range []string{"inline", "file"} {
		for _, module := range []struct {
			path string
			name string
		}{
			{"deploy.lua", "deploy"},
			{"deploy/init.lua", "deploy"},
			{"lua/deploy.lua", "deploy"},
			{"lua/deploy/init.lua", "deploy"},
			{"tools/deploy.lua", "tools.deploy"},
			{"tools/deploy/init.lua", "tools.deploy"},
			{"lua/tools/deploy.lua", "tools.deploy"},
			{"lua/tools/deploy/init.lua", "tools.deploy"},
		} {
			t.Run(entry+"/"+module.path, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "workflow's modules")
				runDir := t.TempDir()
				testfs.Write(t, filepath.Join(dir, module.path), `return {value = "workflow"}`)
				testfs.Write(t, filepath.Join(runDir, module.path), `return {value = "run directory"}`)
				testfs.Write(t, filepath.Join(filepath.Join(dir, "scripts"), module.path), `return {value = "script directory"}`)
				source := fmt.Sprintf(`wuko.output("value", require(%q).value)`, module.name)
				config := map[string]any{"source": source}
				if entry == "file" {
					testfs.Write(t, filepath.Join(dir, "scripts/main.lua"), source)
					config = map[string]any{"file": "scripts/main.lua"}
				}
				runner := steptest.Build(t, New, config)
				result, err := runner.Run(t.Context(), step.Request{WorkflowDir: dir, RunDir: runDir})
				if err != nil {
					t.Fatal(err)
				}
				if got := result.Outputs["value"]; got != "workflow" {
					t.Fatalf("module value = %#v, want workflow", got)
				}
			})
		}
	}
}

func TestLuaModuleSearchPrecedenceAndFallback(t *testing.T) {
	locations := []string{"deploy.lua", "deploy/init.lua", "lua/deploy.lua", "lua/deploy/init.lua"}
	for first := 0; first <= len(locations); first++ {
		name := "LUA_PATH"
		if first < len(locations) {
			name = locations[first]
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			externalDir := t.TempDir()
			t.Setenv("LUA_PATH", filepath.Join(externalDir, "?.lua"))
			testfs.Write(t, filepath.Join(externalDir, "deploy.lua"), `return {value = "LUA_PATH"}`)
			testfs.Write(t, filepath.Join(externalDir, "fallback.lua"), `return {value = "fallback"}`)
			for _, location := range locations[first:] {
				testfs.Write(t, filepath.Join(dir, location), fmt.Sprintf(`return {value = %q}`, location))
			}
			runner := steptest.Build(t, New, map[string]any{"source": `
wuko.output("value", require("deploy").value)
wuko.output("fallback", require("fallback").value)
`})
			result, err := runner.Run(t.Context(), step.Request{WorkflowDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["value"]; got != name {
				t.Fatalf("module value = %#v, want %q", got, name)
			}
			if got := result.Outputs["fallback"]; got != "fallback" {
				t.Fatalf("fallback module = %#v", got)
			}
		})
	}
}

func TestLuaEmptyWorkflowDirPreservesModulePath(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "?.lua") + ";" + filepath.Join(dir, "?", "init.lua")
	t.Setenv("LUA_PATH", modulePath)
	testfs.Write(t, filepath.Join(dir, "deploy.lua"), `return {value = "external"}`)
	runner := steptest.Build(t, New, map[string]any{"source": `
wuko.output("path", package.path)
wuko.output("value", require("deploy").value)
`})
	result, err := runner.Run(t.Context(), step.Request{RunDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["path"]; got != modulePath {
		t.Fatalf("package.path = %#v, want %q", got, modulePath)
	}
	if got := result.Outputs["value"]; got != "external" {
		t.Fatalf("module value = %#v, want external", got)
	}
}

func TestLuaModuleCacheIsIsolatedPerExecution(t *testing.T) {
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "deploy.lua"), `return {count = 0}`)
	runner := steptest.Build(t, New, map[string]any{"source": `
local deploy = require("deploy")
deploy.count = deploy.count + 1
wuko.output("count", require("deploy").count)
`})
	for range 2 {
		result, err := runner.Run(t.Context(), step.Request{WorkflowDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Outputs["count"]; got != float64(1) {
			t.Fatalf("cached module count = %#v, want 1", got)
		}
	}
}

func TestLuaMissingModuleFailsAtRuntime(t *testing.T) {
	runner := steptest.Build(t, New, map[string]any{"source": `require("wuko_nonexistent_test_module")`})
	_, err := runner.Run(t.Context(), step.Request{WorkflowDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "wuko_nonexistent_test_module") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing module error = %v", err)
	}
}

func TestLuaValidationDoesNotLoadModules(t *testing.T) {
	for _, module := range []struct {
		name   string
		source string
	}{
		{name: "missing"},
		{name: "invalid", source: `this is not valid Lua`},
		{name: "side_effect", source: `wuko.fs.write("executed", "unexpected")`},
	} {
		t.Run(module.name, func(t *testing.T) {
			dir := t.TempDir()
			runDir := t.TempDir()
			if module.source != "" {
				testfs.Write(t, filepath.Join(dir, module.name+".lua"), module.source)
			}
			runner := steptest.Build(t, New, map[string]any{"source": fmt.Sprintf(`require(%q); error("entry script executed")`, module.name)})
			if err := runner.(step.Validator).Validate(t.Context(), step.Request{WorkflowDir: dir, RunDir: runDir}); err != nil {
				t.Fatalf("validation executed or loaded Lua: %v", err)
			}
			if _, err := os.Stat(filepath.Join(runDir, "executed")); !os.IsNotExist(err) {
				t.Fatalf("module side effect exists after validation: %v", err)
			}
		})
	}
}

func TestLuaBorrowedWorkflowDirDoesNotExposeCallerFiles(t *testing.T) {
	dir := t.TempDir()
	testfs.Write(t, filepath.Join(dir, "deploy.lua"), `return {value = "caller"}`)
	testfs.Write(t, filepath.Join(dir, "main.lua"), `wuko.output("value", "caller script")`)
	request := step.Request{WorkflowDir: dir, WorkflowDirBorrowed: true}

	runner := steptest.Build(t, New, map[string]any{"source": `wuko.output("ok", pcall(require, "deploy"))`})
	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["ok"]; got != false {
		t.Fatalf("require of caller module succeeded: %#v", got)
	}

	runner, err = New(map[string]any{"file": "main.lua"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), request); err == nil || !strings.Contains(err.Error(), "requires a packaged action") {
		t.Fatalf("borrowed file error = %v", err)
	}
}
