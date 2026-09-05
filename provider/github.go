package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const maxGitHubEventBytes int64 = 25 << 20

type githubProvider struct{}

// NewGitHub returns the built-in GitHub Actions execution-context provider.
func NewGitHub() Provider { return githubProvider{} }

func (githubProvider) Name() string { return "github" }

func (githubProvider) Schema() Schema {
	ref := Object(map[string]Schema{"sha": Scalar(), "ref": Scalar()})
	return Object(map[string]Schema{
		"repository": Object(map[string]Schema{
			"owner": Scalar(), "name": Scalar(), "full_name": Scalar(),
		}),
		"actor": Scalar(),
		"event": Object(map[string]Schema{"name": Scalar(), "action": Scalar()}),
		"pull_request": Object(map[string]Schema{
			"number": Scalar(), "head": ref, "base": ref,
		}),
		"sha": Scalar(), "ref": Scalar(),
		"run": Object(map[string]Schema{
			"id": Scalar(), "number": Scalar(), "attempt": Scalar(),
		}),
		"server_url": Scalar(), "api_url": Scalar(),
		"payload": OpenObject(),
	})
}

func (githubProvider) Load(ctx context.Context, environment map[string]string) (map[string]any, bool, error) {
	if environment["GITHUB_ACTIONS"] != "true" {
		return nil, false, nil
	}
	repository, err := requiredGitHubValue(environment, "GITHUB_REPOSITORY")
	if err != nil {
		return nil, false, err
	}
	owner, name, err := githubRepository(repository)
	if err != nil {
		return nil, false, err
	}
	actor, err := requiredGitHubValue(environment, "GITHUB_ACTOR")
	if err != nil {
		return nil, false, err
	}
	eventName, err := requiredGitHubValue(environment, "GITHUB_EVENT_NAME")
	if err != nil {
		return nil, false, err
	}
	sha, err := requiredGitHubValue(environment, "GITHUB_SHA")
	if err != nil {
		return nil, false, err
	}
	runID, err := githubPositiveNumber(environment, "GITHUB_RUN_ID")
	if err != nil {
		return nil, false, err
	}
	runNumber, err := githubPositiveNumber(environment, "GITHUB_RUN_NUMBER")
	if err != nil {
		return nil, false, err
	}
	runAttempt, err := githubPositiveNumber(environment, "GITHUB_RUN_ATTEMPT")
	if err != nil {
		return nil, false, err
	}
	serverURL, err := githubURL(environment, "GITHUB_SERVER_URL")
	if err != nil {
		return nil, false, err
	}
	apiURL, err := githubURL(environment, "GITHUB_API_URL")
	if err != nil {
		return nil, false, err
	}
	eventPath, err := requiredGitHubValue(environment, "GITHUB_EVENT_PATH")
	if err != nil {
		return nil, false, err
	}
	payload, err := readGitHubEvent(ctx, eventPath)
	if err != nil {
		return nil, false, err
	}
	action, err := githubEventAction(payload)
	if err != nil {
		return nil, false, err
	}

	value := map[string]any{
		"repository": map[string]any{"owner": owner, "name": name, "full_name": repository},
		"actor":      actor,
		"event":      map[string]any{"name": eventName, "action": action},
		"sha":        sha,
		"ref":        strings.TrimSpace(environment["GITHUB_REF"]),
		"run":        map[string]any{"id": runID, "number": runNumber, "attempt": runAttempt},
		"server_url": serverURL,
		"api_url":    apiURL,
		"payload":    payload,
	}
	pullRequest, found, err := githubPullRequest(payload)
	if err != nil {
		return nil, false, err
	}
	if found {
		value["pull_request"] = pullRequest
	}
	return value, true, nil
}

func requiredGitHubValue(environment map[string]string, name string) (string, error) {
	value := strings.TrimSpace(environment[name])
	if value == "" {
		return "", fmt.Errorf("%s is required in GitHub Actions", name)
	}
	return value, nil
}

func githubRepository(value string) (string, string, error) {
	owner, name, found := strings.Cut(value, "/")
	if !found || !githubRepositoryPart(owner) || !githubRepositoryPart(name) || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY must be an owner/repository identifier")
	}
	return owner, name, nil
}

