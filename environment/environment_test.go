package environment

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParsePolicy(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    string
		wantErr string
	}{
		{name: "default", want: "auto"},
		{name: "automatic", values: []string{"auto"}, want: "auto"},
		{name: "disabled", values: []string{"none"}, want: "none"},
		{name: "manager and direnv", values: []string{"mise", "direnv"}, want: "mise,direnv"},
		{name: "unknown", values: []string{"unknown"}, wantErr: "unknown environment loader"},
		{name: "duplicate", values: []string{"direnv", "direnv"}, wantErr: "configured more than once"},
		{name: "automatic combined", values: []string{"auto", "direnv"}, wantErr: "must be used alone"},
		{name: "two managers", values: []string{"mise", "asdf"}, wantErr: "cannot be combined"},
		{name: "unsafe order", values: []string{"direnv", "asdf"}, wantErr: "must precede direnv"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := ParsePolicy(test.values)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ParsePolicy() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy.String() != test.want {
				t.Fatalf("policy = %q, want %q", policy.String(), test.want)
			}
		})
	}
}

func TestParsePolicyValue(t *testing.T) {
	policy, err := ParsePolicyValue(" mise, direnv ")
	if err != nil {
		t.Fatal(err)
	}
	if policy.String() != "mise,direnv" {
		t.Fatalf("policy = %q", policy.String())
	}
}

type fakeLoader struct {
	name  string
	probe func(Request) Probe
	load  func(Request) Result
}

func (loader fakeLoader) Name() string { return loader.name }

func (loader fakeLoader) Probe(_ context.Context, request Request) (Probe, error) {
	if loader.probe != nil {
		return loader.probe(request), nil
	}
	return Probe{Available: true}, nil
}

func (loader fakeLoader) Load(_ context.Context, request Request) (Result, error) {
	return loader.load(request), nil
}

func TestChainPassesProgressiveEnvironmentAndProvenance(t *testing.T) {
	first := fakeLoader{name: "first", load: func(request Request) Result {
		if request.Env["BASE"] != "yes" || len(request.Applied) != 0 {
			t.Fatalf("first request = %#v", request)
		}
		return Result{Changes: Changes{"VALUE": stringPointer("one"), "REMOVE": nil}, Applied: true}
	}}
	second := fakeLoader{name: "second", load: func(request Request) Result {
		if request.Env["VALUE"] != "one" || request.Env["REMOVE"] != "" || !reflect.DeepEqual(request.Applied, []string{"first"}) {
			t.Fatalf("second request = %#v", request)
		}
		return Result{Changes: Changes{"VALUE": stringPointer("two")}, Applied: true}
	}}
	base := map[string]string{"BASE": "yes", "REMOVE": "me"}
	result, err := (Chain{first, second}).Load(t.Context(), Request{Dir: t.TempDir(), Env: base})
	if err != nil {
		t.Fatal(err)
	}
	if result.Values["VALUE"] != "two" || !reflect.DeepEqual(result.Loaders, []string{"first", "second"}) {
		t.Fatalf("result = %#v", result)
	}
	if !maps.Equal(base, map[string]string{"BASE": "yes", "REMOVE": "me"}) {
		t.Fatalf("base was mutated: %#v", base)
	}
}

func TestChainAttributesLoaderFailure(t *testing.T) {
	loader := errorLoader{name: "broken", err: errors.New("boom")}
	_, err := (Chain{loader}).Load(t.Context(), Request{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "loading broken environment: boom") {
		t.Fatalf("error = %v", err)
	}
}

type errorLoader struct {
	name string
	err  error
}

func (loader errorLoader) Name() string { return loader.name }
func (errorLoader) Probe(context.Context, Request) (Probe, error) {
	return Probe{Available: true}, nil
}
func (loader errorLoader) Load(context.Context, Request) (Result, error) {
	return Result{}, loader.err
}

func TestMiseLoaderDecodesJSON(t *testing.T) {
	loader := newMiseLoader(foundLookup, func(_ context.Context, command Command) (CommandResult, error) {
		if command.Dir == "" || !reflect.DeepEqual(command.Args, []string{"env", "--json"}) || command.Env["BASE"] != "yes" {
			t.Fatalf("command = %#v", command)
		}
		return CommandResult{Stdout: `{"PATH":"/mise/bin:/bin","MODE":"dev"}`}, nil
	})
	result, err := loader.Load(t.Context(), Request{Dir: t.TempDir(), Env: map[string]string{"BASE": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	apply(values, result.Changes)
	if !result.Applied || values["MODE"] != "dev" || values["PATH"] != "/mise/bin:/bin" {
		t.Fatalf("result = %#v, values = %#v", result, values)
	}
}

func TestMiseLoaderRejectsMalformedJSON(t *testing.T) {
	loader := newMiseLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: "not json"}, nil
	})
	_, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "decoding export") {
		t.Fatalf("error = %v", err)
	}
}

