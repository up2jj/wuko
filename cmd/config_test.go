package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/localresource"
	"github.com/up2jj/wuko/marketplace"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/shell"
)

func configTestDependencies(directory string, output io.Writer) dependencies {
	return dependencies{
		stdin: strings.NewReader(""), stdout: output, stderr: io.Discard,
		cwd:     func() (string, error) { return directory, nil },
		homeDir: func() (string, error) { return "", nil }, configDir: func() (string, error) { return "", nil },
		getenv: func(string) string { return "" },
	}
}

func TestLocalConfigDiscoveryAndSelection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		files map[string]string
		args  []string
		json  bool
		err   string
	}{
		{name: "absent", args: []string{"list"}},
		{name: "yml", files: map[string]string{"wuko.yml": "commands: {list: {flags: {format: json}}}"}, args: []string{"list"}, json: true},
		{name: "yaml", files: map[string]string{"wuko.yaml": "commands: {list: {flags: {format: json}}}"}, args: []string{"list"}, json: true},
		{name: "empty", files: map[string]string{"wuko.yml": "# empty config\n"}, args: []string{"list"}},
		{name: "no parent lookup", files: map[string]string{"../wuko.yml": "invalid: true"}, args: []string{"list"}},
		{name: "ambiguous", files: map[string]string{"wuko.yml": "{}", "wuko.yaml": "{}"}, args: []string{"list"}, err: "both wuko.yml and wuko.yaml"},
		{name: "explicit bypasses ambiguity", files: map[string]string{"wuko.yml": "commands: {list: {flags: {format: json}}}", "wuko.yaml": "invalid: true"}, args: []string{"--config=wuko.yml", "list"}, json: true},
		{name: "explicit missing", args: []string{"--config=missing.yml", "list"}, err: "reading CLI config"},
		{name: "explicit empty", args: []string{"--config=", "list"}, err: "must name a YAML file"},
		{name: "bypass malformed", files: map[string]string{"wuko.yml": "[broken"}, args: []string{"--no-config", "list"}},
		{name: "conflicting controls", args: []string{"--no-config", "--config=other.yml", "list"}, err: "cannot be used"},
		{name: "explicit false bypass", files: map[string]string{"wuko.yml": "commands: {list: {flags: {format: json}}}"}, args: []string{"--no-config=false", "list"}, json: true},
		{name: "CLI overrides format", files: map[string]string{"wuko.yml": "commands: {list: {flags: {format: json}}}"}, args: []string{"list", "--format=text"}},
		{name: "config is directory", files: map[string]string{"wuko.yml/child": ""}, args: []string{"list"}, err: "reading CLI config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "project")
			testfs.MkdirAll(t, directory, 0o755)
			testfs.WriteTree(t, directory, test.files)
			var output bytes.Buffer
			root := newRootCmd(configTestDependencies(directory, &output))
			root.SetArgs(append([]string{}, test.args...))
			err := root.ExecuteContext(t.Context())
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("Execute() = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(output.String(), `"schema_version"`); got != test.json {
				t.Fatalf("JSON output = %v, want %v; output: %s", got, test.json, output.String())
			}
		})
	}
}

func TestLocalConfigRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, config, want string }{
		{"unknown field", "unknown: true", "field unknown"},
		{"unknown command", "commands: {missing: {}}", "commands.missing"},
		{"private command", "commands: {__script-sdk: {}}", "unknown public command"},
		{"unknown nested command", "commands: {marketplace: {commands: {missing: {}}}}", "commands.marketplace.commands.missing"},
		{"inactive flag", "commands: {run: {flags: {missing: true}}}", "commands.run.flags.missing"},
		{"global local flag", "flags: {once: true}", "flags.once"},
		{"inherited flag in command", "commands: {run: {flags: {debug: true}}}", "commands.run.flags.debug"},
		{"config control", "flags: {no-config: true}", "flags.no-config"},
		{"workflow uninstall confirmation", "commands: {uninstall: {flags: {yes: true}}}", "commands.uninstall.flags.yes: must be passed on the command line"},
		{"workflow uninstall scope", "commands: {uninstall: {flags: {global: true}}}", "commands.uninstall.flags.global: must be passed"},
		{"plugin uninstall confirmation", "commands: {plugin: {commands: {uninstall: {flags: {yes: true}}}}}", "commands.plugin.commands.uninstall.flags.yes: must be passed"},
		{"plugin uninstall scope", "commands: {plugin: {commands: {uninstall: {flags: {global: true}}}}}", "commands.plugin.commands.uninstall.flags.global: must be passed"},
		{"marketplace workflow removal", "commands: {marketplace: {commands: {workflow: {commands: {remove: {flags: {yes: true}}}}}}}", "remove.flags.yes: must be passed"},
		{"marketplace plugin removal", "commands: {marketplace: {commands: {plugin: {commands: {remove: {flags: {yes: true}}}}}}}", "remove.flags.yes: must be passed"},
		{"generated help", "commands: {run: {flags: {help: true}}}", "flags.help"},
		{"wrong bool", "flags: {debug: 'true'}", "expected bool"},
		{"wrong string", "commands: {run: {flags: {cpus: 1.5}}}", "expected string"},
		{"wrong list", "flags: {env-loader: mise}", "expected stringArray"},
		{"wrong list member", "flags: {env-loader: [mise, false]}", "expected stringArray"},
		{"null option", "flags: {debug: null}", "expected bool"},
		{"duplicate flag", "flags: {debug: true, debug: false}", "already defined"},
		{"duplicate section", "flags: {}\nflags: {}", "already defined"},
		{"multiple documents", "flags: {}\n---\nflags: {}", "multiple YAML documents"},
		{"missing workflow", "default: {flags: {once: true}}", "default.workflow"},
		{"empty workflow", "default: {workflow: ' '}", "default.workflow"},
		{"wrong workflow type", "default: {workflow: 42}", "expected a string"},
		{"wrong target type", "default: {workflow: build, target: [production]}", "expected a string"},
		{"unknown default field", "default: {workflow: build, missing: true}", "field missing"},
		{"unknown default flag", "default: {workflow: build, flags: {missing: true}}", "default.flags.missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, "wuko.yml")
			testfs.Write(t, path, test.config)
			root := newRootCmd(configTestDependencies(directory, io.Discard))
			root.SetArgs([]string{"list"})
			err := root.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("Execute() = %v, want file context and %q", err, test.want)
			}
		})
	}
}

const defaultInvocationConfig = `flags:
  env-loader: [none]
default:
  workflow: build
  target: production
  flags:
    reporter: [plain]
    dry-run: false
    var: [stage=preset]
commands:
  run:
    flags:
      reporter: [github]
      dry-run: true
      var: [stage=run, extra=run]
`

