package cmd

import (
	"io"
	"testing"

	envload "github.com/up2jj/wuko/environment"
)

func TestInvocationEnvironmentPolicyDefaultsToAutomatic(t *testing.T) {
	command := newRootCmd(dependencies{stdin: nil, stdout: io.Discard, stderr: io.Discard, getenv: func(string) string { return "" }})
	policy, err := invocationEnvironmentPolicy(command, dependencies{getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Automatic() {
		t.Fatalf("policy = %q", policy.String())
	}
}

func TestInvocationEnvironmentPolicyReadsEnvironment(t *testing.T) {
	deps := dependencies{getenv: func(name string) string {
		if name == "WUKO_ENV_LOADERS" {
			return "mise,direnv"
		}
		return ""
	}}
	command := newRootCmd(dependencies{stdin: nil, stdout: io.Discard, stderr: io.Discard, getenv: deps.getenv})
	policy, err := invocationEnvironmentPolicy(command, deps)
	if err != nil {
		t.Fatal(err)
	}
	if policy.String() != "mise,direnv" {
		t.Fatalf("policy = %q", policy.String())
	}
}

func TestEnvironmentLoaderFlagOverridesEnvironment(t *testing.T) {
	deps := dependencies{getenv: func(string) string { return "asdf,direnv" }}
	command := newRootCmd(dependencies{stdin: nil, stdout: io.Discard, stderr: io.Discard, getenv: deps.getenv})
	if err := command.PersistentFlags().Set("env-loader", "mise"); err != nil {
		t.Fatal(err)
	}
	if err := command.PersistentFlags().Set("env-loader", "direnv"); err != nil {
		t.Fatal(err)
	}
	policy, err := invocationEnvironmentPolicy(command, deps)
	if err != nil {
		t.Fatal(err)
	}
	if policy.String() != "mise,direnv" {
		t.Fatalf("policy = %q", policy.String())
	}
}

func TestEnvironmentLoaderFlagRejectsUnsafeOrder(t *testing.T) {
	command := newRootCmd(dependencies{stdin: nil, stdout: io.Discard, stderr: io.Discard})
	for _, name := range []string{"direnv", "mise"} {
		if err := command.PersistentFlags().Set("env-loader", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := invocationEnvironmentPolicy(command, dependencies{getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("expected invalid policy")
	}
}

func TestEnvironmentValuesClonesProvenance(t *testing.T) {
	result := envload.InvocationEnvironment{Values: map[string]string{"PATH": "/bin"}, Loaders: []string{"mise"}}
	_, loaders := environmentValues(result)
	loaders[0] = "changed"
	if result.Loaders[0] != "mise" {
		t.Fatalf("result loaders mutated: %#v", result.Loaders)
	}
}

func TestEnvironmentLoaderFlagAcceptsCommaSeparatedValues(t *testing.T) {
	deps := dependencies{getenv: func(string) string { return "" }}
	command := newRootCmd(dependencies{stdin: nil, stdout: io.Discard, stderr: io.Discard, getenv: deps.getenv})
	if err := command.PersistentFlags().Set("env-loader", "mise,direnv"); err != nil {
		t.Fatal(err)
	}
	policy, err := invocationEnvironmentPolicy(command, deps)
	if err != nil {
		t.Fatal(err)
	}
	if policy.String() != "mise,direnv" {
		t.Fatalf("policy = %q", policy.String())
	}
}
