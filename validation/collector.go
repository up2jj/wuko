package validation

import (
	"fmt"
	"slices"
)

// Error contains one or more independent validation issues.
type Error struct {
	Issues []Issue
}

func (e *Error) Error() string {
	if e == nil || len(e.Issues) == 0 {
		return "validation failed"
	}
	if len(e.Issues) == 1 {
		return e.Issues[0].Error()
	}
	return fmt.Sprintf("%s (and %d more validation issues)", e.Issues[0].Error(), len(e.Issues)-1)
}

func (e *Error) Unwrap() []error {
	if e == nil {
		return nil
	}
	result := make([]error, len(e.Issues))
	for index := range e.Issues {
		result[index] = e.Issues[index]
	}
	return result
}

// Collector accumulates, sorts, and deduplicates independent issues.
type Collector struct {
	issues []Issue
}

func (c *Collector) Add(issue Issue) {
	if issue.Message == "" {
		issue.Message = "validation failed"
	}
	c.issues = append(c.issues, issue)
}

// AddError extracts structured issues. It returns false for an ordinary error,
// allowing orchestration to keep I/O, cancellation, and transport failures
// fail-fast.
func (c *Collector) AddError(err error) bool {
	issues := Issues(err)
	if len(issues) == 0 {
		return false
	}
	c.issues = append(c.issues, issues...)
	return true
}

func (c *Collector) Len() int { return len(c.List()) }

// List returns a deterministic, deduplicated copy.
func (c *Collector) List() []Issue {
	result := append([]Issue(nil), c.issues...)
	slices.SortStableFunc(result, compareIssue)
	result = slices.CompactFunc(result, sameIssue)
	return result
}

func (c *Collector) Err() error {
	issues := c.List()
	if len(issues) == 0 {
		return nil
	}
	return &Error{Issues: issues}
}

func compareIssue(left, right Issue) int {
	for _, pair := range [][2]string{{left.Span.Source, right.Span.Source}, {left.Workflow, right.Workflow}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	for _, pair := range [][2]int{{left.Span.Line, right.Span.Line}, {left.Span.Column, right.Span.Column}, {left.Span.EndLine, right.Span.EndLine}, {left.Span.EndColumn, right.Span.EndColumn}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	for _, pair := range [][2]string{{left.Path.String(), right.Path.String()}, {string(left.Code), string(right.Code)}, {left.Message, right.Message}, {left.Target, right.Target}, {left.Step, right.Step}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func sameIssue(left, right Issue) bool { return compareIssue(left, right) == 0 }

// IssueProvider is implemented by wrappers that must replace the issues of the
// error they wrap, such as secret redaction. Issues stops at a provider instead
// of descending into the original, unredacted issues.
type IssueProvider interface {
	error
	ValidationIssues() []Issue
}

// Issues returns every structured issue reachable through wrapped or joined
// errors. Ordinary errors produce nil.
func Issues(err error) []Issue {
	if err == nil {
		return nil
	}
	var collector Collector
	var walk func(error)
	walk = func(current error) {
		if current == nil {
			return
		}
		switch typed := current.(type) {
		case IssueProvider:
			for _, issue := range typed.ValidationIssues() {
				collector.Add(issue)
			}
			return
		case Issue:
			collector.Add(typed)
			return
		case *Issue:
			if typed != nil {
				collector.Add(*typed)
			}
			return
		case *Error:
			if typed != nil {
				collector.issues = append(collector.issues, typed.Issues...)
			}
			return
		case interface{ Unwrap() []error }:
			for _, nested := range typed.Unwrap() {
				walk(nested)
			}
			return
		case interface{ Unwrap() error }:
			walk(typed.Unwrap())
		}
	}
	walk(err)
	return collector.List()
}

// Is reports whether err contains at least one validation issue.
func Is(err error) bool { return len(Issues(err)) != 0 }

// AsError returns a normalized validation Error when possible.
func AsError(err error) (*Error, bool) {
	issues := Issues(err)
	if len(issues) == 0 {
		return nil, false
	}
	return &Error{Issues: issues}, true
}

// Prefix adds domain context to every structured message without requiring
// renderers or collectors to parse wrapper strings.
func Prefix(err error, prefix string) error {
	issues := Issues(err)
	if len(issues) == 0 {
		return err
	}
	for index := range issues {
		issues[index].Message = prefix + issues[index].Message
	}
	return &Error{Issues: issues}
}

var _ error = (*Error)(nil)
var _ interface{ Unwrap() []error } = (*Error)(nil)
