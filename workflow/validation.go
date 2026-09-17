package workflow

import (
	"errors"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/validation"
)

type locatedValidationError struct {
	location diagnostic.Location
	err      error
}

func (err *locatedValidationError) Error() string { return err.err.Error() }
func (err *locatedValidationError) Unwrap() error { return err.err }

type contextualValidationError struct {
	message string
	err     error
}

func (err *contextualValidationError) Error() string { return err.message }
func (err *contextualValidationError) Unwrap() error { return err.err }

// ValidationError attaches workflow source context to structured issues, or
// turns a domain validation failure into one. It is the single integration
// boundary used by workflow loading and engine preflight.
func (definition *Definition) ValidationError(err error, code validation.Code, path validation.Path, location diagnostic.Location, stepID string) error {
	if err == nil {
		return nil
	}
	var located *locatedValidationError
	if errors.As(err, &located) {
		location = located.location
	}
	issues := validation.Issues(err)
	if len(issues) == 0 {
		issues = []validation.Issue{{Code: code, Message: err.Error(), Path: path, Cause: err}}
	}
	collector := validation.Collector{}
	for _, issue := range issues {
		if issue.Code == "" {
			issue.Code = code
		}
		if path != "" {
			if issue.Path != "" && issue.Path != path && issue.Target == "" {
				issue.Target = issue.Path.String()
			}
			issue.Path = path
		} else if issue.Path == "" {
			issue.Path = path
		}
		if issue.Workflow == "" {
			issue.Workflow = definition.Name
		}
		if issue.Step == "" {
			issue.Step = stepID
		}
		if issue.Span.Source == "" {
			issue.Span = validation.Span{Source: location.Source, Line: location.Line, Column: location.Column, EndLine: location.Line, EndColumn: location.Column}
		}
		if definition.validationIndex != nil && issue.SourceLine == "" && (issue.Span.Source == "" || issue.Span.Source == definition.validationIndex.Source()) {
			if issue.Path != "" {
				if indexed := definition.validationIndex.Span(issue.Path); indexed.Line > 0 {
					issue.Span = indexed
				}
			}
			issue.SourceLine = definition.validationIndex.Excerpt(issue.Span.Line)
		}
		collector.Add(issue)
	}
	normalized := collector.Err()
	if normalized != nil && err.Error() != normalized.Error() {
		return &contextualValidationError{message: err.Error(), err: normalized}
	}
	return normalized
}

func validationDiagnosticLocation(err error, fallback diagnostic.Location) diagnostic.Location {
	issues := validation.Issues(err)
	if len(issues) == 0 {
		return fallback
	}
	span := issues[0].Span
	return diagnostic.Location{Source: span.Source, Line: span.Line, Column: span.Column}
}

func decodeValidationError(err error, source string, index *validation.SourceIndex) error {
	if !validation.Is(err) {
		issue := validation.Issue{Code: validation.CodeInvalidWorkflow, Message: err.Error(), Span: validation.Span{Source: source}, Cause: err}
		return &validation.Error{Issues: []validation.Issue{issue}}
	}
	var collector validation.Collector
	for _, issue := range validation.Issues(err) {
		if issue.Span.Source == "" {
			issue.Span.Source = source
		}
		if issue.SourceLine == "" && index != nil {
			issue.SourceLine = index.Excerpt(issue.Span.Line)
		}
		collector.Add(issue)
	}
	return collector.Err()
}
