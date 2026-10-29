package text

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

type fixConfig struct {
	Files              any   `yaml:"files"`
	TrailingWhitespace bool  `yaml:"trailing_whitespace,omitempty"`
	FinalNewline       bool  `yaml:"final_newline,omitempty"`
	Check              bool  `yaml:"check,omitempty"`
	FailOnChange       *bool `yaml:"fail_on_change,omitempty"`
}

type fixRunner struct {
	config       fixConfig
	files        fileList
	failOnChange bool
}

// NewFix builds a text_fix step, which removes trailing whitespace and normalizes the final
// newline of text files.
func NewFix(raw map[string]any) (step.Runner, error) {
	var config fixConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if !config.TrailingWhitespace && !config.FinalNewline {
		return nil, fmt.Errorf("enable at least one of trailing_whitespace and final_newline")
	}
	files, err := parseFileList(raw, "files")
	if err != nil {
		return nil, err
	}
	failOnChange := config.FailOnChange == nil || *config.FailOnChange
	return &fixRunner{config: config, files: files, failOnChange: failOnChange}, nil
}

func (*fixRunner) ExecutorAware()      {}
func (*fixRunner) ExecutorFileSystem() {}

func (runner *fixRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	paths, err := runner.files.resolve(request)
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
		fixed := original
		if runner.config.TrailingWhitespace {
			fixed = trimTrailingWhitespace(fixed)
		}
		if runner.config.FinalNewline {
			fixed = normalizeFinalNewline(fixed)
		}
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

// trimTrailingWhitespace removes spaces, tabs, vertical tabs, form feeds, and stray carriage
// returns from the end of every line. The carriage return of a CRLF ending is kept.
func trimTrailingWhitespace(data []byte) []byte {
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
		result = append(result, bytes.TrimRight(line, " \t\r\v\f")...)
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
