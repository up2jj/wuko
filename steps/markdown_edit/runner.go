package markdownedit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/expr-lang/expr"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/step"
)

type fileSource struct {
	path       string
	mode       os.FileMode
	filesystem executor.FileSystem
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	if r.maxBytes == 0 {
		return step.Result{}, fmt.Errorf("max_bytes contains an unresolved template")
	}
	source, file, err := r.resolveSource(ctx, request)
	if err != nil {
		return step.Result{}, err
	}
	document, err := parseDocument(source)
	if err != nil {
		return step.Result{}, err
	}

	patches := make([]patch, 0, len(r.config.Edits))
	matchesOutput := make([]any, 0)
	totalCount := 0
	changedCount := 0
	for editIndex := range r.config.Edits {
		if err := ctx.Err(); err != nil {
			return step.Result{}, err
		}
		edit := &r.config.Edits[editIndex]
		if templated(edit.Result) || templated(edit.Missing) || templated(edit.Position) || templated(edit.Field) {
			return step.Result{}, fmt.Errorf("edits[%d] contains an unresolved template", editIndex)
		}
		if !edit.hasValue && edit.Operation != "delete" && edit.program == nil {
			return step.Result{}, fmt.Errorf("edits[%d].expr contains an unresolved template", editIndex)
		}

		matches, err := document.selectNodes(&edit.Select)
		if err != nil {
			return step.Result{}, fmt.Errorf("edits[%d].select: %w", editIndex, err)
		}
		if edit.Select.Occurrence > 0 {
			occurrence := edit.Select.Occurrence
			if occurrence > len(matches) {
				matches = nil
			} else {
				matches = matches[occurrence-1 : occurrence]
			}
		}
		if len(matches) == 0 {
			if edit.Missing == "ignore" {
				continue
			}
			return step.Result{}, fmt.Errorf("edits[%d].select found no matching nodes", editIndex)
		}
		if edit.Result == "one" && len(matches) != 1 {
			return step.Result{}, fmt.Errorf("edits[%d].select found %d matches, want exactly one: %s",
				editIndex, len(matches), matchLocations(matches))
		}

		for matchIndex, match := range matches {
			if err := ctx.Err(); err != nil {
				return step.Result{}, err
			}
			value := edit.Value
			if !edit.hasValue && edit.Operation != "delete" {
				value, err = expr.Run(edit.program, request.ExpressionEnvironment(map[string]any{
					"current": match.descriptor(), "path": match.path, "index": matchIndex,
				}))
				if err != nil {
					return step.Result{}, fmt.Errorf("evaluating edits[%d].expr for %s: %w", editIndex, match.path, err)
				}
				if err := validateJSON(value); err != nil {
					return step.Result{}, fmt.Errorf("edits[%d].expr result is not JSON-compatible: %w", editIndex, err)
				}
			}
			planned, changed, err := document.plan(editIndex, matchIndex, edit, match, value)
			if err != nil {
				return step.Result{}, fmt.Errorf("edits[%d] %s at %d:%d: %w",
					editIndex, edit.Operation, match.line, match.column, err)
			}
			patches = append(patches, planned...)
			totalCount++
			if changed {
				changedCount++
			}
			matchesOutput = append(matchesOutput, map[string]any{
				"edit": editIndex, "operation": edit.Operation, "path": match.path,
				"kind": match.kind, "line": match.line, "column": match.column,
			})
		}
	}

	if err := validatePatches(patches); err != nil {
		return step.Result{}, err
	}
	updated := applyPatches(source, patches)
	if _, err := parseDocument(updated); err != nil {
		return step.Result{}, fmt.Errorf("verifying edited Markdown: %w", err)
	}
	if file != nil && changedCount > 0 {
		if err := file.filesystem.WriteFile(ctx, file.path, updated,
			executor.WriteOptions{Mode: file.mode, Replace: true}); err != nil {
			return step.Result{}, fmt.Errorf("installing edited file %s: %w", file.path, err)
		}
	}

	outputs := map[string]any{
		"value": string(updated), "changed": changedCount > 0,
		"count": totalCount, "changed_count": changedCount, "matches": matchesOutput,
	}
	if file != nil {
		outputs["file"] = file.path
	}
	return step.Result{Outputs: outputs}, nil
}

func (r *Runner) resolveSource(ctx context.Context, request step.Request) ([]byte, *fileSource, error) {
	switch {
	case r.config.From.Var != "":
		value, ok := request.Vars[r.config.From.Var]
		if !ok {
			return nil, nil, fmt.Errorf("variable %q is not defined", r.config.From.Var)
		}
		text, ok := value.(string)
		if !ok {
			return nil, nil, fmt.Errorf("variable %q is %T, want string", r.config.From.Var, value)
		}
		return []byte(text), nil, nil
	case r.config.From.Expr != "":
		if r.sourceExpr == nil {
			return nil, nil, fmt.Errorf("from.expr contains an unresolved template")
		}
		value, err := expr.Run(r.sourceExpr, request.ExpressionEnvironment(nil))
		if err != nil {
			return nil, nil, fmt.Errorf("evaluating from.expr: %w", err)
		}
		text, ok := value.(string)
		if !ok {
			return nil, nil, fmt.Errorf("from.expr returned %T, want string", value)
		}
		return []byte(text), nil, nil
	default:
		path := r.config.From.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(request.RunDir, path)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving Markdown file: %w", err)
		}
		filesystem, err := executor.FileSystemFor(request.Executor)
		if err != nil {
			return nil, nil, fmt.Errorf("editing %s: %w", absolute, err)
		}
		info, err := filesystem.Stat(ctx, absolute)
		if err != nil {
			return nil, nil, fmt.Errorf("inspecting Markdown file %s: %w", absolute, err)
		}
		if !info.Mode.IsRegular() {
			return nil, nil, fmt.Errorf("Markdown file %s must be a regular file", absolute)
		}
		if info.Size > r.maxBytes {
			return nil, nil, fmt.Errorf("Markdown file %s exceeds max_bytes", absolute)
		}
		data, err := filesystem.ReadFile(ctx, absolute, r.maxBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("reading Markdown file %s: %w", absolute, err)
		}
		if int64(len(data)) > r.maxBytes {
			return nil, nil, fmt.Errorf("Markdown file %s exceeds max_bytes", absolute)
		}
		return data, &fileSource{
			path: absolute, mode: info.Mode.Perm(), filesystem: filesystem,
		}, nil
	}
}
