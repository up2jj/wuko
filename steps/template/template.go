// Package template renders inline or packaged Go templates into memory or a file.
package template

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	filestep "github.com/up2jj/wuko/steps/file"
	"github.com/up2jj/wuko/workflow"
)

const maxTemplateBytes = 1 << 20

type Config struct {
	Source      string         `yaml:"source,omitempty"`
	File        string         `yaml:"file,omitempty"`
	Data        map[string]any `yaml:"data,omitempty"`
	Destination string         `yaml:"destination,omitempty"`
	Overwrite   bool           `yaml:"overwrite,omitempty"`
	Mode        string         `yaml:"mode,omitempty"`
}

type Runner struct {
	config   Config
	present  map[string]bool
	programs map[string]*vm.Program
	// deferred holds expr text that still carried template delimiters when the runner was
	// built, keyed by data name. Such an expression is compiled on use instead.
	deferred map[string]string
}

type fileRunner struct{ *Runner }

func (*Runner) ExecutorAware()          {}
func (*fileRunner) ExecutorFileSystem() {}

var (
	_ step.ExecutorAware      = (*Runner)(nil)
	_ step.ExecutorFileSystem = (*fileRunner)(nil)
)

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("template", step.Registration{
		Builder:       New,
		Outputs:       step.ClosedOutputs("content", "path", "size", "mode", "created"),
		ConfigOutputs: configOutputs,
	})
}

// configOutputs narrows the contract to the mode one step is configured for. A destination
// selects file mode, which reports the written file and deliberately keeps the rendered
// content out of step outputs; without one the content is the output.
func configOutputs(raw map[string]any) step.OutputSchema {
	if _, ok := raw["destination"]; ok {
		return step.ClosedOutputs("path", "size", "mode", "created")
	}
	return step.ClosedOutputs("content", "size")
}

func New(raw map[string]any) (step.Runner, error) {
	if value, ok := raw["mode"]; ok {
		if _, ok := value.(string); !ok {
			return nil, fmt.Errorf("mode must be a quoted octal string")
		}
	}
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(raw))
	for name := range raw {
		present[name] = true
	}
	runner := &Runner{config: config, present: present}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	programs, deferred, err := compileExpressions(config.Data)
	if err != nil {
		return nil, err
	}
	runner.programs, runner.deferred = programs, deferred
	if present["destination"] {
		return &fileRunner{Runner: runner}, nil
	}
	return runner, nil
}

