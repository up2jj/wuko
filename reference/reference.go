// Package reference provides offline, read-only workflow reference data.
package reference

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/up2jj/wuko/docs"
	"github.com/up2jj/wuko/step"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"go.yaml.in/yaml/v3"
)

// SchemaVersion versions the help and reference document envelope.
const SchemaVersion = 1

// WriteJSON emits exactly one versioned document. Callers resolve and validate
// requests before calling it, so lookup errors cannot leave partial JSON behind.
func WriteJSON(w io.Writer, kind string, data any) error {
	encoded, err := json.MarshalIndent(struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          any    `json:"data"`
	}{SchemaVersion, kind, data}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding reference: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", encoded)
	return err
}

// Entry is a compact, stable index entry. Command is an offline lookup, not a URL.
type Entry struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Command string `json:"command"`
}

// Field describes YAML structure. Semantics and custom YAML grammars are
// documented in the step's manual; omitempty never implies optionality.
type Field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required *bool    `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Fields   []Field  `json:"fields,omitempty"`
	Items    *Field   `json:"items,omitempty"`
}

// Output preserves the registry's structural contract without claiming a more
// precise scalar type or pretending dynamic object fields are known.
type Output struct {
	Kind   string            `json:"kind"`
	Open   bool              `json:"open,omitempty"`
	Fields map[string]Output `json:"fields,omitempty"`
	Items  *Output           `json:"items,omitempty"`
}

type Step struct {
	Entry
	Fields        []Field  `json:"fields"`
	Outputs       Output   `json:"outputs"`
	Documentation string   `json:"documentation"`
	Source        string   `json:"source"`
	Examples      []Entry  `json:"examples"`
	Related       []string `json:"related"`
	Rules         []string `json:"rules"`
}

type Topic struct {
	Entry
	Documentation string `json:"documentation"`
	Source        string `json:"source"`
}

// Example contains a complete workflow and any supporting files. Execution is
// always explicit: looking up an example never creates or runs its files.
type Example struct {
	ID            string            `json:"id" yaml:"id"`
	Title         string            `json:"title" yaml:"title"`
	Description   string            `json:"description" yaml:"description"`
	Workflow      string            `json:"workflow" yaml:"workflow"`
	Files         map[string]string `json:"files" yaml:"files"`
	Prerequisites []string          `json:"prerequisites" yaml:"prerequisites"`
	Expected      string            `json:"expected" yaml:"expected"`
	ExpectedFiles map[string]string `json:"expected_files,omitempty" yaml:"expected_files,omitempty"`
	AbsentFiles   []string          `json:"absent_files,omitempty" yaml:"absent_files,omitempty"`
	// Check is a runtime Expr used by isolated example tests when nonempty.
	Check string `json:"check,omitempty" yaml:"check,omitempty"`
	Run   bool   `json:"-" yaml:"run,omitempty"`
}

//go:embed examples/*.yaml metadata.yaml
var exampleFiles embed.FS

type Catalog struct {
	steps    map[string]Step
	topics   map[string]Topic
	examples map[string][]Example
}

// New uses only explicit native registrations and embedded files. It never calls
// builders or dynamic registry resolvers.
func New(definitions map[string]step.Registration) (*Catalog, error) {
	metadata, err := readSemantics()
	if err != nil {
		return nil, err
	}
	c := &Catalog{steps: make(map[string]Step), topics: make(map[string]Topic), examples: make(map[string][]Example)}
	for _, id := range topicNames {
		data, err := docs.Manuals.ReadFile(id + ".md")
		if err != nil {
			return nil, err
		}
		body := string(data)
		c.topics[id] = Topic{Entry: Entry{ID: id, Kind: "topic", Summary: firstParagraph(body), Command: "wuko reference topic " + id}, Documentation: body, Source: "docs/" + id + ".md"}
	}
	for _, name := range slices.Sorted(maps.Keys(definitions)) {
		body, source, err := stepManual(name)
		if err != nil {
			return nil, err
		}
		data, err := exampleFiles.ReadFile("examples/" + name + ".yaml")
		if err != nil {
			return nil, fmt.Errorf("examples for %s: %w", name, err)
		}
		var examples []Example
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&examples); err != nil {
			return nil, fmt.Errorf("examples for %s: %w", name, err)
		}
		for i := range examples {
			if examples[i].Files == nil {
				examples[i].Files = map[string]string{}
			}
			if examples[i].Prerequisites == nil {
				examples[i].Prerequisites = []string{}
			}
		}
		entries := make([]Entry, 0, len(examples))
		for _, example := range examples {
			entries = append(entries, Entry{ID: example.ID, Kind: "example", Summary: example.Title, Command: "wuko reference example " + name + " " + example.ID})
		}
		c.examples[name] = examples
		definition := definitions[name]
		spec, ok := metadata[name]
		if !ok {
			return nil, fmt.Errorf("missing semantic metadata for %s", name)
		}
		shape, err := annotate(fields(reflect.TypeOf(definition.Config), nil), spec)
		if err != nil {
			return nil, fmt.Errorf("metadata for %s: %w", name, err)
		}
		c.steps[name] = Step{Entry: Entry{ID: name, Kind: "step", Summary: firstParagraph(body), Command: "wuko reference step " + name}, Fields: shape, Outputs: output(definition.Outputs), Documentation: body, Source: source, Examples: entries, Related: []string{"templates", "template-functions", "agents"}, Rules: spec.Rules}
	}
	return c, nil
}

func (c *Catalog) Steps() []Entry {
	entries := make([]Entry, 0, len(c.steps))
	for _, name := range slices.Sorted(maps.Keys(c.steps)) {
		entries = append(entries, c.steps[name].Entry)
	}
	return entries
}

func (c *Catalog) Topics() []Entry {
	entries := make([]Entry, 0, len(c.topics))
	for _, name := range slices.Sorted(maps.Keys(c.topics)) {
		entries = append(entries, c.topics[name].Entry)
	}
	return entries
}

func (c *Catalog) Step(name string) (Step, error) {
	if item, ok := c.steps[name]; ok {
		return item, nil
	}
	return Step{}, fmt.Errorf("unknown built-in step %q; use 'wuko reference step'", name)
}

func (c *Catalog) Topic(name string) (Topic, error) {
	if item, ok := c.topics[name]; ok {
		return item, nil
	}
	return Topic{}, fmt.Errorf("unknown topic %q; use 'wuko reference topic'", name)
}

func (c *Catalog) Examples(name string) ([]Example, error) {
	if _, err := c.Step(name); err != nil {
		return nil, err
	}
	return c.examples[name], nil
}

// Search matches every query word case-insensitively. Results contain a short
// matching excerpt and stay in a deterministic kind/name order.
func (c *Catalog) Search(query string) []Entry {
	results := []Entry{}
	for _, entry := range append(c.Steps(), c.Topics()...) {
		body := ""
		if entry.Kind == "step" {
			body = c.steps[entry.ID].Documentation
		} else {
			body = c.topics[entry.ID].Documentation
		}
		haystack := strings.ToLower(entry.ID + " " + body)
		matched := true
		for _, word := range strings.Fields(strings.ToLower(query)) {
			if !strings.Contains(haystack, word) {
				matched = false
				break
			}
		}
		if matched {
			for _, paragraph := range strings.Split(body, "\n\n") {
				if strings.Contains(strings.ToLower(paragraph), strings.ToLower(query)) {
					entry.Summary = compact(paragraph)
					break
				}
			}
			results = append(results, entry)
		}
	}
	return results
}

var topicNames = []string{"agents", "cli", "getting-started", "reference", "execution", "workflow-control", "workflow-discovery", "templates", "template-functions", "executors", "finally", "return", "variable-imports", "validation", "plugins", "security", "secrets", "forms", "git-hooks", "git-changes", "worktrees", "filesystem-operations", "docker-operations", "extract", "script-sdk", "github-actions", "graceful-shutdown"}

func stepManual(name string) (string, string, error) {
	if file, ok := map[string]string{"file": "filesystem-operations", "docker": "docker-operations", "extract": "extract"}[name]; ok {
		data, err := docs.Manuals.ReadFile(file + ".md")
		return string(data), "docs/" + file + ".md", err
	}
	for _, file := range []string{"steps-data", "steps-interactive", "steps-system", "steps-automation", "worktrees"} {
		data, err := docs.Manuals.ReadFile(file + ".md")
		if err != nil {
			return "", "", err
		}
		if body, ok := section(data, name); ok {
			return body, "docs/" + file + ".md", nil
		}
	}
	custom := map[string][2]string{
		"process_call": {"steps-automation", "Calling a process"},
		"run_workflow": {"execution", "Running child workflows"},
		"devenv_task":  {"executors", "devenv executor"},
	}
	if target, ok := custom[name]; ok {
		data, err := docs.Manuals.ReadFile(target[0] + ".md")
		if err != nil {
			return "", "", err
		}
		if body, ok := section(data, target[1]); ok {
			return body, "docs/" + target[0] + ".md", nil
		}
	}
	return "", "", fmt.Errorf("missing manual for built-in step %s", name)
}

// Parse headings with the existing Markdown parser so headings inside fenced
// YAML and shell examples cannot terminate a reference section.
func section(source []byte, title string) (string, bool) {
	document := parser.New().Parse(source)
	start, end, level := -1, len(source), 0
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok {
			continue
		}
		segments := heading.Source()
		if len(segments) == 0 {
			continue
		}
		if start >= 0 && heading.Level <= level {
			end = bytes.LastIndexByte(source[:segments[0].Start], '\n') + 1
			break
		}
		if start < 0 && strings.Trim(strings.TrimSpace(string(source[segments[0].Start:segments[0].Stop])), "`") == title {
			start, level = segments[0].Stop, heading.Level
		}
	}
	if start < 0 {
		return "", false
	}
	// Heading segments start after the '#' marker. Trim the preceding line too.
	if end < len(source) {
		for end > start && source[end-1] != '\n' {
			end--
		}
	}
	return strings.TrimSpace(string(source[start:end])), true
}

