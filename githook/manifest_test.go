package githook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".wuko"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := "version: 1\nhooks:\n  pre-commit:\n    - workflow: check\n      target: staged\n"
	if err := os.WriteFile(filepath.Join(root, ManifestPath), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Hooks["pre-commit"]) != 1 || manifest.Hooks["pre-commit"][0].Target != "staged" {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestLoadManifestRejectsUnknownAndUnsupportedFields(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field":    "version: 1\nhooks:\n  pre-commit:\n    - workflow: check\n      vars: {}\n",
		"unsupported hook": "version: 1\nhooks:\n  pre-receive:\n    - workflow: check\n",
		"empty bindings":   "version: 1\nhooks:\n  pre-commit: []\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".wuko"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ManifestPath), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(root); err == nil {
				t.Fatal("LoadManifest() unexpectedly succeeded")
			}
		})
	}
}

func TestContextParsesPrePush(t *testing.T) {
	repository := Repository{Root: "/repo", GitDir: "/repo/.git", CommonDir: "/repo/.git"}
	input := "refs/heads/main aaaa refs/heads/main bbbb\n"
	value, err := Context(repository, "pre-push", []string{"origin", "ssh://example/repo"}, input)
	if err != nil {
		t.Fatal(err)
	}
	hook := value["hook"].(map[string]any)
	payload := hook["payload"].(map[string]any)
	updates := payload["updates"].([]any)
	if payload["remote_name"] != "origin" || len(updates) != 1 || hook["stdin"] != input {
		t.Fatalf("context = %#v", value)
	}
}

func TestContextRejectsMalformedHookProtocol(t *testing.T) {
	_, err := Context(Repository{Root: "/repo"}, "pre-push", []string{"origin", "url"}, "too few\n")
	if err == nil || !strings.Contains(err.Error(), "expected 4") {
		t.Fatalf("error = %v", err)
	}
}

func TestContextParsesSupportedHookPayloads(t *testing.T) {
	repository := Repository{Root: "/repo", GitDir: "/repo/.git", CommonDir: "/repo/.git"}
	tests := []struct {
		name, stdin string
		args        []string
		wantKey     string
		want        any
	}{
		{name: "applypatch-msg", args: []string{"message"}, wantKey: "message_file", want: "/repo/message"},
		{name: "commit-msg", args: []string{"message"}, wantKey: "message_file", want: "/repo/message"},
		{name: "sendemail-validate", args: []string{"patch"}, wantKey: "patch_file", want: "/repo/patch"},
		{name: "prepare-commit-msg", args: []string{"message", "commit", "abcd"}, wantKey: "source", want: "commit"},
		{name: "pre-rebase", args: []string{"main", "topic"}, wantKey: "branch", want: "topic"},
		{name: "post-checkout", args: []string{"aaaa", "bbbb", "1"}, wantKey: "branch_checkout", want: true},
		{name: "post-merge", args: []string{"0"}, wantKey: "squash", want: false},
		{name: "post-rewrite", args: []string{"rebase"}, stdin: "aaaa bbbb\n", wantKey: "command", want: "rebase"},
		{name: "post-index-change", args: []string{"1", "0"}, wantKey: "working_tree_updated", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := Context(repository, test.name, test.args, test.stdin)
			if err != nil {
				t.Fatal(err)
			}
			payload := value["hook"].(map[string]any)["payload"].(map[string]any)
			if payload[test.wantKey] != test.want {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}
	for _, name := range []string{"pre-applypatch", "post-applypatch", "pre-commit", "pre-merge-commit", "post-commit", "pre-auto-gc"} {
		t.Run(name, func(t *testing.T) {
			value, err := Context(repository, name, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			payload := value["hook"].(map[string]any)["payload"].(map[string]any)
			if len(payload) != 0 {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}
}

func TestLoadManifestRejectsRemoteWorkflowLocator(t *testing.T) {
	for name, locator := range map[string]string{
		"github locator": "github:attacker/repo",
		"https locator":  "https://example.test/wuko.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".wuko"), 0o755); err != nil {
				t.Fatal(err)
			}
			data := "version: 1\nhooks:\n  pre-commit:\n    - workflow: " + locator + "\n"
			if err := os.WriteFile(filepath.Join(root, ManifestPath), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadManifest(root)
			if err == nil || !strings.Contains(err.Error(), "locally discovered workflow") {
				t.Fatalf("LoadManifest() error = %v", err)
			}
		})
	}
}
