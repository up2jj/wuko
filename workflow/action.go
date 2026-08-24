package workflow

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/process"
	"gopkg.in/yaml.v3"
)

const (
	maxManifestSize = 1 << 20
	maxArchiveSize  = 20 << 20
	maxExtracted    = 50 << 20
	maxEntries      = 1000
	dynamicRunDir   = "__wuko_runtime_working_directory__"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var queryInErrorPattern = regexp.MustCompile(`\?[^\s"']+`)

// Action is a resolved Wuko composite action.
type Action struct {
	Version     int                           `yaml:"version"`
	Name        string                        `yaml:"name"`
	Description string                        `yaml:"description,omitempty"`
	Templates   map[string]TemplateDefinition `yaml:"templates,omitempty"`
	Inputs      map[string]ActionInput        `yaml:"inputs,omitempty"`
	Outputs     map[string]ActionOutput       `yaml:"outputs,omitempty"`
	Steps       []Step                        `yaml:"steps"`
	Finally     []Step                        `yaml:"finally,omitempty"`
	Dir         string                        `yaml:"-"`
	Files       map[string]ActionFile         `yaml:"-"`
	Location    diagnostic.Location           `yaml:"-"`
}

// ActionInput declares one typed action input.
type ActionInput struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Default     any    `yaml:"default,omitempty"`
	HasDefault  bool   `yaml:"-"`
}

func (input *ActionInput) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("input declaration must be an object")
	}
	allowed := map[string]bool{"type": true, "description": true, "required": true, "default": true}
	for i := 0; i < len(node.Content); i += 2 {
		if !allowed[node.Content[i].Value] {
			return fmt.Errorf("field %s not found in action input", node.Content[i].Value)
		}
	}
	type rawInput struct {
		Type        string `yaml:"type"`
		Description string `yaml:"description,omitempty"`
		Required    bool   `yaml:"required,omitempty"`
		Default     any    `yaml:"default,omitempty"`
	}
	var raw rawInput
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*input = ActionInput{Type: raw.Type, Description: raw.Description, Required: raw.Required, Default: raw.Default}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == "default" {
			input.HasDefault = true
		}
	}
	return nil
}

// ActionOutput declares one exported action output expression.
type ActionOutput struct {
	Description string `yaml:"description,omitempty"`
	Value       string `yaml:"value"`
}

// ActionFile is one validated file from an action archive.
type ActionFile struct {
	Data []byte
	Mode os.FileMode
}

// Materialize writes an archived action to an isolated temporary directory.
func (action *Action) Materialize() (string, func(), error) {
	if len(action.Files) == 0 {
		return action.Dir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "wuko-action-")
	if err != nil {
		return "", nil, fmt.Errorf("creating action directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for name, file := range action.Files {
		if err := validateArchivePath(name); err != nil {
			cleanup()
			return "", nil, err
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("creating action directory for %s: %w", name, err)
		}
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(target, file.Data, mode); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing action file %s: %w", name, err)
		}
	}
	return dir, cleanup, nil
}

// Loader resolves composite actions while loading a workflow.
type Loader struct {
	client *http.Client
}

// NewLoader constructs a loader. The supplied client's transport is retained, while remote
// action timeouts and redirect policy are enforced by the loader.
func NewLoader(client *http.Client) *Loader {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.Timeout = 30 * time.Second
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" {
			return fmt.Errorf("redirected to non-HTTPS URL")
		}
		if request.URL.User != nil {
			return fmt.Errorf("redirected to URL containing user information")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	return &Loader{client: &copy}
}

// Decode reads and validates a local workflow without resolving composite actions. Call Prepare
// before execution. This split lets callers collect values from optional adapters first.
func (loader *Loader) Decode(filename string, options LoadOptions) (*Definition, error) {
	definition, err := loadLocalWithDiagnostics(filename, options.Diagnostics, options.sourceRoot, options.sourceLabel)
	if err != nil {
		return nil, err
	}
	return definition, nil
}

