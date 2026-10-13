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
	return WriteValidationColor(writer, err, relativeTo, false)
}

// WriteValidationColor renders the same diagnostics as WriteValidation, adding
// ANSI color when color is true.
func WriteValidationColor(writer io.Writer, err error, relativeTo string, color bool) bool {
	paint := func(code, value string) string {
		if !color || value == "" {
			return value
		}
		return "\x1b[" + code + "m" + value + "\x1b[0m"
	}
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
			location = paint("36", location) + ": "
		}
		fmt.Fprintf(writer, "%s%s %s\n", location, paint("1;31", "error["+string(issue.Code)+"]:"), paint("1", issue.Message))
		if issue.Path != "" {
			fmt.Fprintf(writer, "  %s %s\n", paint("2", "path:"), issue.Path)
		}
		if issue.SourceLine != "" {
			gutter := paint("34", "|")
			fmt.Fprintf(writer, "  %s %s\n", gutter, issue.SourceLine)
			column := max(1, issue.Span.Column)
			width := 1
			if issue.Span.EndLine == issue.Span.Line && issue.Span.EndColumn >= column {
				width = issue.Span.EndColumn - column + 1
			}
			width = min(width, max(1, utf8.RuneCountInString(issue.SourceLine)-column+1))
			fmt.Fprintf(writer, "  %s %s%s\n", gutter, strings.Repeat(" ", column-1), paint("1;31", strings.Repeat("^", width)))
		}
		if issue.Hint != "" {
			fmt.Fprintf(writer, "  %s %s\n", paint("1;32", "help:"), issue.Hint)
		}
		for _, related := range issue.Related {
			source := displaySource(validation.SanitizeSource(related.Span.Source), relativeTo)
			if related.Span.Line > 0 {
				source += fmt.Sprintf(":%d:%d", related.Span.Line, related.Span.Column)
			}
			fmt.Fprintf(writer, "  %s %s: %s\n", paint("2", "related:"), paint("36", source), related.Message)
		}
	}
	if len(issues) > 1 {
		fmt.Fprintf(writer, "\n%s\n", paint("1;31", fmt.Sprintf("%d validation issues", len(issues))))
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
