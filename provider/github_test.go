package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func githubTestEnvironment(t *testing.T, payload string) map[string]string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"GITHUB_ACTIONS":     "true",
		"GITHUB_REPOSITORY":  "up2jj/wuko",
		"GITHUB_ACTOR":       "up2jj",
		"GITHUB_EVENT_NAME":  "push",
		"GITHUB_EVENT_PATH":  path,
		"GITHUB_SHA":         "merge-sha",
		"GITHUB_REF":         "refs/heads/main",
		"GITHUB_RUN_ID":      "123456789",
		"GITHUB_RUN_NUMBER":  "42",
		"GITHUB_RUN_ATTEMPT": "1",
		"GITHUB_SERVER_URL":  "https://github.com",
		"GITHUB_API_URL":     "https://api.github.com",
	}
}

func loadGitHub(t *testing.T, environment map[string]string) (map[string]any, bool, error) {
	t.Helper()
	return NewGitHub().Load(context.Background(), environment)
}

func TestGitHubSchemaKeepsCuratedFieldsClosedAndPayloadOpen(t *testing.T) {
	schema := NewGitHub().Schema()
	for _, field := range []string{"repository", "actor", "event", "pull_request", "sha", "ref", "run", "server_url", "api_url", "payload"} {
		if _, exists := schema.Fields[field]; !exists {
			t.Fatalf("schema is missing %q", field)
		}
	}
	if schema.Open || schema.Fields["repository"].Open || schema.Fields["run"].Open {
		t.Fatal("curated GitHub objects must be closed")
	}
	if !schema.Fields["payload"].Open {
		t.Fatal("payload schema must be open")
	}
}

func TestGitHubInactiveDoesNotInspectEvent(t *testing.T) {
	for _, marker := range []string{"", "false", "TRUE", "1", " true"} {
		t.Run(marker, func(t *testing.T) {
			value, active, err := loadGitHub(t, map[string]string{
				"GITHUB_ACTIONS":    marker,
				"GITHUB_EVENT_PATH": filepath.Join(t.TempDir(), "missing.json"),
			})
			if err != nil || active || value != nil {
				t.Fatalf("Load = %#v, %v, %v", value, active, err)
			}
		})
	}
}

func TestGitHubPushContextAndNativeNumbers(t *testing.T) {
	environment := githubTestEnvironment(t, `{"action":null,"small":12,"large":18446744073709551615,"fraction":1.25,"nested":[3]}`)
	value, active, err := loadGitHub(t, environment)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("GitHub provider is inactive")
	}
	wantRepository := map[string]any{"owner": "up2jj", "name": "wuko", "full_name": "up2jj/wuko"}
	if !reflect.DeepEqual(value["repository"], wantRepository) {
		t.Fatalf("repository = %#v", value["repository"])
	}
	if value["actor"] != "up2jj" || value["sha"] != "merge-sha" || value["ref"] != "refs/heads/main" {
		t.Fatalf("metadata = %#v", value)
	}
	if got := value["event"].(map[string]any); got["name"] != "push" || got["action"] != "" {
		t.Fatalf("event = %#v", got)
	}
	if got := value["run"].(map[string]any); got["id"] != int64(123456789) || got["number"] != int64(42) || got["attempt"] != int64(1) {
		t.Fatalf("run = %#v", got)
	}
	payload := value["payload"].(map[string]any)
	if _, ok := payload["small"].(int64); !ok {
		t.Fatalf("small number type = %T", payload["small"])
	}
	if _, ok := payload["large"].(uint64); !ok {
		t.Fatalf("large number type = %T", payload["large"])
	}
	if _, ok := payload["fraction"].(float64); !ok {
		t.Fatalf("fraction type = %T", payload["fraction"])
	}
	if _, exists := value["pull_request"]; exists {
		t.Fatal("push context contains pull_request")
	}
}

func TestGitHubPullRequestAndEnterpriseURLs(t *testing.T) {
	environment := githubTestEnvironment(t, `{
  "action":"synchronize",
  "pull_request":{"number":123,"head":{"sha":"head-sha","ref":"feature/foo"},"base":{"sha":"base-sha","ref":"main"}}
}`)
	environment["GITHUB_EVENT_NAME"] = "pull_request"
	delete(environment, "GITHUB_REF")
	environment["GITHUB_SERVER_URL"] = "https://github.example.com"
	environment["GITHUB_API_URL"] = "https://github.example.com/api/v3"
	value, active, err := loadGitHub(t, environment)
	if err != nil || !active {
		t.Fatalf("Load active=%v error=%v", active, err)
	}
	if value["ref"] != "" || value["server_url"] != environment["GITHUB_SERVER_URL"] || value["api_url"] != environment["GITHUB_API_URL"] {
		t.Fatalf("URLs/ref = %#v", value)
	}
	pull := value["pull_request"].(map[string]any)
	if pull["number"] != int64(123) {
		t.Fatalf("pull request number = %#v", pull["number"])
	}
	if got := pull["head"].(map[string]any); got["sha"] != "head-sha" || got["ref"] != "feature/foo" {
		t.Fatalf("head = %#v", got)
	}
	if value["sha"] != "merge-sha" {
		t.Fatalf("github.sha = %#v", value["sha"])
	}
}