func TestLocalConfigDefaultInvocationAndPrecedence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, config, want string
		args               []string
	}{
		{name: "default target and flags", config: defaultInvocationConfig, want: "preset/base:production"},
		{name: "CLI replaces preset list", config: defaultInvocationConfig, args: []string{"--var=stage=cli"}, want: "cli/base:production"},
		{name: "CLI overrides preset bool", config: defaultInvocationConfig, args: []string{"--dry-run"}, want: "Workflow build"},
		{name: "explicit subcommand ignores preset", config: defaultInvocationConfig, args: []string{"run", "build", "staging", "--dry-run=false", "--reporter=plain"}, want: "run/run:staging"},
		{name: "empty preset list replaces run defaults", config: strings.ReplaceAll(defaultInvocationConfig, "var: [stage=preset]", "var: []"), want: "base/base:production"},
		{name: "without target", config: "default: {workflow: simple}\nflags: {env-loader: [none]}", want: "simple"},
		{name: "without preset opens picker", config: "commands: {run: {flags: {dry-run: true}}}", want: "target production"},
		{name: "bypass preset opens picker", config: defaultInvocationConfig, args: []string{"--no-config"}, want: "target production"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			testfs.Write(t, filepath.Join(directory, "wuko.yml"), test.config)
			testfs.Write(t, filepath.Join(directory, ".wuko/workflows/build.yaml"), `version: 1
name: build
vars: {stage: base, extra: base}
targets:
  production:
    steps:
      - id: print
        type: shell
        with: {script: "printf '%s' '{{ .vars.stage }}/{{ .vars.extra }}:production'"}
  staging:
    steps:
      - id: print
        type: shell
        with: {script: "printf '%s' '{{ .vars.stage }}/{{ .vars.extra }}:staging'"}
`)
			testfs.Write(t, filepath.Join(directory, ".wuko/workflows/simple.yaml"), "version: 1\nname: simple\nsteps:\n  - id: print\n    type: shell\n    with: {script: printf simple}\n")
			var output, diagnostics bytes.Buffer
			deps := configTestDependencies(directory, &output)
			deps.stderr = &diagnostics
			deps.registry = step.NewRegistry()
			if err := shell.Register(deps.registry); err != nil {
				t.Fatal(err)
			}
			root := newRootCmd(deps)
			root.SetArgs(append([]string{}, test.args...))
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String()+diagnostics.String(), test.want) {
				t.Fatalf("output = %q, diagnostics = %q, want %q", output.String(), diagnostics.String(), test.want)
			}
		})
	}
}

func TestLocalConfigRunFlagsRequireDefaultWorkflow(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	root := newRootCmd(configTestDependencies(t.TempDir(), &output))
	root.SetArgs([]string{"--dry-run", "--report-json=-"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "requires default.workflow") {
		t.Fatalf("Execute() = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want no report when no workflow was selected", output.String())
	}
}

func TestLocalConfigFailureKeepsArgumentErrors(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "[broken")
	root := newRootCmd(configTestDependencies(directory, io.Discard))
	root.SetArgs([]string{"typo"})
	err := root.ExecuteContext(t.Context())
	if err == nil {
		t.Fatal("Execute() = nil, want an error")
	}
	message := err.Error()
	unknown, decoding := strings.Index(message, `unknown command "typo"`), strings.Index(message, "decoding CLI config")
	if unknown < 0 || decoding < unknown {
		t.Fatalf("Execute() = %q, want the unknown command error before the config error", message)
	}
}