// Prepare resolves value-dependent workflow environment and composite actions in a decoded definition.
func (loader *Loader) Prepare(ctx context.Context, definition *Definition, options LoadOptions) error {
	if options.sourceRoot == "" {
		options.sourceRoot = definition.sourceRoot
	}
	if options.sourceLabel == "" {
		options.sourceLabel = definition.sourceLabel
	}
	displaySource := definition.Path
	if options.sourceLabel != "" {
		displaySource = options.sourceLabel
	}
	if err := validateDependencyRuntimeOnly(definition); err != nil {
		return fmt.Errorf("validating workflow %s: %w", displaySource, err)
	}
	valuesStarted := traceStart(options.Diagnostics, diagnostic.PhaseValues, definition.Location, definition.Name, "", "", "preparing workflow values")
	vars, environment, err := PrepareValues(definition, options)
	if err != nil {
		traceFinish(options.Diagnostics, valuesStarted, diagnostic.PhaseValues, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return err
	}
	traceFinish(options.Diagnostics, valuesStarted, diagnostic.PhaseValues, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil, countAttr("variables", len(vars)), countAttr("environment", len(environment)))
	data := TemplateData(definition, options.RunDir, nil, vars, environment, nil)
	renderer, err := NewRenderer(definition.Templates)
	if err != nil {
		return err
	}
	cache := make(map[string]*Action)
	if err := loader.resolveActions(ctx, definition.Name, definition.Steps, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Finally, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Install, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Uninstall, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, options.Diagnostics); err != nil {
		return err
	}
	return nil
}

// Load reads a local workflow, expands required step files, and resolves all composite actions.
func (loader *Loader) Load(ctx context.Context, filename string, options LoadOptions) (*Definition, error) {
	displaySource := filename
	if options.sourceLabel != "" {
		displaySource = remapSource(filename, options.sourceRoot, options.sourceLabel)
	}
	started := traceStart(options.Diagnostics, diagnostic.PhaseLoad, diagnostic.Location{Source: displaySource}, "", "", "", "loading workflow")
	definition, err := loader.Decode(filename, options)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, diagnostic.Location{Source: displaySource}, "", "", "", "", nil)
		return nil, err
	}
	targetName := options.Target
	if options.Lifecycle && targetName == "" && definition.HasTargets() {
		targetName = definition.TargetNames()[0]
	}
	selected, err := definition.SelectTarget(targetName)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", err)
		return nil, err
	}
	definition = selected
	if err := loader.Prepare(ctx, definition, options); err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusFailed, definition.Location, definition.Name, "", "", "", nil)
		return nil, err
	}
	traceFinish(options.Diagnostics, started, diagnostic.PhaseLoad, diagnostic.StatusSucceeded, definition.Location, definition.Name, "", "", "", nil, countAttr("steps", len(definition.Steps)))
	return definition, nil
}

