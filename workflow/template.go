package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"

	"github.com/up2jj/wuko/expression"
	"gopkg.in/yaml.v3"
)

const (
	maxTemplateSize             = 1 << 20
	executionTemplateName       = "wuko:value"
	definitionCheckTemplateName = "wuko:definition-check"
)

// TemplateDefinition declares an inline template body or a file containing one.
type TemplateDefinition struct {
	Inline string `yaml:"-"`
	File   string `yaml:"file,omitempty"`
	Body   string `yaml:"-"`
}

func (definition *TemplateDefinition) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return fmt.Errorf("template must be a string or an object containing file")
		}
		definition.Inline = node.Value
		return nil
	case yaml.MappingNode:
		if len(node.Content) != 2 || node.Content[0].Value != "file" {
			return fmt.Errorf("template file must contain exactly the file field")
		}
		if node.Content[1].Tag != "!!str" {
			return fmt.Errorf("template file path must be a string")
		}
		if strings.TrimSpace(node.Content[1].Value) == "" {
			return fmt.Errorf("template file path must not be empty")
		}
		definition.File = node.Content[1].Value
		return nil
	default:
		return fmt.Errorf("template must be a string or an object containing file")
	}
}

// Renderer parses and executes one immutable set of strict named Go templates.
type Renderer struct {
	base  *template.Template
	cache sync.Map
}

// NewRenderer constructs a renderer from resolved template definitions.
func NewRenderer(definitions map[string]TemplateDefinition) (*Renderer, error) {
	base := newTemplate("templates")
	names := slices.Sorted(maps.Keys(definitions))
	for _, name := range names {
		if !identifierPattern.MatchString(name) {
			return nil, fmt.Errorf("invalid template name %q", name)
		}
		definition := definitions[name]
		body := definition.Inline
		if definition.File != "" {
			body = definition.Body
		}
		if strings.TrimSpace(body) == "" {
			return nil, fmt.Errorf("template %q body must not be empty", name)
		}
		parsed, err := newTemplate(name).Parse(body)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", name, err)
		}
		for _, item := range parsed.Templates() {
			if item.Name() != name {
				return nil, fmt.Errorf("template %q must not define nested template %q", name, item.Name())
			}
		}
		if _, err := base.AddParseTree(name, parsed.Lookup(name).Tree); err != nil {
			return nil, fmt.Errorf("template %q: %w", name, err)
		}
	}
	if err := validateTemplateReferences(base); err != nil {
		return nil, err
	}
	return &Renderer{base: base}, nil
}

// Validate parses one template string and checks named-template references.
func (renderer *Renderer) Validate(value string) error {
	_, err := renderer.compile(value, true)
	return err
}

// Render executes one template string with the supplied data.
func (renderer *Renderer) Render(value string, data map[string]any) (string, error) {
	return renderer.render(value, data, true)
}

// ValidateUncached parses one template string without retaining it in the compiled-template
// cache. Use it for one-off external content such as a file body: the cache is keyed by the
// whole template text and lives for the run, so it suits short configuration values that
// repeat across steps rather than large bodies read once.
func (renderer *Renderer) ValidateUncached(value string) error {
	_, err := renderer.compile(value, false)
	return err
}

// RenderUncached executes one template string without retaining it in the compiled-template
// cache. See ValidateUncached.
func (renderer *Renderer) RenderUncached(value string, data map[string]any) (string, error) {
	return renderer.render(value, data, false)
}