func TestLocalConfigUnreadableWorkingDirectory(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"discovery skipped", []string{"list"}, "finding current directory"},
		{"relative explicit config", []string{"--config=wuko.yml", "list"}, "resolving --config wuko.yml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := configTestDependencies(t.TempDir(), io.Discard)
			deps.cwd = func() (string, error) { return "", os.ErrNotExist }
			root := newRootCmd(deps)
			root.SetArgs(test.args)
			if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Execute() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLocalConfigDefaultFailureReportsRun(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "default: {workflow: build, flags: {dry-run: invalid}}")
	var output bytes.Buffer
	root := newRootCmd(configTestDependencies(directory, &output))
	root.SetArgs([]string{"--report-json=-"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "expected bool") {
		t.Fatalf("Execute() = %v", err)
	}
	report := decodeStreamExecutionReport(t, output.Bytes())
	if report.Status != "failed" || report.InvocationID == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestLocalConfigNestedDefaultsApplyBeforeArgumentValidation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "commands:\n  marketplace:\n    commands:\n      plugin:\n        commands:\n          init:\n            flags: {list-templates: true}\n")
	var output bytes.Buffer
	root := newRootCmd(configTestDependencies(directory, &output))
	root.SetArgs([]string{"marketplace", "plugin", "init"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "generic") || !strings.Contains(output.String(), "go") {
		t.Fatalf("templates = %q", output.String())
	}
}

func TestLocalConfigOptionPresenceAndCLIProvenance(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "flags: {debug: true, frozen-lockfile: true}\ncommands:\n  marketplace:\n    commands:\n      plugin:\n        commands:\n          add:\n            flags: {description: ''}\n")
	root := newRootCmd(configTestDependencies(directory, io.Discard))
	command, _, err := root.Find([]string{"marketplace", "plugin", "add"})
	if err != nil {
		t.Fatal(err)
	}
	command.RunE = func(command *cobra.Command, _ []string) error {
		if !optionProvided(command, "description") || command.Flags().Changed("description") {
			t.Fatal("configured empty description must be supplied without becoming explicit CLI input")
		}
		debug, _ := command.Flags().GetBool("debug")
		frozen, _ := command.Flags().GetBool("frozen-lockfile")
		if debug || !frozen {
			t.Fatalf("debug = %v, frozen = %v", debug, frozen)
		}
		return nil
	}
	root.SetArgs([]string{"--debug=false", "marketplace", "plugin", "add", "source"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalConfigEnvironmentPrecedence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, environment, want string
		args                    []string
	}{
		{name: "config", want: "mise,direnv"},
		{name: "environment", environment: "asdf", want: "asdf"},
		{name: "CLI", environment: "asdf", args: []string{"--env-loader=none"}, want: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			testfs.Write(t, filepath.Join(directory, "wuko.yml"), "flags: {env-loader: [mise, direnv]}")
			deps := configTestDependencies(directory, io.Discard)
			deps.getenv = func(string) string { return test.environment }
			root := newRootCmd(deps)
			command, _, _ := root.Find([]string{"list"})
			command.RunE = func(command *cobra.Command, _ []string) error {
				policy, err := invocationEnvironmentPolicy(command, deps)
				if err != nil {
					return err
				}
				if policy.String() != test.want {
					t.Fatalf("policy = %q, want %q", policy.String(), test.want)
				}
				return nil
			}
			root.SetArgs(append([]string{"list"}, test.args...))
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalConfigResourceWorkerDoesNotReloadConfiguration(t *testing.T) {
	t.Parallel()
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "default"}[bare], func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			testfs.Write(t, filepath.Join(directory, "wuko.yml"), "flags: {debug: true, env-loader: [none]}\ndefault: {workflow: build, target: production}\ncommands:\n  run:\n    flags: {cpus: '1.5', memory: 512MiB, reporter: [plain], var: ['stage=a,b']}\n")
			deps := configTestDependencies(directory, io.Discard)
			wantErr := errors.New("supervisor result")
			calls := 0
			deps.resourceRunner = func(_ context.Context, limits localresource.Limits, options process.Options) (process.Result, error) {
				calls++
				if limits.CPUs != 1.5 || limits.Memory != 512<<20 {
					t.Fatalf("limits = %#v", limits)
				}
				want := []string{"run", "--debug=true", "--env-loader=none", "--reporter=plain", "--var=stage=a,b", "--no-config", "--", "build", "production"}
				if diff := cmp.Diff(want, options.Args); diff != "" {
					t.Fatalf("worker arguments mismatch (-want +got):\n%s", diff)
				}
				worker := newRootCmd(configTestDependencies(directory, io.Discard))
				command, _, _ := worker.Find([]string{"run"})
				command.RunE = func(command *cobra.Command, args []string) error {
					if optionProvided(command, "cpus") || optionProvided(command, "memory") || !slices.Equal(args, []string{"build", "production"}) {
						t.Fatal("worker reloaded limits or lost the selected workflow")
					}
					return nil
				}
				worker.SetArgs(options.Args)
				if err := worker.ExecuteContext(t.Context()); err != nil {
					t.Fatal(err)
				}
				return process.Result{}, wantErr
			}
			root := newRootCmd(deps)
			args := []string{"--config=wuko.yml", "run", "build", "production"}
			if bare {
				args = []string{}
			}
			root.SetArgs(args)
			if err := root.ExecuteContext(t.Context()); !errors.Is(err, wantErr) {
				t.Fatalf("Execute() = %v", err)
			}
			if calls != 1 {
				t.Fatalf("supervisor called %d times", calls)
			}
		})
	}
}