func (r *Runner) Validate(ctx context.Context, request step.Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.present["file"] && templated(r.config.File) {
		return nil
	}
	source, _, err := r.readSource(ctx, request)
	if err != nil {
		return err
	}
	if request.TemplateRenderer == nil {
		return fmt.Errorf("template renderer is unavailable")
	}
	if err := request.TemplateRenderer.ValidateContent(source); err != nil {
		return fmt.Errorf("validating template: %w", err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	source, label, err := r.readSource(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	data, err := r.resolveData(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	renderer, ok := request.TemplateRenderer.(step.DataTemplateRenderer)
	if !ok {
		return step.Result{}, fmt.Errorf("template renderer does not support request-local data")
	}
	content, err := renderer.RenderContentWith(source, map[string]any{"data": data})
	if err != nil {
		return step.Result{}, fmt.Errorf("rendering template %s: %w", label, err)
	}
	if !r.present["destination"] {
		return step.Result{Outputs: map[string]any{"content": content, "size": int64(len(content))}}, nil
	}
	return r.write(ctx, request, content)
}

func (r *Runner) validate(resolved bool) error {
	if r.present["source"] == r.present["file"] {
		return fmt.Errorf("exactly one of source or file is required")
	}
	if r.present["source"] && strings.TrimSpace(r.config.Source) == "" {
		return fmt.Errorf("source must not be empty")
	}
	if r.present["file"] {
		if strings.TrimSpace(r.config.File) == "" {
			return fmt.Errorf("file must not be empty")
		}
		if resolved || !templated(r.config.File) {
			if err := validateRelativeFile(r.config.File); err != nil {
				return err
			}
		}
	}
	if !r.present["destination"] {
		if r.present["overwrite"] || r.present["mode"] {
			return fmt.Errorf("overwrite and mode require destination")
		}
		return nil
	}
	if strings.TrimSpace(r.config.Destination) == "" {
		return fmt.Errorf("destination must not be empty")
	}
	if r.present["mode"] && (resolved || !templated(r.config.Mode)) {
		if err := filestep.ValidateMode(r.config.Mode); err != nil {
			return err
		}
	}
	return nil
}

// compileExpressions compiles every typed expr binding that is ready to compile, and
// reports the rest by name. Step configuration is validated before it is rendered, so an
// expr whose text is still a workflow template is not an expression yet: compiling it here
// would fail validation for a workflow that runs correctly, and the runner built for the
// run holds the rendered text.
func compileExpressions(data map[string]any) (map[string]*vm.Program, map[string]string, error) {
	programs := make(map[string]*vm.Program)
	deferred := make(map[string]string)
	for name, value := range data {
		binding, ok := value.(map[string]any)
		if !ok || len(binding) != 1 {
			if !workflow.ActionDataValue(value) {
				return nil, nil, fmt.Errorf("data %q is not a YAML/JSON-compatible value", name)
			}
			continue
		}
		if literal, exists := binding["literal"]; exists {
			if !workflow.ActionDataValue(literal) {
				return nil, nil, fmt.Errorf("data %q literal is not a YAML/JSON-compatible value", name)
			}
			continue
		}
		source, exists := binding["expr"]
		if !exists {
			if !workflow.ActionDataValue(value) {
				return nil, nil, fmt.Errorf("data %q is not a YAML/JSON-compatible value", name)
			}
			continue
		}
		text, ok := source.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, nil, fmt.Errorf("data %q expr must be a non-empty string", name)
		}
		if templated(text) {
			deferred[name] = text
			continue
		}
		program, err := compileExpression(name, text)
		if err != nil {
			return nil, nil, err
		}
		programs[name] = program
	}
	return programs, deferred, nil
}

func compileExpression(name, text string) (*vm.Program, error) {
	program, err := wukoexpr.Compile(text, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compiling data %q expr: %w", name, err)
	}
	return program, nil
}

// Expressions returns the typed data expressions declared by one template step.
func Expressions(raw map[string]any) map[string]string {
	data, _ := raw["data"].(map[string]any)
	result := make(map[string]string)
	for name, value := range data {
		binding, ok := value.(map[string]any)
		if !ok || len(binding) != 1 {
			continue
		}
		if source, ok := binding["expr"].(string); ok {
			result[name] = source
		}
	}
	return result
}

// program returns the compiled expression bound to one data name, compiling text that was
// still a workflow template when the runner was built. It returns nil for a plain value.
func (r *Runner) program(name string) (*vm.Program, error) {
	if program, ok := r.programs[name]; ok {
		return program, nil
	}
	text, ok := r.deferred[name]
	if !ok {
		return nil, nil
	}
	return compileExpression(name, text)
}

func (r *Runner) resolveData(ctx context.Context, request step.Request) (map[string]any, error) {
	data := make(map[string]any, len(r.config.Data))
	for name, value := range r.config.Data {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		program, err := r.program(name)
		if err != nil {
			return nil, err
		}
		if program != nil {
			resolved, err := expr.Run(program, request.ExpressionEnvironment(nil))
			if err != nil {
				return nil, fmt.Errorf("evaluating data %q: %w", name, err)
			}
			if !workflow.ActionDataValue(resolved) {
				return nil, fmt.Errorf("data %q result is not a YAML/JSON-compatible value", name)
			}
			data[name] = workflow.Clone(resolved)
			continue
		}
		if binding, ok := value.(map[string]any); ok && len(binding) == 1 {
			if literal, exists := binding["literal"]; exists {
				value = literal
			}
		}
		data[name] = workflow.Clone(value)
	}
	return data, nil
}

func (r *Runner) readSource(ctx context.Context, request step.Request) (string, string, error) {
	if r.present["source"] {
		return r.config.Source, "inline", nil
	}
	if request.WorkflowDirBorrowed {
		return "", "", fmt.Errorf("file %q requires a packaged action: this action carries no files of its own", r.config.File)
	}
	if request.WorkflowDir == "" {
		return "", "", fmt.Errorf("workflow directory is unavailable")
	}
	path, err := resolvePackagedFile(request.WorkflowDir, r.config.File)
	if err != nil {
		return "", "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("reading template file %s: %w", path, err)
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxTemplateBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("reading template file %s: %w", path, err)
	}
	if len(content) > maxTemplateBytes {
		return "", "", fmt.Errorf("template file %s exceeds %d-byte limit", path, maxTemplateBytes)
	}
	// Text that is not valid UTF-8 cannot round-trip through workflow state or step
	// outputs, so it is rejected here with the file named rather than further down.
	if !utf8.Valid(content) {
		return "", "", fmt.Errorf("template file %s is not valid UTF-8", path)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return string(content), path, nil
}

func resolvePackagedFile(root, name string) (string, error) {
	if err := validateRelativeFile(name); err != nil {
		return "", err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving workflow directory: %w", err)
	}
	path := filepath.Join(absoluteRoot, filepath.FromSlash(name))
	if inside, err := pathWithin(absoluteRoot, path); err != nil {
		return "", fmt.Errorf("resolving template file %q: %w", name, err)
	} else if !inside {
		return "", fmt.Errorf("template file %q escapes the workflow package", name)
	}
	if err := rejectSymlinkComponents(absoluteRoot, name); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspecting template file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("template file %s must not be a symbolic link", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("template file %s must be a regular file", path)
	}
	physicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return "", fmt.Errorf("resolving workflow directory %s: %w", absoluteRoot, err)
	}
	physicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolving template file %s: %w", path, err)
	}
	if inside, err := pathWithin(physicalRoot, physicalPath); err != nil {
		return "", fmt.Errorf("resolving template file %s: %w", path, err)
	} else if !inside {
		return "", fmt.Errorf("template file %q escapes the workflow package", name)
	}
	// The audited path is the physical one: opening the logical path instead would walk the
	// components again, and anything able to write inside the package between the checks
	// above and that open could swap one for a symbolic link pointing outside it.
	return filepath.Clean(physicalPath), nil
}

func rejectSymlinkComponents(root, name string) error {
	current := root
	for component := range strings.SplitSeq(filepath.Clean(filepath.FromSlash(name)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspecting template file %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template file %s must not traverse a symbolic link", current)
		}
	}
	return nil
}

func validateRelativeFile(name string) error {
	// A drive-relative Windows path such as "C:templates/x.tmpl" is not absolute, so the
	// volume name is rejected separately.
	if filepath.IsAbs(name) || filepath.VolumeName(filepath.FromSlash(name)) != "" {
		return fmt.Errorf("file %q must be relative to the workflow package", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("file %q must not escape the workflow package", name)
	}
	return nil
}

func pathWithin(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// write hands the rendered body to the file step's write implementation directly, so the
// content never passes through a configuration map on its way to disk.
func (r *Runner) write(ctx context.Context, request step.Request, content string) (step.Result, error) {
	write := filestep.Write{Path: r.config.Destination, Content: content, Overwrite: r.config.Overwrite}
	if r.present["mode"] {
		write.Mode = r.config.Mode
	}
	runner, err := filestep.NewWrite(write)
	if err != nil {
		return step.Result{}, err
	}
	return runner.Run(ctx, request)
}

func templated(value string) bool { return strings.Contains(value, "{{") }
