package validation

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// SourceIndex maps stable YAML paths to exact source spans and redacted lines.
type SourceIndex struct {
	source string
	lines  []string
	spans  map[Path]Span

	// Lazy indexes keep only the raw document until a diagnostic needs it, so
	// successful loads do not pay for a second YAML parse or retain a span map.
	once sync.Once
	data []byte
}

// NewLazySourceIndex returns an index that parses data on first use. A
// document that cannot be indexed behaves like an empty index.
func NewLazySourceIndex(source string, data []byte) *SourceIndex {
	return &SourceIndex{source: SanitizeSource(source), data: data}
}

func (i *SourceIndex) ensure() {
	i.once.Do(func() {
		data := i.data
		if data == nil {
			return
		}
		i.data = nil
		if built, err := NewSourceIndex(i.source, data); err == nil {
			i.lines, i.spans = built.lines, built.spans
		}
	})
}

// Source returns the logical source named by this index.
func (i *SourceIndex) Source() string {
	if i == nil {
		return ""
	}
	return i.source
}

// NewSourceIndex parses one YAML document and indexes mapping values and
// sequence entries. It is independent of typed decoding, so it also works when
// strict decoding fails.
func NewSourceIndex(source string, data []byte) (*SourceIndex, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	index := &SourceIndex{source: SanitizeSource(source), spans: make(map[Path]Span)}
	index.once.Do(func() {})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		index.lines = append(index.lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("indexing YAML source: %w", err)
	}
	if len(document.Content) != 0 {
		index.visit(document.Content[0], "")
	}
	return index, nil
}

func (i *SourceIndex) visit(node *yaml.Node, path Path) {
	if node == nil {
		return
	}
	if path != "" {
		i.spans[path] = spanForNode(i.source, node)
	}
	switch node.Kind {
	case yaml.MappingNode:
		for offset := 0; offset+1 < len(node.Content); offset += 2 {
			key, value := node.Content[offset], node.Content[offset+1]
			child := path.Field(key.Value)
			i.spans[child] = spanForNode(i.source, key)
			i.visit(value, child)
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			i.visit(child, path.Index(index))
		}
	}
}

func spanForNode(source string, node *yaml.Node) Span {
	endLine, endColumn := node.Line, node.Column
	if node.Kind == yaml.ScalarNode {
		endColumn += max(0, len([]rune(node.Value))-1)
	}
	return Span{Source: source, Line: node.Line, Column: node.Column, EndLine: endLine, EndColumn: endColumn}
}

// Span returns the nearest indexed span, walking up the path when a synthetic
// field (for example a missing required field) has no node of its own.
func (i *SourceIndex) Span(path Path) Span {
	if i == nil {
		return Span{}
	}
	i.ensure()
	for candidate := path; ; candidate = parentPath(candidate) {
		if span, ok := i.spans[candidate]; ok {
			return span
		}
		if candidate == "" {
			break
		}
	}
	return Span{Source: i.source}
}

// Attach adds indexed source context and a redacted excerpt to an issue.
func (i *SourceIndex) Attach(issue Issue) Issue {
	if issue.Span.Source == "" {
		issue.Span = i.Span(issue.Path)
	}
	if issue.SourceLine == "" {
		issue.SourceLine = i.Excerpt(issue.Span.Line)
	}
	return issue
}

// Excerpt returns one redacted source line. It never returns raw values for
// keys conventionally used for secrets, tokens, credentials, or passwords.
func (i *SourceIndex) Excerpt(line int) string {
	if i == nil || line < 1 {
		return ""
	}
	i.ensure()
	if line > len(i.lines) {
		return ""
	}
	return redactSourceLine(i.lines[line-1])
}

func parentPath(path Path) Path {
	value := string(path)
	if value == "" {
		return ""
	}
	if strings.HasSuffix(value, "]") {
		if offset := strings.LastIndex(value, "["); offset >= 0 {
			return Path(value[:offset])
		}
	}
	if offset := strings.LastIndex(value, "."); offset >= 0 {
		return Path(value[:offset])
	}
	return ""
}

// sensitiveYAMLKey matches a sensitive key anywhere on the line, so top-level
// keys and flow mappings such as `with: {token: abc}` are covered too.
var sensitiveYAMLKey = regexp.MustCompile(`(?i)[\w.-]*(?:password|passwd|secret|token|api[_-]?key|credential|private[_-]?key)[\w.-]*["']?\s*:`)

func redactSourceLine(line string) string {
	if match := sensitiveYAMLKey.FindStringIndex(line); match != nil {
		return line[:match[1]] + " <redacted>"
	}
	fields := strings.Fields(line)
	for _, field := range fields {
		candidate := strings.Trim(field, `"'()[]{},`)
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		if parsed.User != nil {
			parsed.User = url.User("redacted")
		}
		parsed.RawQuery, parsed.Fragment = "", ""
		line = strings.Replace(line, candidate, parsed.String(), 1)
	}
	return line
}