// Not parallel: cobra keeps registered flag completions in a process-wide map, and
// generating a completion script annotates the flags of every root built so far.
func TestLocalConfigBypassesInfrastructureCommands(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"help", "run"}, {"completion", "bash"}, {"__complete", "run", ""}, {"__script-sdk"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			directory := t.TempDir()
			testfs.Write(t, filepath.Join(directory, "wuko.yml"), "invalid: true")
			root := newRootCmd(configTestDependencies(directory, io.Discard))
			if args[0] == "__script-sdk" {
				command, _, _ := root.Find(args)
				command.RunE = func(*cobra.Command, []string) error { return nil }
			}
			root.SetArgs(args)
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalConfigExternalFileKeepsInvocationPaths(t *testing.T) {
	// run --file historically resolves relative paths through the process directory.
	// Change it only in this serial test, while injecting the same invocation directory.
	directory := t.TempDir()
	t.Chdir(directory)
	testfs.Write(t, filepath.Join(directory, "config/settings.yml"), "flags: {env-loader: [none]}\ncommands:\n  run:\n    flags: {file: ./check.yaml, var-file: [./values.json], report-json: ./report.json}\n")
	testfs.Write(t, filepath.Join(directory, "values.json"), `{"answer":42}`)
	testfs.Write(t, filepath.Join(directory, "check.yaml"), "version: 1\nname: check\nvars: {answer: 0}\nsteps:\n  - return: {outputs: {answer: vars.answer}}\n")
	root := newRootCmd(configTestDependencies(directory, io.Discard))
	root.SetArgs([]string{"--config=config/settings.yml", "run"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	report := readExecutionReport(t, filepath.Join(directory, "report.json"))
	if report.Outputs == nil || (*report.Outputs)["answer"] != float64(42) {
		t.Fatalf("outputs = %#v", report.Outputs)
	}
}

func TestLocalConfigMarketplaceDescriptionCanBeCleared(t *testing.T) {
	t.Parallel()
	directory, home, source := t.TempDir(), t.TempDir(), t.TempDir()
	writeMarketplacePluginRelease(t, source, "acme", "1.0.0")
	command := marketplaceTestCommand(directory, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "add", source, "--description=Acme"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "commands:\n  marketplace:\n    commands:\n      plugin:\n        commands:\n          update:\n            flags: {description: ''}\n")
	command = marketplaceTestCommand(directory, home, nil)
	command.SetArgs([]string{"marketplace", "plugin", "update", "acme", source})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	metadata, err := marketplace.ReadPluginSource(filepath.Join(directory, ".wuko", "plugin-sources", "acme"))
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Description != "" {
		t.Fatalf("description = %q, want explicitly configured empty value", metadata.Description)
	}
}

func TestLocalConfigFailurePreservesStreamedReport(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	testfs.Write(t, filepath.Join(directory, "wuko.yml"), "flags: {debug: invalid}")
	var output bytes.Buffer
	root := newRootCmd(configTestDependencies(directory, &output))
	root.SetArgs([]string{"run", "build", "--report-json=-"})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "expected bool") {
		t.Fatalf("Execute() = %v", err)
	}
	report := decodeStreamExecutionReport(t, output.Bytes())
	if report.Status != "failed" || report.InvocationID == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestLocalConfigRootInstancesAreIsolated(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "wuko.yml")
	testfs.Write(t, path, "commands: {list: {flags: {format: json}}}")
	for _, configured := range []bool{true, false} {
		if !configured {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		var output bytes.Buffer
		root := newRootCmd(configTestDependencies(directory, &output))
		root.SetArgs([]string{"list"})
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), `"schema_version"`) != configured {
			t.Fatalf("configured = %v, output = %q", configured, output.String())
		}
	}
}
