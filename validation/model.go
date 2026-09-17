// Package validation defines Wuko's structured validation model.
//
// Semantic rules belong to their domain packages. This package owns the stable
// representation, common wording, aggregation, source mapping, and safe JSON
// projection used by every validation adapter.
package validation

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Code is a stable, machine-readable validation issue identifier.
type Code string

const (
	CodeUnknownField         Code = "unknown_field"
	CodeMissingField         Code = "missing_field"
	CodeConflict             Code = "conflict"
	CodeInvalidType          Code = "invalid_type"
	CodeInvalidValue         Code = "invalid_value"
	CodeUnavailableReference Code = "unavailable_reference"
	CodeDuplicateDeclaration Code = "duplicate_declaration"
	CodeUnknownStepOutput    Code = "unknown_step_output"
	CodeUnknownStep          Code = "unknown_step"
	CodeUnknownVariable      Code = "unknown_variable"
	CodeUnknownDependency    Code = "unknown_dependency"
	CodeUnknownOutput        Code = "unknown_output"
	CodeInvalidExpression    Code = "invalid_expression"
	CodeInvalidTemplate      Code = "invalid_template"
	CodeInvalidStep          Code = "invalid_step"
	CodeInvalidWorkflow      Code = "invalid_workflow"
)

// Path identifies a value within a YAML document using a stable dotted path.
type Path string

func (p Path) String() string { return string(p) }

// Field appends a mapping field to a path.
func (p Path) Field(name string) Path {
	if p == "" {
		return Path(name)
	}
	return Path(string(p) + "." + name)
}

// Index appends a sequence index to a path.
func (p Path) Index(index int) Path { return Path(fmt.Sprintf("%s[%d]", p, index)) }

// Span identifies an exact range in a logical YAML source. End positions are
// inclusive. Source is a local path or sanitized remote locator.
type Span struct {
	Source    string `json:"source,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	EndColumn int    `json:"end_column,omitempty"`
}

// Related points at another declaration relevant to an issue.
type Related struct {
	Message string `json:"message"`
	Span    Span   `json:"span"`
}

// Issue is one structured validation problem. Cause and SourceLine are for
// local diagnostics only and are deliberately absent from JSON projections.
type Issue struct {
	Code       Code      `json:"code"`
	Message    string    `json:"message"`
	Hint       string    `json:"hint,omitempty"`
	Path       Path      `json:"path,omitempty"`
	Span       Span      `json:"span,omitempty"`
	Related    []Related `json:"related,omitempty"`
	Workflow   string    `json:"workflow,omitempty"`
	Step       string    `json:"step,omitempty"`
	Target     string    `json:"target,omitempty"`
	SourceLine string    `json:"-"`
	Cause      error     `json:"-"`
}

// Error returns a concise, single-line, ANSI-free diagnostic.
func (i Issue) Error() string {
	message := strings.TrimSpace(i.Message)
	if message == "" {
		message = "validation failed"
	}
	return message
}

// Unwrap preserves the original cause for errors.Is/errors.As without exposing
// it through the safe projection.
func (i Issue) Unwrap() error { return i.Cause }

// WithSpan returns a copy of the issue with source information attached.
func (i Issue) WithSpan(span Span) Issue { i.Span = span; return i }

// WithPath returns a copy of the issue with its YAML path attached.
func (i Issue) WithPath(path Path) Issue { i.Path = path; return i }

// WithContext returns a copy annotated with workflow and step identity.
func (i Issue) WithContext(workflow, step string) Issue {
	i.Workflow, i.Step = workflow, step
	return i
}

// SafeIssue is the stable, non-sensitive JSON representation of an Issue.
type SafeIssue struct {
	Code     Code      `json:"code"`
	Message  string    `json:"message"`
	Hint     string    `json:"hint,omitempty"`
	Path     Path      `json:"path,omitempty"`
	Span     Span      `json:"span,omitempty"`
	Related  []Related `json:"related,omitempty"`
	Workflow string    `json:"workflow,omitempty"`
	Step     string    `json:"step,omitempty"`
	Target   string    `json:"target,omitempty"`
}

// Safe returns the stable projection suitable for JSON, reports, and UI data.
func (i Issue) Safe() SafeIssue {
	span := i.Span
	span.Source = SanitizeSource(span.Source)
	related := append([]Related(nil), i.Related...)
	for index := range related {
		related[index].Span.Source = SanitizeSource(related[index].Span.Source)
	}
	return SafeIssue{
		Code: i.Code, Message: i.Message, Hint: i.Hint, Path: i.Path,
		Span: span, Related: related,
		Workflow: i.Workflow, Step: i.Step, Target: i.Target,
	}
}

// MarshalJSON always uses the safe projection, even when an adapter encodes an
// Issue directly instead of first calling Safe.
func (i Issue) MarshalJSON() ([]byte, error) { return json.Marshal(i.Safe()) }

// SanitizeSource removes URL credentials, queries, and fragments before a
// logical source is presented outside validation internals.
func SanitizeSource(source string) string {
	if source == "" {
		return ""
	}
	parsed, err := url.Parse(source)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		if parsed.User != nil {
			parsed.User = url.User("redacted")
		}
		parsed.RawQuery, parsed.Fragment = "", ""
		return parsed.String()
	}
	if offset := strings.IndexAny(source, "?#"); offset >= 0 {
		return source[:offset]
	}
	return source
}

// Document is the versioned JSON result emitted by `wuko validate --format json`.
type Document struct {
	SchemaVersion int         `json:"schema_version"`
	Valid         bool        `json:"valid"`
	Issues        []SafeIssue `json:"issues"`
}

const DocumentSchemaVersion = 1

// NewDocument creates the canonical validation JSON document.
func NewDocument(issues []Issue) Document {
	safe := make([]SafeIssue, len(issues))
	for index := range issues {
		safe[index] = issues[index].Safe()
	}
	return Document{SchemaVersion: DocumentSchemaVersion, Valid: len(safe) == 0, Issues: safe}
}
