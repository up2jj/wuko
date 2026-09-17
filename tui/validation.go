package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/up2jj/wuko/validation"
)

// WriteValidation renders compiler-style diagnostics from the shared issue
// model. It returns false when err is not a validation error.
func WriteValidation(writer io.Writer, err error, relativeTo string) bool {
	issues := validation.Issues(err)
	if len(issues) == 0 {
		return false
	}
	for index, issue := range issues {
		if index > 0 {
			fmt.Fprintln(writer)
		}
		location := displaySource(validation.SanitizeSource(issue.Span.Source), relativeTo)
		if issue.Span.Line > 0 {
			location += fmt.Sprintf(":%d", issue.Span.Line)
			if issue.Span.Column > 0 {
				location += fmt.Sprintf(":%d", issue.Span.Column)
			}
		}
		if location != "" {
			location += ": "
		}
		fmt.Fprintf(writer, "%serror[%s]: %s\n", location, issue.Code, issue.Message)
		if issue.Path != "" {
			fmt.Fprintf(writer, "  path: %s\n", issue.Path)
		}
		if issue.SourceLine != "" {
			fmt.Fprintf(writer, "  | %s\n", issue.SourceLine)
			column := max(1, issue.Span.Column)
			width := 1
			if issue.Span.EndLine == issue.Span.Line && issue.Span.EndColumn >= column {
				width = issue.Span.EndColumn - column + 1
			}
			width = min(width, max(1, utf8.RuneCountInString(issue.SourceLine)-column+1))
			fmt.Fprintf(writer, "  | %s%s\n", strings.Repeat(" ", column-1), strings.Repeat("^", width))
		}
		if issue.Hint != "" {
			fmt.Fprintf(writer, "  help: %s\n", issue.Hint)
		}
		for _, related := range issue.Related {
			source := displaySource(validation.SanitizeSource(related.Span.Source), relativeTo)
			if related.Span.Line > 0 {
				source += fmt.Sprintf(":%d:%d", related.Span.Line, related.Span.Column)
			}
			fmt.Fprintf(writer, "  related: %s: %s\n", source, related.Message)
		}
	}
	if len(issues) > 1 {
		fmt.Fprintf(writer, "\n%d validation issues\n", len(issues))
	}
	return true
}

func displaySource(source, relativeTo string) string {
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") || strings.HasPrefix(source, "github:") || !filepath.IsAbs(source) || relativeTo == "" {
		return source
	}
	relative, err := filepath.Rel(relativeTo, source)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return source
	}
	return filepath.ToSlash(relative)
}