func (loader *Loader) resolveActions(ctx context.Context, workflowName string, steps []Step, renderer *Renderer, data map[string]any, environment map[string]string, runDir string, runDirKnown bool, definitionPath, sourceRoot, sourceLabel string, cache map[string]*Action, reporter diagnostic.Reporter) error {
	for i := range steps {
		workflowStep := &steps[i]
		if !workflowStep.IsExecutorBlock() && workflowStep.IsWorkingDirectoryBlock() {
			childData, childRunDir, childRunDirKnown := actionWorkingDirectoryScope(renderer, data, runDir, runDirKnown, workflowStep.WorkingDirectory)
			if err := loader.resolveActions(ctx, workflowName, workflowStep.Steps, renderer, childData, environment, childRunDir, childRunDirKnown, definitionPath, sourceRoot, sourceLabel, cache, reporter); err != nil {
				return err
			}
			continue
		}
		if children := workflowStep.ChildSequences(); len(children) > 0 {
			for _, child := range children {
				if err := loader.resolveActions(ctx, workflowName, child.Steps, renderer, data, environment, runDir, runDirKnown, definitionPath, sourceRoot, sourceLabel, cache, reporter); err != nil {
					return err
				}
			}
			if workflowStep.IsExecutorBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
				continue
			}
		}
		if workflowStep.Uses.Empty() {
			continue
		}
		started := traceStart(reporter, diagnostic.PhaseActionResolve, workflowStep.Location, workflowName, workflowStep.ID, "uses", "resolving composite action")
		declarationPath := workflowStep.sourcePath
		if declarationPath == "" {
			declarationPath = definitionPath
		}
		resolution, err := loader.resolveSource(ctx, workflowStep.Uses, renderer, data, environment, runDir, runDirKnown, declarationPath, sourceRoot, sourceLabel)
		if err != nil {
			traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
			return fmt.Errorf("step %q uses: %w", workflowStep.ID, err)
		}
		if resolution.local && workflowStep.SHA256 != "" {
			err := fmt.Errorf("sha256 is not supported for local actions")
			traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		if !resolution.local && workflowStep.SHA256 != "" && !sha256Pattern.MatchString(workflowStep.SHA256) {
			err := fmt.Errorf("sha256 must be a 64-character hexadecimal digest")
			traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
			return fmt.Errorf("step %q: %w", workflowStep.ID, err)
		}
		key := resolution.key + "\x00" + strings.ToLower(workflowStep.SHA256)
		action := cache[key]
		if action == nil {
			fetchStarted := traceStart(reporter, diagnostic.PhaseActionFetch, workflowStep.Location, workflowName, workflowStep.ID, "uses", resolution.description)
			payload, err := resolution.fetch()
			if err != nil {
				traceFinish(reporter, fetchStarted, diagnostic.PhaseActionFetch, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
				traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", nil)
				return fmt.Errorf("step %q uses: %w", workflowStep.ID, err)
			}
			traceFinish(reporter, fetchStarted, diagnostic.PhaseActionFetch, diagnostic.StatusSucceeded, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", nil, countAttr("bytes", len(payload)))
			if !resolution.local {
				checksumStarted := traceStart(reporter, diagnostic.PhaseActionChecksum, workflowStep.Location, workflowName, workflowStep.ID, "uses", "verifying action checksum")
				if err := verifyChecksum(payload, workflowStep.SHA256); err != nil {
					traceFinish(reporter, checksumStarted, diagnostic.PhaseActionChecksum, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
					traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", nil)
					return fmt.Errorf("step %q: %w", workflowStep.ID, err)
				}
				traceFinish(reporter, checksumStarted, diagnostic.PhaseActionChecksum, diagnostic.StatusSucceeded, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", nil)
			}
			decodeStarted := traceStart(reporter, diagnostic.PhaseActionDecode, workflowStep.Location, workflowName, workflowStep.ID, "uses", resolution.description)
			if resolution.local {
				if isZIP(payload) || len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
					err = fmt.Errorf("local action path must reference a YAML manifest; archives are not supported")
				} else {
					action, err = decodeAction(payload, "local action manifest", resolution.actionDir, nil, resolution.description, resolution.actionDir)
				}
			} else {
				action, err = decodeActionPayload(payload, filepath.Dir(definitionPath), resolution.description)
			}
			if err != nil {
				traceFinish(reporter, decodeStarted, diagnostic.PhaseActionDecode, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", err)
				traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusFailed, workflowStep.Location, workflowName, workflowStep.ID, "uses", "", nil)
				return fmt.Errorf("step %q action %s: %w", workflowStep.ID, resolution.description, err)
			}
			traceFinish(reporter, decodeStarted, diagnostic.PhaseActionDecode, diagnostic.StatusSucceeded, action.Location, workflowName, workflowStep.ID, "uses", action.Name, nil, countAttr("steps", len(action.Steps)))
			cache[key] = action
		} else {
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseActionFetch, Status: diagnostic.StatusSkipped, WorkflowName: workflowName, StepID: workflowStep.ID, StepType: "uses", Location: workflowStep.Location, Message: "using cached action"})
		}
		workflowStep.Uses = resolution.source
		workflowStep.Action = action
		traceFinish(reporter, started, diagnostic.PhaseActionResolve, diagnostic.StatusSucceeded, workflowStep.Location, workflowName, workflowStep.ID, "uses", action.Name, nil)
	}
	return nil
}

func actionWorkingDirectoryScope(renderer *Renderer, data map[string]any, runDir string, runDirKnown bool, value string) (map[string]any, string, bool) {
	if runDirKnown {
		rendered, err := renderer.Render(value, data)
		if err == nil && strings.TrimSpace(rendered) != "" && !strings.Contains(rendered, "<no value>") {
			dir := rendered
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(runDir, dir)
			}
			dir = filepath.Clean(dir)
			return templateDataWithRunDir(data, dir), dir, true
		}
	}
	return templateDataWithRunDir(data, dynamicRunDir), "", false
}

func templateDataWithRunDir(data map[string]any, runDir string) map[string]any {
	result := CloneMap(data)
	result["run"] = map[string]any{"dir": runDir}
	return result
}

