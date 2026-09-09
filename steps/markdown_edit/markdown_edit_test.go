package markdownedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestPrepareNextReleasePreservesExistingBody(t *testing.T) {
	t.Parallel()
	input := "### Next release (2026-09-09)\n\n- Add keyboard navigation to the command palette.\n- Preserve filters when returning to the dashboard.\n\n### 2026-08-19\n\n- Improve retry diagnostics for failed uploads.\n"
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{
			map[string]any{
				"operation": "set", "select": map[string]any{"kind": "heading", "level": 3, "text": "Next release (2026-09-09)"},
				"field": "text", "value": "2026-09-09",
			},
			map[string]any{
				"operation": "insert", "select": map[string]any{"kind": "heading", "level": 3, "text": "Next release (2026-09-09)"},
				"position": "before", "value": "### Next release (2026-09-27)\n\n- Add release notes here.\n",
			},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	want := "### Next release (2026-09-27)\n\n- Add release notes here.\n### 2026-09-09\n\n- Add keyboard navigation to the command palette.\n- Preserve filters when returning to the dashboard.\n\n### 2026-08-19\n\n- Improve retry diagnostics for failed uploads.\n"
	if got := result.Outputs["value"]; got != want {
		t.Fatalf("value:\n%s\nwant:\n%s", got, want)
	}
	if result.Outputs["count"] != 2 || result.Outputs["changed_count"] != 2 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestDuplicateHeadingRequiresDisambiguation(t *testing.T) {
	t.Parallel()
	input := "## Fixed\n\nfirst\n\n## Fixed\n\nsecond\n"
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Fixed"},
			"field": "text", "value": "Resolved",
		}},
	})
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err == nil || !strings.Contains(err.Error(), "found 2 matches") || !strings.Contains(err.Error(), "1:1, 5:1") {
		t.Fatalf("error = %v", err)
	}

	runner = newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Fixed", "occurrence": 2},
			"field": "text", "value": "Resolved",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "## Fixed\n\nfirst\n\n## Resolved\n\nsecond\n" {
		t.Fatalf("value = %q", got)
	}
}

func TestStructuralHeadingAnchors(t *testing.T) {
	t.Parallel()
	input := "# Version 2.0\n## Fixed\n# Version 1.9\n## Fixed\n"
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set",
			"select": map[string]any{
				"kind": "heading", "text": "Fixed",
				"after":  map[string]any{"kind": "heading", "text": "Version 2.0"},
				"before": map[string]any{"kind": "heading", "text": "Version 1.9"},
			},
			"field": "text", "value": "Bug fixes",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "# Version 2.0\n## Bug fixes\n# Version 1.9\n## Fixed\n" {
		t.Fatalf("value = %q", got)
	}
}

