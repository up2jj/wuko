// Package text provides steps that fix and check text files, the policies a pre-commit
// framework applies to the files a commit or push touches.
package text

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/up2jj/wuko/executor"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
)

// fileList is a list-of-paths field such as `files`: a literal list or `{expr: ...}`
// returning one.
type fileList struct {
	paths   []string
	program *vm.Program
	field   string
}

func parseFileList(raw map[string]any, field string) (fileList, error) {
	switch value := raw[field].(type) {
	case nil:
		return fileList{}, fmt.Errorf("%s is required", field)
	case []any:
		paths := make([]string, len(value))
		for index, item := range value {
			path, ok := item.(string)
			if !ok || strings.TrimSpace(path) == "" {
				return fileList{}, fmt.Errorf("%s[%d] must be a non-empty path", field, index)
			}
			paths[index] = path
		}
		return fileList{paths: paths}, nil
	case map[string]any:
		source, ok := value["expr"].(string)
		if len(value) != 1 || !ok || strings.TrimSpace(source) == "" {
			return fileList{}, fmt.Errorf("%s must be a list or an object containing exactly a non-empty expr", field)
		}
		program, err := wukoexpr.Compile(source, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return fileList{}, fmt.Errorf("compiling %s expr: %w", field, err)
		}
		return fileList{program: program, field: field}, nil
	default:
		return fileList{}, fmt.Errorf("%s must be a list or an object containing expr", field)
	}
}

// fileListReferences reports the path-list expressions to workflow validation.
func fileListReferences(raw map[string]any) (step.References, error) {
	var references step.References
	for _, field := range []string{"files", "size_exempt"} {
		binding, _ := raw[field].(map[string]any)
		reference := step.ExpressionField(binding, "expr")
		reference.Label = field + " expr"
		references.Expressions = append(references.Expressions, reference)
	}
	return references, nil
}

// resolve returns the configured paths, evaluating the expression form for this run.
func (list fileList) resolve(request step.Request) ([]string, error) {
	if list.program == nil {
		return list.paths, nil
	}
	value, err := expr.Run(list.program, request.ExpressionEnvironment(nil))
	if err != nil {
		return nil, fmt.Errorf("evaluating %s expr: %w", list.field, err)
	}
	items := reflect.ValueOf(value)
	if !items.IsValid() || (items.Kind() != reflect.Slice && items.Kind() != reflect.Array) {
		return nil, fmt.Errorf("%s expr must return a list of paths", list.field)
	}
	paths := make([]string, items.Len())
	for index := range items.Len() {
		path, ok := items.Index(index).Interface().(string)
		if !ok || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%s expr item %d must be a non-empty path", list.field, index)
		}
		paths[index] = path
	}
	return paths, nil
}

// regularFile resolves path against the run directory and reports whether it is a regular
// file. Symbolic links and directories have no text of their own and are skipped.
func regularFile(ctx context.Context, filesystem executor.FileSystem, request step.Request, path string) (string, executor.FileInfo, bool, error) {
	location := path
	if !filepath.IsAbs(location) {
		location = filepath.Join(request.RunDir, filepath.FromSlash(path))
	}
	info, err := filesystem.Stat(ctx, location)
	if err != nil {
		return "", executor.FileInfo{}, false, fmt.Errorf("inspecting %s: %w", path, err)
	}
	return location, info, info.Mode.IsRegular(), nil
}