type actionSourceResolution struct {
	source      ActionSource
	key         string
	description string
	fetch       func() ([]byte, error)
	actionDir   string
	local       bool
}

func (loader *Loader) resolveSource(ctx context.Context, source ActionSource, renderer *Renderer, data map[string]any, environment map[string]string, runDir string, runDirKnown bool, declarationPath, sourceRoot, sourceLabel string) (actionSourceResolution, error) {
	if source.URL != "" || source.Path != "" {
		reference := source.URL
		if reference == "" {
			reference = source.Path
		}
		resolved, err := renderer.Render(reference, data)
		if err != nil {
			return actionSourceResolution{}, err
		}
		if strings.TrimSpace(resolved) == "" {
			return actionSourceResolution{}, fmt.Errorf("rendered action source is empty")
		}
		if !runDirKnown && strings.Contains(resolved, dynamicRunDir) {
			return actionSourceResolution{}, fmt.Errorf("action source depends on a working_directory that is resolved at runtime")
		}
		if filepath.IsAbs(resolved) {
			return actionSourceResolution{}, fmt.Errorf("local action path %q must be relative", resolved)
		}
		parsed, parseErr := url.Parse(resolved)
		if source.URL != "" || strings.Contains(resolved, "://") || parseErr == nil && parsed.Scheme != "" {
			remoteURL, err := validateActionURL(resolved)
			if err != nil {
				return actionSourceResolution{}, err
			}
			return actionSourceResolution{
				source: ActionSource{URL: resolved},
				key:    "url\x00" + remoteURL.String(), description: safeURL(remoteURL),
				fetch: func() ([]byte, error) { return loader.fetch(ctx, remoteURL) },
			}, nil
		}
		return resolveLocalActionSource(resolved, declarationPath, sourceRoot, sourceLabel)
	}
	if !runDirKnown {
		return actionSourceResolution{}, fmt.Errorf("command action source requires a working_directory that can be resolved while loading the workflow")
	}

	command, err := renderer.Render(source.Command, data)
	if err != nil {
		return actionSourceResolution{}, fmt.Errorf("rendering command: %w", err)
	}
	if strings.TrimSpace(command) == "" {
		return actionSourceResolution{}, fmt.Errorf("rendered command is empty")
	}
	args := make([]string, len(source.Args))
	for i, argument := range source.Args {
		args[i], err = renderer.Render(argument, data)
		if err != nil {
			return actionSourceResolution{}, fmt.Errorf("rendering command argument %d: %w", i+1, err)
		}
	}
	resolved := ActionSource{Command: command, Args: args}
	keyData, err := json.Marshal(resolved)
	if err != nil {
		return actionSourceResolution{}, fmt.Errorf("encoding command source: %w", err)
	}
	fetch := func() ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, runErr := process.Run(commandCtx, process.Options{
			Command: command, Args: args, Dir: runDir, Env: environment, CaptureLimit: maxArchiveSize + 1,
		})
		if result.StdoutTruncated || len(result.Stdout) > maxArchiveSize {
			return nil, fmt.Errorf("action command %q output exceeds %d-byte download limit", command, maxArchiveSize)
		}
		if runErr != nil {
			message := fmt.Sprintf("action command %q failed: %v", command, runErr)
			if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
				const diagnosticLimit = 4096
				if len(stderr) > diagnosticLimit {
					stderr = stderr[:diagnosticLimit] + "…"
				}
				message += ": " + stderr
			}
			return nil, fmt.Errorf("%s", message)
		}
		return []byte(result.Stdout), nil
	}
	return actionSourceResolution{
		source:      resolved,
		key:         "command\x00" + runDir + "\x00" + string(keyData),
		description: fmt.Sprintf("command %q", command), fetch: fetch,
	}, nil
}