func TestHeadingAttributesAndSetextLevel(t *testing.T) {
	t.Parallel()
	t.Run("attribute", func(t *testing.T) {
		runner := newRunner(t, map[string]any{
			"from": map[string]any{"var": "document"},
			"edits": []any{map[string]any{
				"operation": "set", "select": map[string]any{"kind": "heading", "id": "install", "attributes": map[string]any{"owner": "docs"}},
				"field": "attribute.reviewed", "value": "true",
			}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "## Installation {#install .public owner=docs}\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Outputs["value"]; got != "## Installation {#install .public owner=docs reviewed=true}\n" {
			t.Fatalf("value = %q", got)
		}
	})
	t.Run("setext level", func(t *testing.T) {
		runner := newRunner(t, map[string]any{
			"from": map[string]any{"var": "document"},
			"edits": []any{map[string]any{
				"operation": "set", "select": map[string]any{"kind": "heading", "text": "Release"},
				"field": "level", "value": 3,
			}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "Release\n=======\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Outputs["value"]; got != "### Release\n" {
			t.Fatalf("value = %q", got)
		}
	})
}

func TestTaskCodeLinkAndTableFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
		edit              map[string]any
	}{
		{
			name: "task", input: "- [ ] Publish release\n", want: "- [x] Publish release\n",
			edit: map[string]any{"operation": "set", "select": map[string]any{"kind": "list_item", "task": true, "text": "Publish release"}, "field": "checked", "value": true},
		},
		{
			name: "code", input: "```ruby\nold\n```\n", want: "```ruby\nnew\nline\n```\n",
			edit: map[string]any{"operation": "set", "select": map[string]any{"kind": "code_block", "language": "ruby"}, "field": "content", "value": "new\nline"},
		},
		{
			name: "link", input: "Read the [guide](/old \"Setup\").\n", want: "Read the [guide](/new \"Setup\").\n",
			edit: map[string]any{"operation": "set", "select": map[string]any{"kind": "link", "destination": "/old"}, "field": "destination", "value": "/new"},
		},
		{
			name: "table cell", input: "| Service | Status |\n| --- | --- |\n| API | beta |\n", want: "| Service | Status |\n| --- | --- |\n| API | stable |\n",
			edit: map[string]any{
				"operation": "set",
				"select": map[string]any{
					"kind": "table_cell", "column": "Status", "header": false,
					"ancestor": map[string]any{"kind": "table", "headers": []any{"Service", "Status"}},
					"parent":   map[string]any{"kind": "table_row", "contains": map[string]any{"kind": "table_cell", "column": "Service", "text": "API"}},
				},
				"field": "content", "value": "stable",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newRunner(t, map[string]any{
				"from": map[string]any{"var": "document"}, "edits": []any{test.edit},
			})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": test.input}})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["value"]; got != test.want {
				t.Fatalf("value = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAppendAndDeleteTableRows(t *testing.T) {
	t.Parallel()
	input := "| Service | Status |\n| --- | --- |\n| API | beta |\n| Legacy API | retired |\n"
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{
			map[string]any{
				"operation": "append", "select": map[string]any{"kind": "table", "headers": []any{"Service", "Status"}},
				"value": []any{"Scheduler", "experimental"},
			},
			map[string]any{
				"operation": "delete", "select": map[string]any{
					"kind": "table_row", "ancestor": map[string]any{"kind": "table", "headers": []any{"Service", "Status"}},
					"contains": map[string]any{"kind": "table_cell", "column": "Service", "text": "Legacy API"},
				},
			},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	want := "| Service | Status |\n| --- | --- |\n| API | beta |\n| Scheduler | experimental |\n"
	if got := result.Outputs["value"]; got != want {
		t.Fatalf("value:\n%s\nwant:\n%s", got, want)
	}
}

func TestAppendTableRowAfterNonTerminatedDocument(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "append", "select": map[string]any{"kind": "table", "headers": []any{"Name", "State"}},
			"value": []any{"worker", "ready"},
		}},
	})
	input := "| Name | State |\n| --- | --- |\n| api | ready |"
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != input+"\n| worker | ready |" {
		t.Fatalf("value = %q", got)
	}
}

func TestReplaceAndInsertTableRows(t *testing.T) {
	t.Parallel()
	input := "| Service | Status |\n| --- | --- |\n| API | beta |\n| Worker | ready |\n"
	rowSelector := map[string]any{
		"kind":     "table_row",
		"ancestor": map[string]any{"kind": "table", "headers": []any{"Service", "Status"}},
		"contains": map[string]any{"kind": "table_cell", "column": "Service", "text": "API"},
	}
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{
			map[string]any{"operation": "replace", "select": rowSelector, "value": []any{"API", "stable"}},
			map[string]any{"operation": "insert", "select": rowSelector, "position": "after", "value": []any{"Scheduler", "experimental"}},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
	if err != nil {
		t.Fatal(err)
	}
	want := "| Service | Status |\n| --- | --- |\n| API | stable |\n| Scheduler | experimental |\n| Worker | ready |\n"
	if got := result.Outputs["value"]; got != want {
		t.Fatalf("value:\n%s\nwant:\n%s", got, want)
	}
}

func TestDeleteListItemDoesNotConsumeSibling(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "delete", "select": map[string]any{"kind": "list_item", "text": "first"},
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "- first\n- second\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "- second\n" {
		t.Fatalf("value = %q", got)
	}
}

func TestCodeFenceCollisionAndBlockquotePrefix(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set",
			"select":    map[string]any{"kind": "code_block", "language": "go", "ancestor": map[string]any{"kind": "blockquote"}},
			"field":     "content", "value": "```\nnew",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "> ```go\n> old\n> ```\n"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "> ````go\n> ```\n> new\n> ````\n"
	if got := result.Outputs["value"]; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestHeadingAttributeRemovalAndLinkTitleAddition(t *testing.T) {
	t.Parallel()
	t.Run("remove attribute", func(t *testing.T) {
		runner := newRunner(t, map[string]any{
			"from": map[string]any{"var": "document"},
			"edits": []any{map[string]any{
				"operation": "set", "select": map[string]any{"kind": "heading", "id": "install"},
				"field": "attribute.owner", "value": nil,
			}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "## Installation {#install owner=docs}\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Outputs["value"]; got != "## Installation {#install}\n" {
			t.Fatalf("value = %q", got)
		}
	})
	t.Run("add title", func(t *testing.T) {
		runner := newRunner(t, map[string]any{
			"from": map[string]any{"var": "document"},
			"edits": []any{map[string]any{
				"operation": "set", "select": map[string]any{"kind": "link", "text": "guide"},
				"field": "title", "value": "Read me",
			}},
		})
		result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "[guide](/docs)\n"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := result.Outputs["value"]; got != "[guide](/docs \"Read me\")\n" {
			t.Fatalf("value = %q", got)
		}
	})
}

func TestCRLFAndNoOpArePreserved(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Same"},
			"field": "text", "value": "Same",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "# Same\r\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["value"] != "# Same\r\n" || result.Outputs["changed"] != false || result.Outputs["changed_count"] != 0 {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
}

func TestCodeContentPreservesCRLF(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "code_block", "language": "go"},
			"field": "content", "value": "first\nsecond",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "```go\r\nold\r\n```\r\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "```go\r\nfirst\r\nsecond\r\n```\r\n" {
		t.Fatalf("value = %q", got)
	}
}

func TestTableHeaderRowCannotBeDeleted(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{map[string]any{
			"operation": "delete",
			"select":    map[string]any{"kind": "table_row", "contains": map[string]any{"kind": "table_cell", "text": "Service"}},
		}},
	})
	_, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "| Service | Status |\n| --- | --- |\n| API | beta |\n"}})
	if err == nil || !strings.Contains(err.Error(), "body rows only") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrependAndAppendEditNodeContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, kind, text, operation, value, want string
	}{
		{name: "heading", input: "## Notes\n", kind: "heading", text: "Notes", operation: "prepend", value: "Release ", want: "## Release Notes\n"},
		{name: "paragraph", input: "Some text.\n", kind: "paragraph", text: "Some text.", operation: "append", value: " More", want: "Some text. More\n"},
		{name: "link", input: "[guide](/docs)\n", kind: "link", text: "guide", operation: "prepend", value: "setup ", want: "[setup guide](/docs)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := newRunner(t, map[string]any{
				"from": map[string]any{"var": "document"},
				"edits": []any{map[string]any{
					"operation": test.operation, "select": map[string]any{"kind": test.kind, "text": test.text}, "value": test.value,
				}},
			})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": test.input}})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["value"]; got != test.want {
				t.Fatalf("value = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConflictingEditsDoNotWriteFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "README.md")
	input := []byte("# Title\n")
	if err := os.WriteFile(path, input, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"file": "README.md"},
		"edits": []any{
			map[string]any{"operation": "replace", "select": map[string]any{"kind": "heading", "text": "Title"}, "value": "# First"},
			map[string]any{"operation": "set", "select": map[string]any{"kind": "heading", "text": "Title"}, "field": "text", "value": "Second"},
		},
	})
	_, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(input) {
		t.Fatalf("file changed to %q", data)
	}
}