func TestASDFLoaderValidatesAndPrependsShims(t *testing.T) {
	home := t.TempDir()
	homeShims := filepath.Join(home, ".asdf", "shims")
	dataDir := t.TempDir()
	dataShims := filepath.Join(dataDir, "shims")
	for _, dir := range []string{homeShims, dataShims} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	loader := newASDFLoader(foundLookup, func(_ context.Context, command Command) (CommandResult, error) {
		if !reflect.DeepEqual(command.Args, []string{"current"}) {
			t.Fatalf("args = %#v", command.Args)
		}
		return CommandResult{}, nil
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{"HOME": home, "PATH": "/bin:" + homeShims}})
	if err != nil {
		t.Fatal(err)
	}
	if got := *result.Changes["PATH"]; got != homeShims+":/bin" || !result.Applied {
		t.Fatalf("PATH = %q, applied = %v", got, result.Applied)
	}
	result, err = loader.Load(t.Context(), Request{Env: map[string]string{"ASDF_DATA_DIR": dataDir, "PATH": "/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := *result.Changes["PATH"]; got != dataShims+":/bin" {
		t.Fatalf("custom PATH = %q", got)
	}
}

func TestASDFLoaderToleratesUninstalledVersions(t *testing.T) {
	home := t.TempDir()
	shims := filepath.Join(home, ".asdf", "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	loader := newASDFLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: "golang 1.24 Not installed\n"}, failedExit(t)
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{"HOME": home, "PATH": "/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := *result.Changes["PATH"]; got != shims+":/bin" {
		t.Fatalf("PATH = %q", got)
	}
}

func TestASDFLoaderIsNotAppliedWithoutShims(t *testing.T) {
	loader := newASDFLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{}, nil
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{"HOME": t.TempDir(), "PATH": "/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || len(result.Changes) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestASDFLoaderReportsStartupFailure(t *testing.T) {
	loader := newASDFLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stderr: "permission denied"}, errors.New("fork/exec: permission denied")
	})
	_, err := loader.Load(t.Context(), Request{Env: map[string]string{"HOME": t.TempDir()}})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestMiseLoaderIsNotAppliedWithoutExports(t *testing.T) {
	loader := newMiseLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: "{}"}, nil
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("mise unexpectedly applied")
	}
}

// failedExit produces the *exec.ExitError a real command that ran and exited non-zero returns.
func failedExit(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exit error, got %v", err)
	}
	return err
}

func TestDirenvLoaderTracksActiveEnvironmentAndRemovals(t *testing.T) {
	loader := newDirenvLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: `{"DIRENV_FILE":"/repo/.envrc","REMOVE":null,"MODE":"direnv"}`}, nil
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{"REMOVE": "yes"}})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"REMOVE": "yes"}
	apply(values, result.Changes)
	if !result.Applied || values["MODE"] != "direnv" {
		t.Fatalf("result = %#v, values = %#v", result, values)
	}
	if _, exists := values["REMOVE"]; exists {
		t.Fatalf("REMOVE remains: %#v", values)
	}
}

func TestDirenvLoaderIsNotAppliedWithoutActiveEnvrc(t *testing.T) {
	loader := newDirenvLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: `{}`}, nil
	})
	result, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied {
		t.Fatal("direnv unexpectedly applied")
	}
}