func resolveLocalActionSource(reference, declarationPath, sourceRoot, sourceLabel string) (actionSourceResolution, error) {
	if filepath.IsAbs(reference) {
		return actionSourceResolution{}, fmt.Errorf("local action path %q must be relative", reference)
	}
	manifestPath := filepath.Join(filepath.Dir(declarationPath), filepath.FromSlash(reference))
	info, err := os.Stat(manifestPath)
	if err != nil {
		return actionSourceResolution{}, fmt.Errorf("locating local action %q: %w", reference, err)
	}
	if info.IsDir() {
		manifestPath, err = localActionManifest(manifestPath)
		if err != nil {
			return actionSourceResolution{}, fmt.Errorf("locating local action %q: %w", reference, err)
		}
	} else if !info.Mode().IsRegular() {
		return actionSourceResolution{}, fmt.Errorf("local action path %q must reference a regular file or directory", reference)
	}
	manifestPath, err = canonicalFilePath(manifestPath)
	if err != nil {
		return actionSourceResolution{}, fmt.Errorf("resolving local action %q: %w", reference, err)
	}
	description := manifestPath
	if sourceLabel != "" {
		logicalRoot := sourceRoot
		if canonicalRoot, canonicalErr := filepath.EvalSymlinks(sourceRoot); canonicalErr == nil {
			logicalRoot = canonicalRoot
		}
		description = remapSource(manifestPath, logicalRoot, sourceLabel)
	}
	return actionSourceResolution{
		source: ActionSource{Path: reference},
		key:    "path\x00" + manifestPath, description: description,
		fetch:     func() ([]byte, error) { return readLocalActionManifest(manifestPath) },
		actionDir: filepath.Dir(manifestPath), local: true,
	}, nil
}

func localActionManifest(directory string) (string, error) {
	var manifest string
	for _, name := range []string{"action.yml", "action.yaml"} {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s must be a regular file", name)
		}
		if manifest != "" {
			return "", fmt.Errorf("directory contains both action.yml and action.yaml")
		}
		manifest = candidate
	}
	if manifest == "" {
		return "", fmt.Errorf("directory must contain action.yml or action.yaml")
	}
	return manifest, nil
}

func readLocalActionManifest(manifestPath string) ([]byte, error) {
	file, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading local action manifest: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading local action manifest: %w", err)
	}
	if len(payload) > maxManifestSize {
		return nil, fmt.Errorf("local action manifest exceeds %d-byte limit", maxManifestSize)
	}
	return payload, nil
}

func validateActionURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("URL must use HTTPS and include a host")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("URL user information is not allowed")
	}
	return parsed, nil
}

func (loader *Loader) fetch(ctx context.Context, remoteURL *url.URL) ([]byte, error) {
	return loader.fetchWithHeaders(ctx, remoteURL, nil, "action")
}

func (loader *Loader) fetchWithHeaders(ctx context.Context, remoteURL *url.URL, headers http.Header, kind string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating %s request for %s: %w", kind, safeURL(remoteURL), err)
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := loader.client.Do(request)
	if err != nil {
		message := strings.ReplaceAll(err.Error(), remoteURL.String(), safeURL(remoteURL))
		message = queryInErrorPattern.ReplaceAllString(message, "")
		return nil, fmt.Errorf("fetching %s %s: %s", kind, safeURL(remoteURL), message)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching %s %s: unexpected HTTP status %s", kind, safeURL(remoteURL), response.Status)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s %s: %w", kind, safeURL(remoteURL), err)
	}
	if len(payload) > maxArchiveSize {
		return nil, fmt.Errorf("%s %s exceeds %d-byte download limit", kind, safeURL(remoteURL), maxArchiveSize)
	}
	return payload, nil
}

func safeURL(remoteURL *url.URL) string {
	copy := *remoteURL
	copy.RawQuery = ""
	copy.Fragment = ""
	return copy.String()
}

func verifyChecksum(payload []byte, expected string) error {
	if expected == "" {
		return nil
	}
	digest := sha256.Sum256(payload)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), expected) {
		return fmt.Errorf("action SHA-256 checksum does not match")
	}
	return nil
}

func decodeActionPayload(payload []byte, callerDir, source string) (*Action, error) {
	switch {
	case isZIP(payload):
		manifest, files, err := unpackZIP(payload)
		if err != nil {
			return nil, err
		}
		return decodeAction(manifest, "archived action", "", files, archivedActionSource(source, files), "")
	case len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b:
		manifest, files, err := unpackTarGzip(payload)
		if err != nil {
			return nil, err
		}
		return decodeAction(manifest, "archived action", "", files, archivedActionSource(source, files), "")
	default:
		if len(payload) > maxManifestSize {
			return nil, fmt.Errorf("manifest exceeds %d-byte limit", maxManifestSize)
		}
		return decodeAction(payload, "action manifest", callerDir, nil, source, "")
	}
}

func archivedActionSource(source string, files map[string]ActionFile) string {
	if _, ok := files["action.yaml"]; ok {
		return source + "::action.yaml"
	}
	return source + "::action.yml"
}