func TestGitHubAcceptsEmptyPayload(t *testing.T) {
	value, active, err := loadGitHub(t, githubTestEnvironment(t, `{}`))
	if err != nil || !active {
		t.Fatalf("Load active=%v error=%v", active, err)
	}
	if got := value["payload"].(map[string]any); len(got) != 0 {
		t.Fatalf("payload = %#v", got)
	}
	if got := value["event"].(map[string]any)["action"]; got != "" {
		t.Fatalf("action = %#v", got)
	}
}

func TestGitHubOmitsNullPullRequest(t *testing.T) {
	value, active, err := loadGitHub(t, githubTestEnvironment(t, `{"pull_request":null}`))
	if err != nil || !active {
		t.Fatalf("Load active=%v error=%v", active, err)
	}
	if _, exists := value["pull_request"]; exists {
		t.Fatal("null pull_request was exposed")
	}
}

func TestGitHubRequiresRunnerMetadata(t *testing.T) {
	for _, name := range []string{
		"GITHUB_REPOSITORY", "GITHUB_ACTOR", "GITHUB_EVENT_NAME", "GITHUB_EVENT_PATH",
		"GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_RUN_NUMBER", "GITHUB_RUN_ATTEMPT",
		"GITHUB_SERVER_URL", "GITHUB_API_URL",
	} {
		t.Run(name, func(t *testing.T) {
			environment := githubTestEnvironment(t, `{}`)
			delete(environment, name)
			_, active, err := loadGitHub(t, environment)
			if err == nil || !strings.Contains(err.Error(), name+" is required") {
				t.Fatalf("Load active=%v error=%v", active, err)
			}
		})
	}
}

func TestGitHubRejectsInvalidMetadataAndEvents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, map[string]string)
		want   string
	}{
		{name: "missing repository", mutate: func(_ *testing.T, env map[string]string) { delete(env, "GITHUB_REPOSITORY") }, want: "GITHUB_REPOSITORY is required"},
		{name: "repository format", mutate: func(_ *testing.T, env map[string]string) { env["GITHUB_REPOSITORY"] = "owner/repo/extra" }, want: "owner/repository"},
		{name: "repository whitespace", mutate: func(_ *testing.T, env map[string]string) { env["GITHUB_REPOSITORY"] = "owner name/repo" }, want: "owner/repository"},
		{name: "zero run", mutate: func(_ *testing.T, env map[string]string) { env["GITHUB_RUN_NUMBER"] = "0" }, want: "positive integer"},
		{name: "bad URL", mutate: func(_ *testing.T, env map[string]string) { env["GITHUB_API_URL"] = "ssh://github.example.com" }, want: "absolute HTTP(S) URL"},
		{name: "missing file", mutate: func(t *testing.T, env map[string]string) {
			env["GITHUB_EVENT_PATH"] = filepath.Join(t.TempDir(), "missing")
		}, want: "opening GITHUB_EVENT_PATH"},
		{name: "directory", mutate: func(t *testing.T, env map[string]string) { env["GITHUB_EVENT_PATH"] = t.TempDir() }, want: "regular file"},
		{name: "malformed", mutate: replaceGitHubPayload(`{"broken":`), want: "decoding GITHUB_EVENT_PATH"},
		{name: "trailing", mutate: replaceGitHubPayload(`{} {}`), want: "multiple JSON values"},
		{name: "non object", mutate: replaceGitHubPayload(`[]`), want: "JSON object"},
		{name: "bad action", mutate: replaceGitHubPayload(`{"action":1}`), want: "action must be a string"},
		{name: "bad pull request", mutate: replaceGitHubPayload(`{"pull_request":[]}`), want: "pull_request must be an object"},
		{name: "missing pull number", mutate: replaceGitHubPayload(`{"pull_request":{"head":{"sha":"h","ref":"r"},"base":{"sha":"b","ref":"m"}}}`), want: "number is required"},
		{name: "bad pull ref", mutate: replaceGitHubPayload(`{"pull_request":{"number":1,"head":{"sha":"h","ref":""},"base":{"sha":"b","ref":"m"}}}`), want: "ref must be a non-empty string"},
		{name: "integer out of range", mutate: replaceGitHubPayload(`{"value":18446744073709551616}`), want: "out of range"},
		{name: "oversized", mutate: func(t *testing.T, env map[string]string) {
			path := filepath.Join(t.TempDir(), "large.json")
			if err := os.WriteFile(path, make([]byte, maxGitHubEventBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
			env["GITHUB_EVENT_PATH"] = path
		}, want: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environment := githubTestEnvironment(t, `{}`)
			tt.mutate(t, environment)
			_, active, err := loadGitHub(t, environment)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load active=%v error=%v, want %q", active, err, tt.want)
			}
			if tt.name == "integer out of range" && strings.Contains(err.Error(), "18446744073709551616") {
				t.Fatalf("payload value leaked into error: %v", err)
			}
		})
	}
}

func replaceGitHubPayload(payload string) func(*testing.T, map[string]string) {
	return func(t *testing.T, environment map[string]string) {
		path := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		environment["GITHUB_EVENT_PATH"] = path
	}
}