func TestDirenvLoaderRejectsMalformedJSON(t *testing.T) {
	loader := newDirenvLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: "[invalid"}, nil
	})
	_, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "decoding export") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderIncludesCommandDiagnostic(t *testing.T) {
	loader := newMiseLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stderr: "configured version is not installed\n"}, errors.New("exit status 1")
	})
	_, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "configured version is not installed") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderErrorOmitsExportedEnvironment(t *testing.T) {
	loader := newDirenvLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{Stdout: `{"API_TOKEN":"super-secret"}`}, errors.New("exit status 1")
	})
	_, err := loader.Load(t.Context(), Request{Env: map[string]string{}})
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	loader := newMiseLoader(foundLookup, func(context.Context, Command) (CommandResult, error) {
		return CommandResult{}, errors.New("killed")
	})
	_, err := loader.Load(ctx, Request{Env: map[string]string{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestResolverAutomaticSelectionAndProgressiveLookup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".tool-versions"), []byte("nodejs 22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asdf := fakeLoader{name: LoaderASDF, probe: func(Request) Probe { return Probe{Available: true} }, load: func(Request) Result {
		return Result{Changes: Changes{"PATH": stringPointer("/asdf/shims:/bin")}, Applied: true}
	}}
	direnv := fakeLoader{name: LoaderDirenv, probe: func(request Request) Probe {
		return Probe{Available: strings.HasPrefix(request.Env["PATH"], "/asdf/shims")}
	}, load: func(request Request) Result {
		if !reflect.DeepEqual(request.Applied, []string{LoaderASDF}) {
			t.Fatalf("applied = %#v", request.Applied)
		}
		return Result{Applied: true}
	}}
	result, err := NewResolver(asdf, direnv).Load(t.Context(), root, map[string]string{"PATH": "/bin"}, AutomaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Loaders, []string{LoaderASDF, LoaderDirenv}) {
		t.Fatalf("loaders = %#v", result.Loaders)
	}
}

func TestResolverPrefersNativeMiseMarkerAtSameLevel(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"mise.toml", ".tool-versions"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mise := fakeLoader{name: LoaderMise, load: func(Request) Result { return Result{Applied: true} }}
	asdf := fakeLoader{name: LoaderASDF, load: func(Request) Result { t.Fatal("asdf loaded"); return Result{} }}
	result, err := NewResolver(mise, asdf).Load(t.Context(), root, map[string]string{}, AutomaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Loaders, []string{LoaderMise}) {
		t.Fatalf("loaders = %#v", result.Loaders)
	}
}

func TestResolverUsesNearestMarker(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".tool-versions"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	asdf := fakeLoader{name: LoaderASDF, load: func(Request) Result { return Result{Applied: true} }}
	mise := fakeLoader{name: LoaderMise, load: func(Request) Result { t.Fatal("mise loaded"); return Result{} }}
	result, err := NewResolver(mise, asdf).Load(t.Context(), child, map[string]string{}, AutomaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Loaders, []string{LoaderASDF}) {
		t.Fatalf("loaders = %#v", result.Loaders)
	}
}

func TestResolverFallsBackFromASDFToMise(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".tool-versions"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	asdf := fakeLoader{name: LoaderASDF, probe: func(Request) Probe { return Probe{} }, load: func(Request) Result { t.Fatal("asdf loaded"); return Result{} }}
	mise := fakeLoader{name: LoaderMise, load: func(Request) Result { return Result{Applied: true} }}
	result, err := NewResolver(asdf, mise).Load(t.Context(), root, map[string]string{}, AutomaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Loaders, []string{LoaderMise}) {
		t.Fatalf("loaders = %#v", result.Loaders)
	}
}

func TestResolverDisabledDoesNotRunLoaders(t *testing.T) {
	loader := fakeLoader{name: LoaderMise, load: func(Request) Result { t.Fatal("loader ran"); return Result{} }}
	base := map[string]string{"PATH": "/bin"}
	result, err := NewResolver(loader).Load(t.Context(), t.TempDir(), base, DisabledPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(result.Values, base) || len(result.Loaders) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolverAutomaticWithoutConfigurationOrDirenvIsUnchanged(t *testing.T) {
	root := t.TempDir()
	direnv := fakeLoader{name: LoaderDirenv, probe: func(Request) Probe { return Probe{} }, load: func(Request) Result {
		t.Fatal("direnv loaded")
		return Result{}
	}}
	base := map[string]string{"PATH": "/bin"}
	result, err := NewResolver(direnv).Load(t.Context(), root, base, AutomaticPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(result.Values, base) || len(result.Loaders) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestResolverFailsWhenConfiguredManagerIsUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mise.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mise := fakeLoader{name: LoaderMise, probe: func(Request) Probe { return Probe{} }, load: func(Request) Result { return Result{} }}
	_, err := NewResolver(mise).Load(t.Context(), root, map[string]string{}, AutomaticPolicy())
	if err == nil || !strings.Contains(err.Error(), "executable not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestMarkerSearchStopsAtHomeDirectory(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "projects", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(home), "mise.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := markerFor(project, map[string]string{"HOME": home}); got != markerNone {
		t.Fatalf("marker = %v, want none above home", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".tool-versions"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := markerFor(project, map[string]string{"HOME": home}); got != markerToolVersions {
		t.Fatalf("marker = %v, want tool-versions at home", got)
	}
}

func foundLookup(name string, _ map[string]string) (string, error) { return "/bin/" + name, nil }

func missingLookup(string, map[string]string) (string, error) { return "", exec.ErrNotFound }
