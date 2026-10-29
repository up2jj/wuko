package text

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/internal/dataformat"
	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"go.yaml.in/yaml/v3"
)

type checkConfig struct {
	Files      any             `yaml:"files"`
	Forbid     []forbiddenText `yaml:"forbid,omitempty"`
	MaxSize    string          `yaml:"max_size,omitempty"`
	SizeExempt any             `yaml:"size_exempt,omitempty"`
	Parse      string          `yaml:"parse,omitempty"`
}

// forbiddenText is one forbid entry: a bare pattern, or a pattern with the message to report.
type forbiddenText struct {
	Pattern string `yaml:"pattern"`
	Message string `yaml:"message,omitempty"`
}

func (entry *forbiddenText) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		entry.Pattern = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("forbid entries must be a pattern or an object with pattern and message")
	}
	// A custom decoder does not inherit the strict decoder's unknown-field check.
	for index := 0; index < len(node.Content); index += 2 {
		if key := node.Content[index].Value; key != "pattern" && key != "message" {
			return fmt.Errorf("forbid entry field %s not found", key)
		}
	}
	type plain forbiddenText
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*entry = forbiddenText(decoded)
	return nil
}

type forbiddenPattern struct {
	pattern *regexp.Regexp
	message string
}

type checkRunner struct {
	config     checkConfig
	files      step.PathList
	sizeExempt step.PathList
	forbid     []forbiddenPattern
	maxSize    int64
}

// NewCheck builds a text_check step, which reports files that contain forbidden text, exceed
// a size, or fail to parse.
func NewCheck(raw map[string]any) (step.Runner, error) {
	var config checkConfig
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if len(config.Forbid) == 0 && config.MaxSize == "" && config.Parse == "" {
		return nil, fmt.Errorf("configure at least one of forbid, max_size, and parse")
	}
	files, err := requiredFiles(raw)
	if err != nil {
		return nil, err
	}
	runner := &checkRunner{config: config, files: files}
	for index, entry := range config.Forbid {
		if strings.TrimSpace(entry.Pattern) == "" {
			return nil, fmt.Errorf("forbid[%d] pattern must not be blank", index)
		}
		compiled, err := regexp.Compile(entry.Pattern)
		if err != nil {
			return nil, fmt.Errorf("forbid[%d]: invalid pattern: %w", index, err)
		}
		message := entry.Message
		if message == "" {
			message = fmt.Sprintf("matches forbidden pattern %q", entry.Pattern)
		}
		runner.forbid = append(runner.forbid, forbiddenPattern{pattern: compiled, message: message})
	}
	if config.MaxSize != "" && !strings.Contains(config.MaxSize, "{{") {
		size, err := process.ParseCaptureLimit(config.MaxSize)
		if err != nil {
			return nil, fmt.Errorf("max_size %w", err)
		}
		runner.maxSize = size
	}
	if config.SizeExempt != nil {
		if config.MaxSize == "" {
			return nil, fmt.Errorf("size_exempt requires max_size")
		}
		if runner.sizeExempt, err = step.ParsePathList(raw, "size_exempt"); err != nil {
			return nil, err
		}
	}
	switch config.Parse {
	case "", "json", "yaml", "toml":
	default:
		return nil, fmt.Errorf("parse must be json, yaml, or toml")
	}
	return runner, nil
}

func (*checkRunner) ExecutorAware()      {}
func (*checkRunner) ExecutorFileSystem() {}

func (runner *checkRunner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if strings.Contains(runner.config.MaxSize, "{{") {
		return step.Result{}, fmt.Errorf("max_size contains an unresolved template")
	}
	paths, err := runner.files.Resolve(request)
	if err != nil {
		return step.Result{}, err
	}
	exempt := make(map[string]bool)
	if runner.config.SizeExempt != nil {
		exemptPaths, err := runner.sizeExempt.Resolve(request)
		if err != nil {
			return step.Result{}, err
		}
		for _, path := range exemptPaths {
			exempt[path] = true
		}
	}
	filesystem, err := executor.FileSystemFor(request.Executor)
	if err != nil {
		return step.Result{}, err
	}
	failures := []any{}
	var report []string
	fail := func(path string, line int, message string) {
		failures = append(failures, map[string]any{"path": path, "line": line, "message": message})
		location := path
		if line > 0 {
			location = fmt.Sprintf("%s:%d", path, line)
		}
		report = append(report, location+": "+message)
	}
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
		if runner.maxSize > 0 && info.Size > runner.maxSize && !exempt[path] {
			fail(path, 0, fmt.Sprintf("%d bytes exceeds max_size %s", info.Size, runner.config.MaxSize))
			continue
		}
		if len(runner.forbid) == 0 && runner.config.Parse == "" {
			continue
		}
		content, err := filesystem.ReadFile(ctx, location, 0)
		if err != nil {
			return step.Result{}, fmt.Errorf("reading %s: %w", path, err)
		}
		for _, forbidden := range runner.forbid {
			// Only the position is reported: the matched text may be a secret.
			if match := forbidden.pattern.FindIndex(content); match != nil {
				fail(path, bytes.Count(content[:match[0]], []byte{'\n'})+1, forbidden.message)
			}
		}
		if err := parseDocument(runner.config.Parse, string(content)); err != nil {
			fail(path, 0, fmt.Sprintf("invalid %s: %v", strings.ToUpper(runner.config.Parse), err))
		}
	}
	outputs := map[string]any{"checked": len(paths), "count": len(failures), "failures": failures}
	if len(failures) > 0 {
		return step.Result{Outputs: outputs}, fmt.Errorf("%d problem(s) found:\n%s", len(failures), strings.Join(report, "\n"))
	}
	return step.Result{Outputs: outputs}, nil
}

func parseDocument(format, text string) error {
	switch format {
	case "json":
		return dataformat.DecodeJSON(text, new(json.RawMessage))
	case "yaml":
		_, err := dataformat.DecodeYAML(text, true)
		return err
	case "toml":
		_, err := dataformat.DecodeTOML(text)
		return err
	}
	return nil
}