func decodeAction(data []byte, description, dir string, files map[string]ActionFile, logicalSource, localFileRoot string) (*Action, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var action Action
	if err := decoder.Decode(&action); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", description, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding %s: multiple YAML documents are not supported", description)
		}
		return nil, fmt.Errorf("decoding %s: %w", description, err)
	}
	action.Dir = dir
	action.Files = files
	if action.Files == nil && localFileRoot == "" {
		for name, definition := range action.Templates {
			if definition.File != "" {
				return nil, fmt.Errorf("loading %s templates: template %q file %q requires a packaged action", description, name, definition.File)
			}
		}
	}
	if err := resolveTemplateFiles(action.Templates, action.Dir, action.Files, localFileRoot); err != nil {
		return nil, fmt.Errorf("loading %s templates: %w", description, err)
	}
	annotateActionLocations(data, &action, logicalSource)
	if err := validateAction(&action); err != nil {
		return nil, fmt.Errorf("validating %s: %w", description, err)
	}
	return &action, nil
}

func validateAction(action *Action) error {
	if action.Version != 1 {
		return fmt.Errorf("unsupported version %d (want 1)", action.Version)
	}
	if strings.TrimSpace(action.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(action.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	if _, err := NewRenderer(action.Templates); err != nil {
		return err
	}
	for name, input := range action.Inputs {
		if !identifierPattern.MatchString(name) {
			return fmt.Errorf("invalid input name %q", name)
		}
		switch input.Type {
		case "string", "boolean", "number", "array", "object":
		default:
			return fmt.Errorf("input %q has unsupported type %q", name, input.Type)
		}
		if input.Required && input.HasDefault {
			return fmt.Errorf("input %q cannot be required and have a default", name)
		}
		if input.HasDefault && !actionValueMatches(input.Type, input.Default) {
			return fmt.Errorf("input %q default does not match type %s", name, input.Type)
		}
		if input.HasDefault && !ActionDataValue(input.Default) {
			return fmt.Errorf("input %q default is not a YAML/JSON-compatible value", name)
		}
	}
	for name, output := range action.Outputs {
		if !identifierPattern.MatchString(name) {
			return fmt.Errorf("invalid output name %q", name)
		}
		if strings.TrimSpace(output.Value) == "" {
			return fmt.Errorf("output %q value is required", name)
		}
	}
	definition := &Definition{Version: 1, Name: action.Name, Steps: action.Steps, Finally: action.Finally}
	if err := validateDefinitionStructure(definition, false); err != nil {
		return err
	}
	return action.ValidateReturnContracts()
}

// ValidateReturnContracts checks that every early return satisfies the declared action outputs.
func (action *Action) ValidateReturnContracts() error {
	return validateActionReturnContracts(action.Steps, action.Outputs)
}

func validateActionReturnContracts(steps []Step, outputs map[string]ActionOutput) error {
	for _, workflowStep := range steps {
		if workflowStep.IsExecutorBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsWorktreeBlock() || workflowStep.IsConditionalBlock() {
			for _, child := range workflowStep.ChildSequences() {
				if child.Role == ChildFinally || child.Role == ChildDefer {
					continue
				}
				if err := validateActionReturnContracts(child.Steps, outputs); err != nil {
					return err
				}
			}
			continue
		}
		if workflowStep.Return == nil {
			continue
		}
		for name := range outputs {
			if _, exists := workflowStep.Return.Outputs[name]; !exists {
				return fmt.Errorf("return outputs do not match action outputs: missing %q", name)
			}
		}
		for name := range workflowStep.Return.Outputs {
			if _, exists := outputs[name]; !exists {
				return fmt.Errorf("return outputs do not match action outputs: unexpected %q", name)
			}
		}
	}
	return nil
}

// ActionValueMatches reports whether a value satisfies a manifest input type.
func ActionValueMatches(kind string, value any) bool { return actionValueMatches(kind, value) }

// ActionDataValue reports whether value can cross an action input/output boundary.
func ActionDataValue(value any) bool {
	switch typed := value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	case []any:
		for _, item := range typed {
			if !ActionDataValue(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range typed {
			if !ActionDataValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func actionValueMatches(kind string, value any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			return true
		}
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	}
	return false
}

func unpackZIP(payload []byte) ([]byte, map[string]ActionFile, error) {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, nil, fmt.Errorf("opening ZIP action: %w", err)
	}
	if len(reader.File) > maxEntries {
		return nil, nil, fmt.Errorf("archive exceeds %d-entry limit", maxEntries)
	}
	files := make(map[string]ActionFile)
	seen := make(map[string]struct{})
	var total int64
	for _, entry := range reader.File {
		if err := validateArchivePath(entry.Name); err != nil {
			return nil, nil, err
		}
		cleanName := strings.TrimSuffix(entry.Name, "/")
		if _, exists := seen[cleanName]; exists {
			return nil, nil, fmt.Errorf("archive contains duplicate path %q", entry.Name)
		}
		seen[cleanName] = struct{}{}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return nil, nil, fmt.Errorf("archive entry %q is not a regular file or directory", entry.Name)
		}
		if mode.IsDir() {
			continue
		}
		content, err := readArchiveFile(entry.Open, &total)
		if err != nil {
			return nil, nil, fmt.Errorf("reading archive entry %q: %w", entry.Name, err)
		}
		files[entry.Name] = ActionFile{Data: content, Mode: mode}
	}
	return archiveManifest(files)
}

func unpackTarGzip(payload []byte) ([]byte, map[string]ActionFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("opening gzip action: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := make(map[string]ActionFile)
	seen := make(map[string]struct{})
	var total int64
	entries := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading tar action: %w", err)
		}
		entries++
		if entries > maxEntries {
			return nil, nil, fmt.Errorf("archive exceeds %d-entry limit", maxEntries)
		}
		if err := validateArchivePath(header.Name); err != nil {
			return nil, nil, err
		}
		cleanName := strings.TrimSuffix(header.Name, "/")
		if _, exists := seen[cleanName]; exists {
			return nil, nil, fmt.Errorf("archive contains duplicate path %q", header.Name)
		}
		seen[cleanName] = struct{}{}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, nil, fmt.Errorf("archive entry %q is not a regular file or directory", header.Name)
		}
		content, err := readArchiveFile(func() (io.ReadCloser, error) { return io.NopCloser(reader), nil }, &total)
		if err != nil {
			return nil, nil, fmt.Errorf("reading archive entry %q: %w", header.Name, err)
		}
		files[header.Name] = ActionFile{Data: content, Mode: os.FileMode(header.Mode)}
	}
	return archiveManifest(files)
}

