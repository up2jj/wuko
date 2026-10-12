package workflow

import (
	"fmt"
	"net/url"
	"strings"
)

type githubWorkflowLocatorKind uint8

const (
	githubWorkflowRepository githubWorkflowLocatorKind = iota
	githubWorkflowTree
	githubWorkflowBlob
)

// GitHubWorkflowLocator is a validated GitHub repository, tree, or workflow-file selector.
type GitHubWorkflowLocator struct {
	owner      string
	repository string
	ref        string
	path       string
	kind       githubWorkflowLocatorKind
	original   string
}

// ParseGitHubWorkflowLocator parses Wuko's github: shorthand and ordinary github.com repository,
// tree, and blob URLs. recognized is false for non-GitHub HTTPS URLs.
func ParseGitHubWorkflowLocator(value string) (locator GitHubWorkflowLocator, recognized bool, err error) {
	if strings.HasPrefix(value, "github:") {
		locator, err = parseGitHubWorkflowShorthand(value)
		return locator, true, err
	}
	parsed, parseErr := url.Parse(value)
	if parseErr != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return GitHubWorkflowLocator{}, false, nil
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	kind, supported := githubWorkflowURLKind(segments)
	if !supported {
		// Other github.com URLs, such as /raw/ files or release downloads, stay ordinary HTTPS
		// workflow URLs that GitHub redirects to their content.
		return GitHubWorkflowLocator{}, false, nil
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: use HTTPS github.com without credentials, a port, query, or fragment", value)
	}
	lowerEscaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(lowerEscaped, "%2f") || strings.Contains(lowerEscaped, "%5c") || strings.Contains(lowerEscaped, "%00") {
		return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: encoded path separators are not allowed", value)
	}
	if !validGitHubPart(segments[0]) {
		return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: expected https://github.com/owner/repository", value)
	}
	repository := strings.TrimSuffix(segments[1], ".git")
	if !validGitHubPart(repository) {
		return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: repository is invalid", value)
	}
	locator = GitHubWorkflowLocator{owner: segments[0], repository: repository, path: defaultRemoteWorkflowFile, kind: kind, original: value}
	switch kind {
	case githubWorkflowBlob:
		locator.ref, locator.path = segments[3], strings.Join(segments[4:], "/")
	case githubWorkflowTree:
		if len(segments) < 4 {
			return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: tree ref is required", value)
		}
		locator.ref, locator.path = segments[3], strings.Join(segments[4:], "/")
	}
	if locator.ref != "" && (!validGitHubRef(locator.ref) || strings.Contains(locator.ref, "/")) {
		return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: URL refs must be one path segment; use github:owner/repo@ref:path for refs containing slashes", value)
	}
	if locator.kind == githubWorkflowTree && locator.path == "" {
		locator.path = "."
	}
	if locator.path != "." {
		if err := validateRemotePath(locator.path); err != nil {
			return GitHubWorkflowLocator{}, true, fmt.Errorf("invalid GitHub workflow URL %q: %w", value, err)
		}
	}
	return locator, true, nil
}

// githubWorkflowURLKind classifies the path segments of a github.com URL. Only repository,
// /tree/, and /blob/ URLs are workflow locators.
func githubWorkflowURLKind(segments []string) (githubWorkflowLocatorKind, bool) {
	switch {
	case len(segments) == 2:
		return githubWorkflowRepository, true
	case len(segments) >= 5 && segments[2] == "blob":
		return githubWorkflowBlob, true
	case len(segments) >= 3 && segments[2] == "tree":
		return githubWorkflowTree, true
	default:
		return 0, false
	}
}

func parseGitHubWorkflowShorthand(value string) (GitHubWorkflowLocator, error) {
	raw := strings.TrimPrefix(value, "github:")
	if raw == "" || strings.TrimSpace(raw) != raw {
		return GitHubWorkflowLocator{}, fmt.Errorf("invalid GitHub workflow locator %q: owner/repo is required", value)
	}
	repositoryPart, workflowPath, hasPath := strings.Cut(raw, ":")
	if hasPath && workflowPath == "" {
		return GitHubWorkflowLocator{}, fmt.Errorf("invalid GitHub workflow locator %q: path is empty", value)
	}
	if !hasPath {
		workflowPath = defaultRemoteWorkflowFile
	}
	ownerRepository, ref, hasRef := strings.Cut(repositoryPart, "@")
	if hasRef && !validGitHubRef(ref) {
		return GitHubWorkflowLocator{}, fmt.Errorf("invalid GitHub workflow locator %q: ref is invalid", value)
	}
	parts := strings.Split(ownerRepository, "/")
	if len(parts) != 2 || !validGitHubPart(parts[0]) || !validGitHubPart(parts[1]) {
		return GitHubWorkflowLocator{}, fmt.Errorf("invalid GitHub workflow locator %q: expected owner/repo[@ref][:path]", value)
	}
	if err := validateRemotePath(workflowPath); err != nil {
		return GitHubWorkflowLocator{}, fmt.Errorf("invalid GitHub workflow locator %q: %w", value, err)
	}
	kind := githubWorkflowBlob
	if !hasPath {
		kind = githubWorkflowRepository
	}
	return GitHubWorkflowLocator{owner: parts[0], repository: parts[1], ref: ref, path: workflowPath, kind: kind, original: value}, nil
}

func validGitHubRef(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "@") {
		return false
	}
	return validateRemotePath(value) == nil
}

// Canonical returns the unambiguous locator used in diagnostics and reproducible commands.
func (locator GitHubWorkflowLocator) Canonical() string {
	value := "github:" + locator.owner + "/" + locator.repository
	if locator.ref != "" {
		value += "@" + locator.ref
	}
	if locator.kind == githubWorkflowTree {
		if locator.path != "." {
			value += ":" + locator.path
		}
		return value
	}
	if locator.path != defaultRemoteWorkflowFile || locator.kind == githubWorkflowBlob {
		value += ":" + locator.path
	}
	return value
}

func (locator GitHubWorkflowLocator) repositoryString() string {
	value := locator.owner + "/" + locator.repository
	if locator.ref != "" {
		value += "@" + locator.ref
	}
	return value
}

func (locator GitHubWorkflowLocator) isTree() bool { return locator.kind == githubWorkflowTree }

func (locator GitHubWorkflowLocator) direct(workflowPath string) GitHubWorkflowLocator {
	locator.kind = githubWorkflowBlob
	locator.path = workflowPath
	return locator
}