func TestExpressionSourceAndReplacement(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"expr": "vars.document"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "level": 2},
			"result": "all", "field": "text", "expr": `"Chapter " + string(index + 1) + ": " + current.text`,
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": "## One\n## Two\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Outputs["value"]; got != "## Chapter 1: One\n## Chapter 2: Two\n" {
		t.Fatalf("value = %q", got)
	}
}

func TestFileSourceWritesAtomicallyAndPreservesMode(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "README.md")
	if err := os.WriteFile(path, []byte("# Old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"file": "README.md"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Old"},
			"field": "text", "value": "New",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# New\n" || info.Mode().Perm() != 0o640 || result.Outputs["file"] != path {
		t.Fatalf("data = %q, mode = %o, outputs = %#v", data, info.Mode().Perm(), result.Outputs)
	}
}

func TestFileSourceRejectsSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "real.md"), []byte("# Real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.md", filepath.Join(directory, "link.md")); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"from":  map[string]any{"file": "link.md"},
		"edits": []any{map[string]any{"operation": "delete", "select": map[string]any{"kind": "heading"}}},
	})
	_, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestNoOpFileEditDoesNotRewrite(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "README.md")
	if err := os.WriteFile(path, []byte("# Same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"file": "README.md"},
		"edits": []any{map[string]any{
			"operation": "set", "select": map[string]any{"kind": "heading", "text": "Same"},
			"field": "text", "value": "Same",
		}},
	})
	result, err := runner.Run(t.Context(), step.Request{RunDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) || result.Outputs["changed"] != false {
		t.Fatalf("modtime = %s, outputs = %#v", info.ModTime(), result.Outputs)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, want string
		raw        map[string]any
	}{
		{
			name: "source union", want: "exactly one",
			raw: map[string]any{
				"from":  map[string]any{"file": "a", "var": "a"},
				"edits": []any{map[string]any{"operation": "delete", "select": map[string]any{"kind": "heading"}}},
			},
		},
		{name: "no edits", want: "at least one", raw: map[string]any{
			"from": map[string]any{"var": "a"}, "edits": []any{},
		}},
		{name: "unknown kind", want: "unsupported kind", raw: map[string]any{
			"from":  map[string]any{"var": "a"},
			"edits": []any{map[string]any{"operation": "delete", "select": map[string]any{"kind": "section"}}},
		}},
		{name: "set field", want: "field is required", raw: map[string]any{
			"from":  map[string]any{"var": "a"},
			"edits": []any{map[string]any{"operation": "set", "select": map[string]any{"kind": "heading"}, "value": "x"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func newRunner(t *testing.T, raw map[string]any) step.Runner {
	t.Helper()
	runner, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestDeleteAndInsertAtSameOffsetStayOrdered(t *testing.T) {
	t.Parallel()
	runner := newRunner(t, map[string]any{
		"from": map[string]any{"var": "document"},
		"edits": []any{
			map[string]any{
				"operation": "delete", "select": map[string]any{"kind": "paragraph", "text": "Alpha paragraph."},
			},
			map[string]any{
				"operation": "insert", "select": map[string]any{"kind": "paragraph", "text": "Alpha paragraph."},
				"position": "before", "value": "Inserted line.",
			},
		},
	})
	result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{
		"document": "# Title\n\nAlpha paragraph.\n\nBeta paragraph.\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Title\n\nInserted line.\n\nBeta paragraph.\n"
	if got := result.Outputs["value"]; got != want {
		t.Fatalf("value:\n%q\nwant:\n%q", got, want)
	}
}

func TestTableRowKeepsSurroundingIndentation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, want string
	}{
		{
			name:  "list item",
			input: "- item\n\n  | a | b |\n  | - | - |\n  | 1 | 2 |\n",
			want:  "- item\n\n  | a | b |\n  | - | - |\n  | 1 | 2 |\n  | 3 | 4 |\n",
		},
		{
			name:  "blockquote",
			input: "> | a | b |\n> | - | - |\n> | 1 | 2 |\n",
			want:  "> | a | b |\n> | - | - |\n> | 1 | 2 |\n> | 3 | 4 |\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newRunner(t, map[string]any{
				"from": map[string]any{"var": "document"},
				"edits": []any{map[string]any{
					"operation": "append", "select": map[string]any{"kind": "table"},
					"value": []any{"3", "4"},
				}},
			})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": test.input}})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["value"]; got != test.want {
				t.Fatalf("value:\n%q\nwant:\n%q", got, test.want)
			}
		})
	}
}

func TestTableCellEscapesBackslashes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, value, want string
	}{
		{name: "pipe", value: `a|b`, want: `a\|b`},
		{name: "escaped pipe", value: `a\|b`, want: `a\\\|b`},
		{name: "windows path", value: `C:\Users\logs`, want: `C:\\Users\\logs`},
		{name: "escaped emphasis", value: `a\*b\*c`, want: `a\\*b\\*c`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newRunner(t, map[string]any{
				"from": map[string]any{"var": "document"},
				"edits": []any{map[string]any{
					"operation": "set", "select": map[string]any{"kind": "table_cell", "text": "beta"},
					"field": "content", "value": test.value,
				}},
			})
			input := "| Service | Status |\n| --- | --- |\n| API | beta |\n"
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
			if err != nil {
				t.Fatal(err)
			}
			value, _ := result.Outputs["value"].(string)
			want := "| Service | Status |\n| --- | --- |\n| API | " + test.want + " |\n"
			if value != want {
				t.Fatalf("value = %q, want %q", value, want)
			}
			if got := lastTableCellText(t, value); got != test.value {
				t.Fatalf("cell reads back as %q, want %q", got, test.value)
			}
		})
	}
}