func validateArchivePath(name string) error {
	cleanName := strings.TrimSuffix(name, "/")
	if cleanName == "" || cleanName == "." || strings.Contains(name, "\\") || path.IsAbs(cleanName) || path.Clean(cleanName) != cleanName || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return fmt.Errorf("archive contains unsafe path %q", name)
	}
	return nil
}

func isZIP(payload []byte) bool {
	if len(payload) < 4 || payload[0] != 'P' || payload[1] != 'K' {
		return false
	}
	return (payload[2] == 3 && payload[3] == 4) || (payload[2] == 5 && payload[3] == 6) || (payload[2] == 7 && payload[3] == 8)
}

func readArchiveFile(open func() (io.ReadCloser, error), total *int64) ([]byte, error) {
	reader, err := open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	remaining := int64(maxExtracted) - *total
	if remaining < 0 {
		return nil, fmt.Errorf("archive exceeds %d-byte extracted limit", maxExtracted)
	}
	content, err := io.ReadAll(io.LimitReader(reader, remaining+1))
	if err != nil {
		return nil, err
	}
	*total += int64(len(content))
	if *total > maxExtracted {
		return nil, fmt.Errorf("archive exceeds %d-byte extracted limit", maxExtracted)
	}
	return content, nil
}

func archiveManifest(files map[string]ActionFile) ([]byte, map[string]ActionFile, error) {
	var manifest []byte
	for _, name := range []string{"action.yml", "action.yaml"} {
		if file, ok := files[name]; ok {
			if manifest != nil {
				return nil, nil, fmt.Errorf("archive contains multiple action manifests")
			}
			manifest = file.Data
		}
	}
	if manifest == nil {
		return nil, nil, fmt.Errorf("archive must contain action.yml or action.yaml at its root")
	}
	if len(manifest) > maxManifestSize {
		return nil, nil, fmt.Errorf("manifest exceeds %d-byte limit", maxManifestSize)
	}
	return manifest, files, nil
}
