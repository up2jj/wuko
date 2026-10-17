package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/secret"
)

// Loader resolves composite actions while loading a workflow.
type Loader struct {
	client              *http.Client
	githubStoredTokenFn func(context.Context, map[string]string) string
	pluginHelpers       PluginHelperLoader
	pluginResolver      PluginSourceResolver
	pluginActions       PluginActionLoader
}

// PluginHelperLoader initializes helpers declared by one workflow.
type PluginHelperLoader interface {
	LoadHelpers(context.Context, map[string]ResolvedPluginSource) (helper.Set, error)
}

// PluginActionLoader retrieves an immutable action JSON object from an initialized plugin.
type PluginActionLoader interface {
	LoadAction(context.Context, string, string) (json.RawMessage, error)
}

// LoaderOption customizes workflow loading.
type LoaderOption func(*Loader)

// WithPluginHelpers enables helpers from explicitly declared plugins.
func WithPluginHelpers(loader PluginHelperLoader) LoaderOption {
	return func(workflowLoader *Loader) { workflowLoader.pluginHelpers = loader }
}

// WithPluginResolver enables lock and cache resolution before helper loading.
func WithPluginResolver(resolver PluginSourceResolver) LoaderOption {
	return func(loader *Loader) { loader.pluginResolver = resolver }
}

// WithPluginActions enables plugin:namespace/action composite-action sources.
func WithPluginActions(loader PluginActionLoader) LoaderOption {
	return func(workflowLoader *Loader) { workflowLoader.pluginActions = loader }
}

// NewLoader constructs a loader. The supplied client's transport is retained, while remote
// action timeouts and redirect policy are enforced by the loader.
func NewLoader(client *http.Client, options ...LoaderOption) *Loader {
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
		if !strings.EqualFold(request.URL.Hostname(), "api.github.com") {
			request.Header.Del("Authorization")
		}
		return nil
	}
	loader := &Loader{client: &copy, githubStoredTokenFn: githubStoredToken}
	for _, option := range options {
		option(loader)
	}
	return loader
}

// Decode reads and validates a local workflow without resolving composite actions. Call Prepare
// before execution. This split lets callers collect values from optional adapters first.
func (loader *Loader) Decode(filename string, options LoadOptions) (*Definition, error) {
	return loader.decode(context.Background(), filename, options)
}

// DecodeContext reads and validates a local workflow, initializing declared helpers with ctx.
func (loader *Loader) DecodeContext(ctx context.Context, filename string, options LoadOptions) (*Definition, error) {
	return loader.decode(ctx, filename, options)
}

func (loader *Loader) decode(ctx context.Context, filename string, options LoadOptions) (*Definition, error) {
	definition, err := loadLocalWithDiagnosticsAndHelpers(ctx, filename, options.Diagnostics, options.sourceRoot, options.sourceLabel, loader.pluginHelpers, loader.pluginResolver, options.remoteOrigin)
	if err != nil {
		return nil, err
	}
	return definition, nil
}

// DecodeStdin reads and validates a workflow supplied on standard input without resolving
// composite actions. Relative workflow resources are resolved from baseDir. Call Prepare before
// execution.
func (loader *Loader) DecodeStdin(reader io.Reader, baseDir string, options LoadOptions) (*Definition, error) {
	return loader.DecodeStdinContext(context.Background(), reader, baseDir, options)
}

// DecodeStdinContext decodes stdin and initializes declared plugin helpers with ctx.
func (loader *Loader) DecodeStdinContext(ctx context.Context, reader io.Reader, baseDir string, options LoadOptions) (*Definition, error) {
	const sourceLabel = "stdin"
	started := traceStart(options.Diagnostics, diagnostic.PhaseDecode, diagnostic.Location{Source: sourceLabel}, "", "", "", "decoding workflow")
	if reader == nil {
		err := fmt.Errorf("standard input is required")
		traceFinish(options.Diagnostics, started, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: sourceLabel}, "", "", "", "", err)
		return nil, fmt.Errorf("reading workflow from stdin: %w", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: sourceLabel}, "", "", "", "", err)
		return nil, fmt.Errorf("reading workflow from stdin: %w", err)
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		traceFinish(options.Diagnostics, started, diagnostic.PhaseDecode, diagnostic.StatusFailed, diagnostic.Location{Source: sourceLabel}, "", "", "", "", err)
		return nil, fmt.Errorf("resolving stdin workflow base directory %s: %w", baseDir, err)
	}
	return decodeWorkflowData(data, workflowDecodeSource{
		path: filepath.Join(absBaseDir, "-"), display: sourceLabel,
		sourceRoot: absBaseDir, sourceLabel: sourceLabel, virtual: true,
	}, options.Diagnostics, started, ctx, loader.pluginHelpers, loader.pluginResolver)
}

// Prepare resolves value-dependent workflow environment and composite actions in a decoded definition.
func (loader *Loader) Prepare(ctx context.Context, definition *Definition, options LoadOptions) (err error) {
	ctx = definition.PluginContext(ctx)
	if len(definition.helpers) > 0 {
		definition.setHelpers(ctx, definition.helpers)
	}
	if definition.secretSession == nil {
		definition.secretSession = options.SecretSession
	}
	if definition.secretSession == nil {
		definition.secretSession = secret.NewSession(ctx, secret.Options{
			BaseEnv: options.BaseEnv, Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
			Interactive: options.Interactive,
		})
	}
	session := definition.secretSession
	// Preparation renders workflow values, which may resolve secrets, so a failure here can
	// carry a resolved value in its message. Redact what this function returns the way
	// engine.Run does for step failures.
	defer func() { err = session.RedactError(err) }()
	options.SecretSession = session
	if options.EnsureSecretAuth {
		if err := session.EnsureAuth(definition.Secrets); err != nil {
			return err
		}
	}
	options.Diagnostics = secretDiagnostics(options.Diagnostics, session)
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
	data := TemplateDataWithProviders(definition, options.RunDir, options.EnvironmentLoaders, nil, vars, environment, nil, nil, nil, options.Providers)
	renderer, err := NewRendererWithHelpers(definition.HelperContext(), definition.Templates, session, definition.helpers)
	if err != nil {
		return err
	}
	cache := make(map[string]*Action)
	credentials := newGitHubCredentials(environment, loader.githubStoredTokenFn)
	if err := loader.resolveActions(ctx, definition.Name, definition.Steps, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, credentials, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Finally, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, credentials, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Install, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, credentials, options.Diagnostics); err != nil {
		return err
	}
	if err := loader.resolveActions(ctx, definition.Name, definition.Uninstall, renderer, data, environment, options.RunDir, true, definition.Path, options.sourceRoot, options.sourceLabel, cache, credentials, options.Diagnostics); err != nil {
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
	definition, err := loader.decode(ctx, filename, options)
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
