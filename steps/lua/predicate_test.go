package lua

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/step"
)

func TestPredicateEvaluatesTypedValueAndWorkflowContext(t *testing.T) {
	predicate, err := CompilePredicate(`function(value, context) return value >= context.vars.minimum and context.env.MODE == "test" end`)
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{Vars: map[string]any{"minimum": 2}, Env: map[string]string{"MODE": "test"}}
	for _, test := range []struct {
		value int64
		want  bool
	}{{1, false}, {2, true}} {
		actual, err := predicate.Evaluate(t.Context(), request, test.value)
		if err != nil || actual != test.want {
			t.Fatalf("Evaluate(%d)=%v,%v", test.value, actual, err)
		}
	}
}

func TestPredicateLoadsReusableWorkflowModule(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "lua"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := `return { name = function(value) return value == "Ada" end }`
	testfs.WriteFile(t, filepath.Join(root, "lua", "validators.lua"), []byte(source), 0o600)
	predicate, err := CompilePredicate(`require("validators").name`)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := predicate.Evaluate(t.Context(), step.Request{WorkflowDir: root}, "Ada")
	if err != nil || !valid {
		t.Fatalf("valid=%v error=%v", valid, err)
	}
	if _, err := predicate.Evaluate(t.Context(), step.Request{WorkflowDir: root, WorkflowDirBorrowed: true}, "Ada"); err == nil {
		t.Fatal("borrowed workflow module directory was used")
	}
}

func TestPredicateUsesFreshStateAndCannotChangeWorkflowData(t *testing.T) {
	predicate, err := CompilePredicate(`function(value, context)
  if seen then return false end
  seen = true
  context.vars.name = "changed"
  return value[1] == "one"
 end`)
	if err != nil {
		t.Fatal(err)
	}
	request := step.Request{Vars: map[string]any{"name": "original"}}
	for range 2 {
		matched, err := predicate.Evaluate(t.Context(), request, []any{"one"})
		if err != nil || !matched || request.Vars["name"] != "original" {
			t.Fatalf("matched=%v error=%v vars=%v", matched, err, request.Vars)
		}
	}
}

func TestPredicateRejectsSyntaxAndInvalidResults(t *testing.T) {
	if _, err := CompilePredicate(`function(value) return`); err == nil {
		t.Fatal("invalid syntax compiled")
	}
	for _, source := range []string{`true`, `function(value) return "true" end`, `function(value) error("failed") end`} {
		predicate, err := CompilePredicate(source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := predicate.Evaluate(t.Context(), step.Request{}, "input"); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func TestPredicateHonorsCancellationAndDeadline(t *testing.T) {
	predicate, err := CompilePredicate(`function(value) while true do end end`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := predicate.Evaluate(ctx, step.Request{}, "input"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	cancel()
	if _, err := predicate.Evaluate(ctx, step.Request{}, "input"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestPredicateTreatsTemplateDelimitersAsLiteralLua(t *testing.T) {
	predicate, err := CompilePredicate(`function(value) return value == "{{ .vars.not_a_template }}" end`)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := predicate.Evaluate(t.Context(), step.Request{}, "{{ .vars.not_a_template }}")
	if err != nil || !matched {
		t.Fatalf("matched=%v error=%v", matched, err)
	}
}
