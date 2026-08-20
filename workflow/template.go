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
	base := template.New("templates").Option("missingkey=error")
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
		parsed, err := template.New(name).Option("missingkey=error").Parse(body)
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
	_, err := renderer.compile(value)
	return err
}

// Render executes one template string with the supplied data.
func (renderer *Renderer) Render(value string, data map[string]any) (string, error) {
	tmpl, err := renderer.compile(value)
	if err != nil {
		return "", err
	}
	var rendered bytes.Buffer
	if err := tmpl.ExecuteTemplate(&rendered, executionTemplateName, data); err != nil {
		return "", err
	}
	return rendered.String(), nil
}

func (renderer *Renderer) compile(value string) (*template.Template, error) {
	if cached, ok := renderer.cache.Load(value); ok {
		return cached.(*template.Template), nil
	}
	parsed, err := template.New(executionTemplateName).Option("missingkey=error").Parse(value)
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
	definitionCheck, err := template.New(definitionCheckTemplateName).Parse(value)
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
	actual, _ := renderer.cache.LoadOrStore(value, compiled)
	return actual.(*template.Template), nil
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
		if err := walkTemplateNodes(item.Tree.Root, func(name string) error {
			if tmpl.Lookup(name) == nil {
				return fmt.Errorf("template %q references undefined template %q", displayName, name)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func walkTemplateNodes(node parse.Node, visit func(string) error) error {
	if node == nil {
		return nil
	}
	switch typed := node.(type) {
	case *parse.ListNode:
		for _, child := range typed.Nodes {
			if err := walkTemplateNodes(child, visit); err != nil {
				return err
			}
		}
	case *parse.IfNode:
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.RangeNode:
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.WithNode:
		if err := walkTemplateNodes(typed.List, visit); err != nil {
			return err
		}
		return walkTemplateNodes(typed.ElseList, visit)
	case *parse.TemplateNode:
		return visit(typed.Name)
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
