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

	"github.com/up2jj/wuko/process"
	"gopkg.in/yaml.v3"
)

const (
	maxManifestSize = 1 << 20
	maxArchiveSize  = 20 << 20
	maxExtracted    = 50 << 20
	maxEntries      = 1000
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var queryInErrorPattern = regexp.MustCompile(`\?[^\s"']+`)

// Action is a resolved Wuko composite action.
type Action struct {
	Version     int                     `yaml:"version"`
	Name        string                  `yaml:"name"`
	Description string                  `yaml:"description,omitempty"`
	Inputs      map[string]ActionInput  `yaml:"inputs,omitempty"`
	Outputs     map[string]ActionOutput `yaml:"outputs,omitempty"`
	Steps       []Step                  `yaml:"steps"`
	Dir         string                  `yaml:"-"`
	Files       map[string]ActionFile   `yaml:"-"`
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

// Loader resolves remote actions while loading a local workflow.
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

// Load reads a local workflow and resolves all of its remote actions before returning.
func (loader *Loader) Load(ctx context.Context, filename string, options LoadOptions) (*Definition, error) {
	definition, err := loadLocal(filename)
	if err != nil {
		return nil, err
	}
	vars, environment, err := PrepareValues(definition, options)
	if err != nil {
		return nil, err
	}
	data := TemplateData(definition, options.RunDir, nil, vars, environment, nil)
	cache := make(map[string]*Action)
	for i := range definition.Steps {
		workflowStep := &definition.Steps[i]
		if workflowStep.Uses.Empty() {
			continue
		}
		if workflowStep.SHA256 != "" && !sha256Pattern.MatchString(workflowStep.SHA256) {
			return nil, fmt.Errorf("step %q: sha256 must be a 64-character hexadecimal digest", workflowStep.ID)
		}
		resolved, key, sourceDescription, fetch, err := loader.resolveSource(ctx, workflowStep.Uses, data, environment, options.RunDir)
		if err != nil {
			return nil, fmt.Errorf("step %q uses: %w", workflowStep.ID, err)
		}
		key += "\x00" + strings.ToLower(workflowStep.SHA256)
		action := cache[key]
		if action == nil {
			payload, err := fetch()
			if err != nil {
				return nil, fmt.Errorf("step %q uses: %w", workflowStep.ID, err)
			}
			if err := verifyChecksum(payload, workflowStep.SHA256); err != nil {
				return nil, fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			action, err = decodeActionPayload(payload, definition.Dir)
			if err != nil {
				return nil, fmt.Errorf("step %q action %s: %w", workflowStep.ID, sourceDescription, err)
			}
			cache[key] = action
		}
		workflowStep.Uses = resolved
		workflowStep.Action = action
	}
	return definition, nil
}

func (loader *Loader) resolveSource(ctx context.Context, source ActionSource, data map[string]any, environment map[string]string, runDir string) (ActionSource, string, string, func() ([]byte, error), error) {
	if source.URL != "" {
		resolved, err := RenderString(source.URL, data)
		if err != nil {
			return ActionSource{}, "", "", nil, err
		}
		remoteURL, err := validateActionURL(resolved)
		if err != nil {
			return ActionSource{}, "", "", nil, err
		}
		return ActionSource{URL: resolved}, "url\x00" + remoteURL.String(), safeURL(remoteURL), func() ([]byte, error) {
			return loader.fetch(ctx, remoteURL)
		}, nil
	}

	command, err := RenderString(source.Command, data)
	if err != nil {
		return ActionSource{}, "", "", nil, fmt.Errorf("rendering command: %w", err)
	}
	if strings.TrimSpace(command) == "" {
		return ActionSource{}, "", "", nil, fmt.Errorf("rendered command is empty")
	}
	args := make([]string, len(source.Args))
	for i, argument := range source.Args {
		args[i], err = RenderString(argument, data)
		if err != nil {
			return ActionSource{}, "", "", nil, fmt.Errorf("rendering command argument %d: %w", i+1, err)
		}
	}
	resolved := ActionSource{Command: command, Args: args}
	keyData, err := json.Marshal(resolved)
	if err != nil {
		return ActionSource{}, "", "", nil, fmt.Errorf("encoding command source: %w", err)
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
	return resolved, "command\x00" + string(keyData), fmt.Sprintf("command %q", command), fetch, nil
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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request for %s: %w", safeURL(remoteURL), err)
	}
	response, err := loader.client.Do(request)
	if err != nil {
		message := strings.ReplaceAll(err.Error(), remoteURL.String(), safeURL(remoteURL))
		message = queryInErrorPattern.ReplaceAllString(message, "")
		return nil, fmt.Errorf("fetching action %s: %s", safeURL(remoteURL), message)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching action %s: unexpected HTTP status %s", safeURL(remoteURL), response.Status)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading action %s: %w", safeURL(remoteURL), err)
	}
	if len(payload) > maxArchiveSize {
		return nil, fmt.Errorf("action %s exceeds %d-byte download limit", safeURL(remoteURL), maxArchiveSize)
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

func decodeActionPayload(payload []byte, callerDir string) (*Action, error) {
	switch {
	case isZIP(payload):
		manifest, files, err := unpackZIP(payload)
		if err != nil {
			return nil, err
		}
		return decodeAction(manifest, "archived action", "", files)
	case len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b:
		manifest, files, err := unpackTarGzip(payload)
		if err != nil {
			return nil, err
		}
		return decodeAction(manifest, "archived action", "", files)
	default:
		if len(payload) > maxManifestSize {
			return nil, fmt.Errorf("manifest exceeds %d-byte limit", maxManifestSize)
		}
		return decodeAction(payload, "action manifest", callerDir, nil)
	}
}

func decodeAction(data []byte, source, dir string, files map[string]ActionFile) (*Action, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var action Action
	if err := decoder.Decode(&action); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", source, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding %s: multiple YAML documents are not supported", source)
		}
		return nil, fmt.Errorf("decoding %s: %w", source, err)
	}
	action.Dir = dir
	action.Files = files
	if err := validateAction(&action); err != nil {
		return nil, fmt.Errorf("validating %s: %w", source, err)
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
	definition := &Definition{Version: 1, Name: action.Name, Steps: action.Steps}
	return validateDefinition(definition, false)
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