// lastTableCellText re-parses a rendered document and returns the text of its
// final table cell, so an escaping test can assert the value round trips.
func lastTableCellText(t *testing.T, source string) string {
	t.Helper()
	doc, err := parseDocument([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, node := range doc.nodes {
		if node.kind == "table_cell" {
			text = node.content()
		}
	}
	return text
}

func TestMultiLineSetextHeadingCoversUnderline(t *testing.T) {
	t.Parallel()
	input := "First line\nsecond line\n==========\n\nafter\n"
	tests := []struct {
		name, want string
		edit       map[string]any
	}{
		{
			name: "delete", want: "\nafter\n",
			edit: map[string]any{"operation": "delete", "select": map[string]any{"kind": "heading"}},
		},
		{
			name: "replace", want: "# New\n\nafter\n",
			edit: map[string]any{"operation": "replace", "value": "# New\n", "select": map[string]any{"kind": "heading"}},
		},
		{
			name: "demote to setext", want: "First line\nsecond line\n----------\n\nafter\n",
			edit: map[string]any{"operation": "set", "field": "level", "value": 2, "select": map[string]any{"kind": "heading"}},
		},
		{
			name: "convert to atx", want: "### First line second line\n\nafter\n",
			edit: map[string]any{"operation": "set", "field": "level", "value": 3, "select": map[string]any{"kind": "heading"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := newRunner(t, map[string]any{
				"from": map[string]any{"var": "document"}, "edits": []any{test.edit},
			})
			result, err := runner.Run(t.Context(), step.Request{Vars: map[string]any{"document": input}})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Outputs["value"]; got != test.want {
				t.Fatalf("value:\n%q\nwant:\n%q", got, test.want)
			}
		})
	}
}
