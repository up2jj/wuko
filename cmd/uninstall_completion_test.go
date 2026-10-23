package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	pluginpkg "github.com/up2jj/wuko/plugin"
	"github.com/up2jj/wuko/workflow"
)

func TestUninstallWorkflowCompletionUsesSelectedStorageScope(t *testing.T) {
	root := t.TempDir()
	project, home := filepath.Join(root, "project"), filepath.Join(root, "home")
	local, global := filepath.Join(project, ".wuko", "workflows"), filepath.Join(home, ".wuko", "workflows")
	for path, content := range map[string]string{
		filepath.Join(root, ".wuko", "workflows", "ancestor.yaml"):                         "description: Ancestor",
		filepath.Join(global, "global.yaml"):                                               "description: Global",
		filepath.Join(local, "check.yaml"):                                                 "description: Check\nplugins: {missing: {command: /does/not/exist}}\nuninstall: [{type: missing_plugin_step}]",
		filepath.Join(local, "nested", "helper.yaml"):                                      "description: Internal\ninvokable: false",
		filepath.Join(local, "targeted.yaml"):                                              "description: Targets\ntargets: {first: {}, second: {}}",
		filepath.Join(local, "draft.yaml"):                                                 "version: [",
		filepath.Join(local, "marketplace", "release", "wuko.yaml"):                        "name: packaged\ndescription: Package",
		filepath.Join(local, "marketplace", "release", workflow.WorkflowPackageMarkerName): `{"version":1,"name":"packaged"}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"uninstall", ""}, "check\tCheck\nnested/helper\tInternal\npackaged\tPackage\ntargeted\tTargets\n:4\n"},
		{[]string{"uninstall", "--global", ""}, "global\tGlobal\n:4\n"},
		{[]string{"uninstall", "nest"}, "nested/helper\tInternal\n:4\n"},
		{[]string{"uninstall", "targeted", ""}, ":4\n"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			if got := executeUninstallCompletion(t, project, home, test.args); got != test.want {
				t.Fatalf("completion = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(local, "check.yaml")); err != nil {
		t.Fatalf("completion removed workflow: %v", err)
	}
}

func TestUninstallPluginCompletionUsesValidatedInstallationsInSelectedScope(t *testing.T) {
	root := t.TempDir()
	project, home := filepath.Join(root, "project"), filepath.Join(root, "home")
	local, global := filepath.Join(project, ".wuko", "plugins"), filepath.Join(home, ".wuko", "plugins")
	launched := filepath.Join(root, "plugin-launched")
	for _, item := range []struct{ root, name string }{{local, "zeta"}, {local, "alpha"}, {global, "global"}, {local, "tampered"}} {
		writeCompletionPlugin(t, item.root, item.name, launched)
	}
	if err := os.WriteFile(filepath.Join(local, "tampered", "wuko-plugin-tampered"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(local, "unfinished"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"plugin", "uninstall", ""}, "alpha\nzeta\n:4\n"},
		{[]string{"plugin", "uninstall", "--global", ""}, "global\n:4\n"},
		{[]string{"plugin", "uninstall", "al"}, "alpha\n:4\n"},
		{[]string{"plugin", "uninstall", "alpha", ""}, ":4\n"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			if got := executeUninstallCompletion(t, project, home, test.args); got != test.want {
				t.Fatalf("completion = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := os.Stat(launched); !os.IsNotExist(err) {
		t.Fatalf("completion launched a plugin: %v", err)
	}
	if _, err := pluginpkg.ValidateInstallation(filepath.Join(local, "alpha"), "alpha"); err != nil {
		t.Fatalf("completion changed plugin installation: %v", err)
	}
}

func TestUninstallCompletionHandlesMissingAndInvalidDirectories(t *testing.T) {
	for _, command := range [][]string{{"uninstall"}, {"plugin", "uninstall"}} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			root := t.TempDir()
			args := append(append([]string(nil), command...), "")
			if got := executeUninstallCompletion(t, root, "", args); got != ":4\n" {
				t.Fatalf("empty directory completion = %q", got)
			}
			if err := os.WriteFile(filepath.Join(root, ".wuko"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if got := executeUninstallCompletion(t, root, "", args); got != ":1\n" {
				t.Fatalf("invalid directory completion = %q", got)
			}
		})
	}
}

func executeUninstallCompletion(t *testing.T, project, home string, args []string) string {
	t.Helper()
	var output bytes.Buffer
	input := strings.NewReader("do not consume")
	deps := listTestDependencies(project, &output)
	deps.homeDir = func() (string, error) { return home, nil }
	deps.stdin = input
	command := newRootCmd(deps)
	command.SetArgs(append([]string{"__complete"}, args...))
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if input.Len() != len("do not consume") {
		t.Fatal("completion consumed stdin")
	}
	return output.String()
}

func writeCompletionPlugin(t *testing.T, root, namespace, launched string) {
	t.Helper()
	dir := filepath.Join(root, namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nprintf launched > " + shellQuote(launched) + "\nexit 99\n")
	if err := os.WriteFile(filepath.Join(dir, "wuko-plugin-"+namespace), content, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := pluginpkg.InstallationMarker{
		MarkerVersion: 2, ManifestVersion: 1, Namespace: namespace, PluginVersion: "1.0.0",
		Protocol: pluginpkg.ProtocolV1, Source: "https://plugins.test/" + namespace,
		ManifestDigest: strings.Repeat("a", 64), ArtifactDigest: strings.Repeat("b", 64),
		ExecutableDigest: fmt.Sprintf("%x", sha256.Sum256(content)), OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pluginpkg.MarkerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