func githubRepositoryPart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '.' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func githubPositiveNumber(environment map[string]string, name string) (any, error) {
	value, err := requiredGitHubValue(environment, name)
	if err != nil {
		return nil, err
	}
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || number == 0 {
		return nil, fmt.Errorf("%s must be a positive integer", name)
	}
	if number <= math.MaxInt64 {
		return int64(number), nil
	}
	return number, nil
}

func githubURL(environment map[string]string, name string) (string, error) {
	value, err := requiredGitHubValue(environment, name)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	return value, nil
}

func readGitHubEvent(ctx context.Context, path string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening GITHUB_EVENT_PATH: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting GITHUB_EVENT_PATH: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH must identify a regular file")
	}
	if info.Size() > maxGitHubEventBytes {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH exceeds %d-byte limit", maxGitHubEventBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxGitHubEventBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading GITHUB_EVENT_PATH: %w", err)
	}
	if int64(len(data)) > maxGitHubEventBytes {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH exceeds %d-byte limit", maxGitHubEventBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decoding GITHUB_EVENT_PATH: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding GITHUB_EVENT_PATH: multiple JSON values are not supported")
		}
		return nil, fmt.Errorf("decoding GITHUB_EVENT_PATH: %w", err)
	}
	normalized, err := normalizeGitHubJSON(decoded)
	if err != nil {
		return nil, fmt.Errorf("decoding GITHUB_EVENT_PATH: %w", err)
	}
	payload, ok := normalized.(map[string]any)
	if !ok || payload == nil {
		return nil, fmt.Errorf("decoding GITHUB_EVENT_PATH: event payload must be a JSON object")
	}
	return payload, nil
}

func normalizeGitHubJSON(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool:
		return typed, nil
	case json.Number:
		if !strings.ContainsAny(typed.String(), ".eE") {
			if signed, err := typed.Int64(); err == nil {
				return signed, nil
			}
			if unsigned, err := strconv.ParseUint(typed.String(), 10, 64); err == nil {
				return unsigned, nil
			}
			return nil, fmt.Errorf("JSON integer is out of range")
		}
		number, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("JSON number is invalid")
		}
		return number, nil
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeGitHubJSON(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for name, item := range typed {
			normalized, err := normalizeGitHubJSON(item)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", name, err)
			}
			result[name] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

func githubEventAction(payload map[string]any) (string, error) {
	value, exists := payload["action"]
	if !exists || value == nil {
		return "", nil
	}
	action, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("GitHub event action must be a string")
	}
	return action, nil
}

func githubPullRequest(payload map[string]any) (map[string]any, bool, error) {
	value, exists := payload["pull_request"]
	if !exists || value == nil {
		return nil, false, nil
	}
	pull, ok := value.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("GitHub event pull_request must be an object")
	}
	number, err := githubPayloadPositiveNumber(pull, "number")
	if err != nil {
		return nil, false, fmt.Errorf("GitHub event pull_request: %w", err)
	}
	head, err := githubPullRef(pull, "head")
	if err != nil {
		return nil, false, fmt.Errorf("GitHub event pull_request: %w", err)
	}
	base, err := githubPullRef(pull, "base")
	if err != nil {
		return nil, false, fmt.Errorf("GitHub event pull_request: %w", err)
	}
	return map[string]any{"number": number, "head": head, "base": base}, true, nil
}

func githubPayloadPositiveNumber(object map[string]any, name string) (any, error) {
	value, exists := object[name]
	if !exists {
		return nil, fmt.Errorf("%s is required", name)
	}
	switch typed := value.(type) {
	case int64:
		if typed > 0 {
			return typed, nil
		}
	case uint64:
		if typed > 0 {
			return typed, nil
		}
	}
	return nil, fmt.Errorf("%s must be a positive integer", name)
}

func githubPullRef(pull map[string]any, name string) (map[string]any, error) {
	value, exists := pull[name]
	if !exists {
		return nil, fmt.Errorf("%s is required", name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	sha, err := githubPayloadString(object, "sha")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	ref, err := githubPayloadString(object, "ref")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return map[string]any{"sha": sha, "ref": ref}, nil
}

func githubPayloadString(object map[string]any, name string) (string, error) {
	value, exists := object[name]
	if !exists {
		return "", fmt.Errorf("%s is required", name)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", name)
	}
	return text, nil
}