func firstParagraph(body string) string {
	for _, paragraph := range strings.Split(body, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" || strings.HasPrefix(paragraph, "#") || strings.HasPrefix(paragraph, "[") {
			continue
		}
		return compact(paragraph)
	}
	return ""
}

func compact(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if len(characters) > 200 {
		return string(characters[:197]) + "..."
	}
	return value
}

func fields(t reflect.Type, ancestors map[reflect.Type]bool) []Field {
	result := []Field{}
	if t == nil {
		return result
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || ancestors[t] {
		return result
	}
	ancestors = maps.Clone(ancestors)
	if ancestors == nil {
		ancestors = map[reflect.Type]bool{}
	}
	ancestors[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			if slices.Contains(strings.Split(f.Tag.Get("yaml"), ","), "inline") {
				result = append(result, fields(f.Type, ancestors)...)
				continue
			}
			name = strings.ToLower(f.Name)
		}
		field := describeField(name, f.Type, ancestors)
		if index := slices.IndexFunc(result, func(existing Field) bool { return existing.Name == name }); index >= 0 {
			result[index] = field
		} else {
			result = append(result, field)
		}
	}
	return result
}

func describeField(name string, t reflect.Type, ancestors map[reflect.Type]bool) Field {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	f := Field{Name: name}
	// A custom YAML decoder defines its own grammar. Its Go representation is not
	// necessarily its YAML shape (duration, environment operations, transform input).
	if reflect.PointerTo(t).Implements(reflect.TypeFor[yaml.Unmarshaler]()) {
		f.Type = "custom YAML (see documentation)"
		return f
	}
	switch t.Kind() {
	case reflect.String:
		f.Type = "string"
	case reflect.Bool:
		f.Type = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.Type = "integer"
	case reflect.Float32, reflect.Float64:
		f.Type = "number"
	case reflect.Map:
		f.Type = "object"
		item := describeField("*", t.Elem(), ancestors)
		f.Items = &item
	case reflect.Slice, reflect.Array:
		f.Type = "array"
		item := describeField("[]", t.Elem(), ancestors)
		f.Items = &item
	case reflect.Struct:
		f.Type = "object"
		f.Fields = fields(t, ancestors)
	default:
		f.Type = "any"
	}
	return f
}

func output(schema step.OutputSchema) Output {
	o := Output{Kind: map[step.OutputKind]string{step.OutputUnknown: "unknown", step.OutputScalar: "scalar", step.OutputObject: "object", step.OutputArray: "array"}[schema.Kind], Open: schema.Open}
	if schema.Fields != nil {
		o.Fields = make(map[string]Output, len(schema.Fields))
		for name, child := range schema.Fields {
			o.Fields[name] = output(child)
		}
	}
	if schema.Items != nil {
		item := output(*schema.Items)
		o.Items = &item
	}
	return o
}
