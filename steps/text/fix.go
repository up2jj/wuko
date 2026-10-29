package text

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

type fixConfig struct {
	Files                  any    `yaml:"files"`
	TrailingWhitespace     bool   `yaml:"trailing_whitespace,omitempty"`
	KeepMarkdownLineBreaks bool   `yaml:"keep_markdown_line_breaks,omitempty"`
	FinalNewline           bool   `yaml:"final_newline,omitempty"`
	LineEndings            string `yaml:"line_endings,omitempty"`
	RemoveByteOrderMark    bool   `yaml:"remove_byte_order_mark,omitempty"`
	MaxBlankLines          *int   `yaml:"max_blank_lines,omitempty"`
	Check                  bool   `yaml:"check,omitempty"`
	FailOnChange           *bool  `yaml:"fail_on_change,omitempty"`
}

// utf8ByteOrderMark is the encoded U+FEFF that some editors put at the start of UTF-8 text.
var utf8ByteOrderMark = []byte{0xEF, 0xBB, 0xBF}

type fixRunner struct {
	config       fixConfig
	files        step.PathList
	failOnChange bool
}

// NewFix builds a text_fix step, which normalizes whitespace, line endings, and byte order
// marks in text files.
func NewFix(raw map[string]any) (step.Runner, error) {
	var config fixConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if !config.TrailingWhitespace && !config.FinalNewline && config.LineEndings == "" && !config.RemoveByteOrderMark && config.MaxBlankLines == nil {
		return nil, fmt.Errorf("enable at least one of trailing_whitespace, final_newline, line_endings, remove_byte_order_mark, and max_blank_lines")
	}
	switch config.LineEndings {
	case "", "lf", "crlf", "auto":
	default:
		return nil, fmt.Errorf("line_endings must be lf, crlf, or auto")
	}
	if config.KeepMarkdownLineBreaks && !config.TrailingWhitespace {
		return nil, fmt.Errorf("keep_markdown_line_breaks requires trailing_whitespace")
	}
	if config.MaxBlankLines != nil && *config.MaxBlankLines < 0 {
		return nil, fmt.Errorf("max_blank_lines must not be negative")
	}
	files, err := requiredFiles(raw)
	if err != nil {
		return nil, err
	}
	failOnChange := config.FailOnChange == nil || *config.FailOnChange
	return &fixRunner{config: config, files: files, failOnChange: failOnChange}, nil
}

func (*fixRunner) ExecutorAware()      {}
func (*fixRunner) ExecutorFileSystem() {}

func (runner *fixRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	paths, err := runner.files.Resolve(request)
	if err != nil {
		return step.Result{}, err
	}
	filesystem, err := executor.FileSystemFor(request.Executor)
	if err != nil {
		return step.Result{}, err
	}
	changed := []any{}
	names := make([]string, 0)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		location, info, regular, err := regularFile(ctx, filesystem, request, path)
		if err != nil {
			return step.Result{}, err
		}
		if !regular {
			continue
		}
		original, err := filesystem.ReadFile(ctx, location, 0)
		if err != nil {
			return step.Result{}, fmt.Errorf("reading %s: %w", path, err)
		}
		// A NUL byte means binary content, where whitespace is data rather than formatting.
		if bytes.IndexByte(original, 0) >= 0 {
			continue
		}
		fixed := runner.fix(path, original)
		if bytes.Equal(fixed, original) {
			continue
		}
		changed = append(changed, path)
		names = append(names, path)
		if runner.config.Check {
			continue
		}
		if err := filesystem.WriteFile(ctx, location, fixed, executor.WriteOptions{Mode: info.Mode.Perm(), Replace: true}); err != nil {
			return step.Result{}, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	outputs := map[string]any{"changed": changed, "count": len(changed), "checked": len(paths)}
	if len(changed) > 0 && runner.failOnChange {
		if runner.config.Check {
			return step.Result{Outputs: outputs}, fmt.Errorf("%d file(s) need fixing: %s", len(changed), strings.Join(names, ", "))
		}
		return step.Result{Outputs: outputs}, fmt.Errorf("fixed %d file(s); review the changes: %s", len(changed), strings.Join(names, ", "))
	}
	return step.Result{Outputs: outputs}, nil
}

// fix applies the enabled rules in an order where each sees the previous one's result: line
// endings are settled before whitespace is trimmed, and blank lines are collapsed before the
// final newline is normalized.
func (runner *fixRunner) fix(path string, data []byte) []byte {
	config := runner.config
	if config.RemoveByteOrderMark {
		data = bytes.TrimPrefix(data, utf8ByteOrderMark)
	}
	if config.LineEndings != "" {
		data = convertLineEndings(data, config.LineEndings)
	}
	if config.TrailingWhitespace {
		extension := strings.ToLower(filepath.Ext(path))
		data = trimTrailingWhitespace(data, config.KeepMarkdownLineBreaks && (extension == ".md" || extension == ".markdown"))
	}
	if config.MaxBlankLines != nil {
		data = collapseBlankLines(data, *config.MaxBlankLines)
	}
	if config.FinalNewline {
		data = normalizeFinalNewline(data)
	}
	return data
}

// trimTrailingWhitespace removes spaces, tabs, vertical tabs, form feeds, and stray carriage
// returns from the end of every line. The carriage return of a CRLF ending is kept. With
// markdownBreaks, a non-blank line ending in two or more spaces keeps exactly two, the
// Markdown hard line break.
func trimTrailingWhitespace(data []byte, markdownBreaks bool) []byte {
	if !hasTrailingWhitespace(data) {
		return data
	}
	result := make([]byte, 0, len(data))
	for len(data) > 0 {
		line, rest, found := bytes.Cut(data, []byte{'\n'})
		ending := ""
		if found {
			ending = "\n"
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line, ending = line[:len(line)-1], "\r\n"
			}
		}
		trimmed := bytes.TrimRight(line, " \t\r\v\f")
		result = append(result, trimmed...)
		if markdownBreaks && len(trimmed) > 0 && bytes.HasSuffix(line, []byte("  ")) {
			result = append(result, "  "...)
		}
		result = append(result, ending...)
		data = rest
	}
	return result
}