func (renderer *Renderer) render(value string, data map[string]any, cache bool) (string, error) {
	tmpl, err := renderer.compile(value, cache)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.ExecuteTemplate(&rendered, executionTemplateName, data); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func (renderer *Renderer) compile(value string, cache bool) (*template.Template, error) {
	if cache {
		if cached, ok := renderer.cache.Load(value); ok {
			return cached.(*template.Template), nil
		}
	}
	parsed, err := newTemplate(executionTemplateName).Parse(value)
	if err != nil {
		return nil, err
	}
	for _, item := range parsed.Templates() {
		if item.Name() != executionTemplateName {
			return nil, fmt.Errorf("value must not define nested template %q", item.Name())
		}
	}
	// Parse under a second root name so an explicit definition of the execution root
	// cannot be mistaken for the value's own parse tree.
	definitionCheck, err := newTemplate(definitionCheckTemplateName).Parse(value)
	if err != nil {
		return nil, err
	}
	for _, item := range definitionCheck.Templates() {
		if item.Name() != definitionCheckTemplateName {
			return nil, fmt.Errorf("value must not define nested template %q", item.Name())
		}
	}
	cloned, err := renderer.base.Clone()
	if err != nil {
		return nil, err
	}
	compiled, err := cloned.AddParseTree(executionTemplateName, parsed.Lookup(executionTemplateName).Tree)
	if err != nil {
		return nil, err
	}
	if err := validateTemplateReferences(compiled); err != nil {
		return nil, err
	}
	if !cache {
		return compiled, nil
	}
	actual, _ := renderer.cache.LoadOrStore(value, compiled)
	return actual.(*template.Template), nil
}

func newTemplate(name string) *template.Template {
	return template.New(name).Funcs(expression.TemplateFuncs()).Option("missingkey=error")
}

func validateTemplateReferences(tmpl *template.Template) error {
	for _, item := range tmpl.Templates() {
		if item.Tree == nil || item.Tree.Root == nil {
			continue
		}
		displayName := item.Name()
		if displayName == executionTemplateName {
			displayName = "value"
		}
		if err := walkTemplateNodes(item.Tree.Root, templateNodeVisitor{template: func(name string) error {
			if tmpl.Lookup(name) == nil {
				return fmt.Errorf("template %q references undefined template %q", displayName, name)
			}
			return nil
		}}); err != nil {
			return err
		}
	}
	return nil
}

// WalkDataReferences visits static data paths used by value and every named template it invokes.
// Paths omit the leading dot or dollar sign: .steps.build.stdout becomes
// ["steps", "build", "stdout"]. Dynamic index segments stop the static path.
func (renderer *Renderer) WalkDataReferences(value string, visit func([]string) error) error {
	tmpl, err := renderer.compile(value, true)
	if err != nil {
		return err
	}
	return walkTemplateDataReferences(tmpl, executionTemplateName, make(map[string]struct{}), visit)
}

// WalkNamedDataReferences visits static data paths in every declared named template.
func (renderer *Renderer) WalkNamedDataReferences(visit func(name string, path []string) error) error {
	items := renderer.base.Templates()
	slices.SortFunc(items, func(left, right *template.Template) int {
		return strings.Compare(left.Name(), right.Name())
	})
	for _, item := range items {
		if item.Tree == nil || item.Tree.Root == nil {
			continue
		}
		if err := walkTemplateNodes(item.Tree.Root, templateNodeVisitor{data: func(path []string) error {
			return visit(item.Name(), path)
		}}); err != nil {
			return err
		}
	}
	return nil
}

func walkTemplateDataReferences(tmpl *template.Template, name string, visited map[string]struct{}, visit func([]string) error) error {
	if _, ok := visited[name]; ok {
		return nil
	}
	visited[name] = struct{}{}
	item := tmpl.Lookup(name)
	if item == nil || item.Tree == nil || item.Tree.Root == nil {
		return nil
	}
	return walkTemplateNodes(item.Tree.Root, templateNodeVisitor{
		data: visit,
		template: func(child string) error {
			return walkTemplateDataReferences(tmpl, child, visited, visit)
		},
	})
}

type templateNodeVisitor struct {
	template func(string) error
	data     func([]string) error
}

func walkTemplateNodes(node parse.Node, visit templateNodeVisitor) error {
	if node == nil {
		return nil
	}
	switch typed := node.(type) {
	case *parse.ListNode:
		if typed == nil {
			return nil
		}
		for _, child := range typed.Nodes {
			if err := walkTemplateNodes(child, visit); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return walkTemplateNodes(typed.Pipe, visit)
	case *parse.PipeNode:
		if typed == nil {
			return nil
		}
		if visit.data != nil {
			if path := templatePipePath(typed); len(path) > 0 {
				if err := visit.data(path); err != nil {
					return err
				}
			}
		}
		for _, declaration := range typed.Decl {
			if err := walkTemplateNodes(declaration, visit); err != nil {
				return err
			}
		}
		for _, command := range typed.Cmds {
			if err := walkTemplateNodes(command, visit); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if visit.data != nil {
			if path := templateCommandPath(typed); len(path) > 0 {
				if err := visit.data(path); err != nil {
					return err
				}
			}
		}
		for _, argument := range typed.Args {
			if err := walkTemplateNodes(argument, visit); err != nil {
				return err
			}
		}
	case *parse.FieldNode:
		if visit.data != nil && len(typed.Ident) > 0 {
			return visit.data(slices.Clone(typed.Ident))
		}
	case *parse.VariableNode:
		if visit.data != nil && len(typed.Ident) > 1 && typed.Ident[0] == "$" {
			return visit.data(slices.Clone(typed.Ident[1:]))
		}
	case *parse.ChainNode:
		if visit.data != nil {
			if path := templateStaticPath(typed); len(path) > 0 {
				if err := visit.data(path); err != nil {
					return err
				}
			}
		}
		return walkTemplateNodes(typed.Node, visit)
	case *parse.IfNode:
		if err := walkTemplateNodes(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.RangeNode:
		if err := walkTemplateNodes(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.WithNode:
		if err := walkTemplateNodes(typed.Pipe, visit); err != nil {
			return err
		}
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.TemplateNode:
		if err := walkTemplateNodes(typed.Pipe, visit); err != nil {
			return err
		}
		if visit.template != nil {
			return visit.template(typed.Name)
		}
	}
	return nil
}

func templatePipePath(pipe *parse.PipeNode) []string {
	if len(pipe.Cmds) < 2 {
		return nil
	}
	path := templateStaticPath(pipe.Cmds[0])
	if len(path) == 0 {
		return nil
	}
	matched := false
	for _, command := range pipe.Cmds[1:] {
		if len(command.Args) != 2 {
			break
		}
		identifier, ok := command.Args[0].(*parse.IdentifierNode)
		if !ok || (identifier.Ident != "get" && identifier.Ident != "hasKey") {
			break
		}
		key, ok := command.Args[1].(*parse.StringNode)
		if !ok {
			break
		}
		if identifier.Ident == "hasKey" {
			// A presence test must not require the key it asks about.
			break
		}
		path = append(path, key.Text)
		matched = true
	}
	if !matched {
		return nil
	}
	return path
}

func templateCommandPath(command *parse.CommandNode) []string {
	if len(command.Args) < 3 {
		return nil
	}
	identifier, ok := command.Args[0].(*parse.IdentifierNode)
	if !ok {
		return nil
	}
	if identifier.Ident == "get" || identifier.Ident == "hasKey" {
		key, ok := command.Args[1].(*parse.StringNode)
		if !ok {
			return nil
		}
		path := templateStaticPath(command.Args[2])
		if len(path) == 0 {
			return nil
		}
		if identifier.Ident == "hasKey" {
			// A presence test must not require the key it asks about.
			return path
		}
		return append(path, key.Text)
	}
	if identifier.Ident != "index" {
		return nil
	}
	path := templateStaticPath(command.Args[1])
	if len(path) == 0 {
		return nil
	}
	for _, argument := range command.Args[2:] {
		key, ok := argument.(*parse.StringNode)
		if !ok {
			break
		}
		path = append(path, key.Text)
	}
	return path
}

func templateStaticPath(node parse.Node) []string {
	switch typed := node.(type) {
	case *parse.FieldNode:
		return slices.Clone(typed.Ident)
	case *parse.VariableNode:
		if len(typed.Ident) > 1 && typed.Ident[0] == "$" {
			return slices.Clone(typed.Ident[1:])
		}
	case *parse.ChainNode:
		path := templateStaticPath(typed.Node)
		if len(path) > 0 {
			return append(path, typed.Field...)
		}
	case *parse.PipeNode:
		if len(typed.Cmds) == 1 {
			return templateStaticPath(typed.Cmds[0])
		}
	case *parse.CommandNode:
		if path := templateCommandPath(typed); len(path) > 0 {
			return path
		}
		if len(typed.Args) == 1 {
			return templateStaticPath(typed.Args[0])
		}
	}
	return nil
}

func resolveTemplateFiles(definitions map[string]TemplateDefinition, baseDir string, files map[string]ActionFile, allowedRoot string) error {
	for name, definition := range definitions {
		if definition.File == "" {
			continue
		}
		if strings.TrimSpace(definition.File) == "" {
			return fmt.Errorf("template %q file must not be empty", name)
		}
		if filepath.IsAbs(definition.File) {
			return fmt.Errorf("template %q file %q must be relative", name, definition.File)
		}
		if files != nil {
			archivePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(definition.File)))
			if err := validateArchivePath(archivePath); err != nil {
				return fmt.Errorf("template %q: %w", name, err)
			}
			file, ok := files[archivePath]
			if !ok {
				return fmt.Errorf("template %q: file %q not found in action package", name, definition.File)
			}
			if len(file.Data) > maxTemplateSize {
				return fmt.Errorf("template %q file %q exceeds %d-byte limit", name, definition.File, maxTemplateSize)
			}
			definition.Body = string(file.Data)
			definitions[name] = definition
			continue
		}

		filePath := filepath.Join(baseDir, filepath.FromSlash(definition.File))
		if allowedRoot != "" {
			inside, err := pathWithin(allowedRoot, filePath)
			if err != nil {
				return fmt.Errorf("template %q file %q: %w", name, definition.File, err)
			}
			if !inside {
				return fmt.Errorf("template %q file %q escapes the workflow package", name, definition.File)
			}
		}
		body, err := readTemplateFile(filePath)
		if err != nil {
			return fmt.Errorf("template %q file %q: %w", name, definition.File, err)
		}
		definition.Body = body
		definitions[name] = definition
	}
	return nil
}

func readTemplateFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading template file: %w", pathlessError(err))
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTemplateSize+1))
	if err != nil {
		return "", fmt.Errorf("reading template file: %w", pathlessError(err))
	}
	if len(data) > maxTemplateSize {
		return "", fmt.Errorf("template file exceeds %d-byte limit", maxTemplateSize)
	}
	return string(data), nil
}

func pathlessError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func pathWithin(root, candidate string) (bool, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}