func hasTrailingWhitespace(data []byte) bool {
	if len(data) > 0 && bytes.IndexByte([]byte(" \t\r\v\f"), data[len(data)-1]) >= 0 {
		return true
	}
	for line := range bytes.Lines(data) {
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > 0 && bytes.IndexByte([]byte(" \t\r\v\f"), line[len(line)-1]) >= 0 {
			return true
		}
	}
	return false
}

// normalizeFinalNewline ends non-empty text with exactly one line ending, keeping the style
// of the first trailing ending, and empties text that holds only line endings.
func normalizeFinalNewline(data []byte) []byte {
	body := bytes.TrimRight(data, "\r\n")
	if len(body) == 0 {
		return []byte{}
	}
	ending := data[len(body):]
	newline := "\n"
	switch {
	case bytes.HasPrefix(ending, []byte("\r\n")):
		newline = "\r\n"
	case len(ending) > 0:
		newline = string(ending[:1])
	}
	if string(ending) == newline {
		return data
	}
	return append(bytes.Clone(body), newline...)
}

// convertLineEndings rewrites LF, CRLF, and lone CR endings to one style. Auto picks the
// style the file uses most, preferring LF and then CRLF on a tie.
func convertLineEndings(data []byte, mode string) []byte {
	lf, crlf, cr := 0, 0, 0
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '\n':
			lf++
		case '\r':
			if index+1 < len(data) && data[index+1] == '\n' {
				crlf++
				index++
			} else {
				cr++
			}
		}
	}
	target := mode
	if mode == "auto" {
		switch {
		case lf >= crlf && lf >= cr:
			target = "lf"
		case crlf >= cr:
			target = "crlf"
		default:
			target = "cr"
		}
	}
	ending := map[string]string{"lf": "\n", "crlf": "\r\n", "cr": "\r"}[target]
	if (target == "lf" && crlf+cr == 0) || (target == "crlf" && lf+cr == 0) || (target == "cr" && lf+crlf == 0) {
		return data
	}
	result := make([]byte, 0, len(data)+crlf+lf+cr)
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '\n':
			result = append(result, ending...)
		case '\r':
			if index+1 < len(data) && data[index+1] == '\n' {
				index++
			}
			result = append(result, ending...)
		default:
			result = append(result, data[index])
		}
	}
	return result
}

// collapseBlankLines keeps at most limit consecutive blank lines. A line holding only spaces,
// tabs, vertical tabs, or form feeds counts as blank.
func collapseBlankLines(data []byte, limit int) []byte {
	result := make([]byte, 0, len(data))
	run := 0
	for line := range bytes.Lines(data) {
		if len(bytes.TrimRight(line, " \t\v\f\r\n")) > 0 {
			run = 0
			result = append(result, line...)
			continue
		}
		run++
		if run <= limit {
			result = append(result, line...)
		}
	}
	if len(result) == len(data) {
		return data
	}
	return result
}
