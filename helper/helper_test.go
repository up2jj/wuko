package helper

import (
	"context"
	"strings"
	"testing"
	"text/template"

	"github.com/expr-lang/expr"
)

func TestSetExposesNativeExprAndTemplateFunctions(t *testing.T) {
	set := Set{"acme_slug": func(_ context.Context, args []any) (any, error) {
		return args[0].(string) + "-slug", nil
	}}
	program, err := expr.Compile(`acme_slug("demo")`, expr.AllowUndefinedVariables())
	if err != nil {
		t.Fatal(err)
	}
	value, err := expr.Run(program, set.Functions(t.Context()))
	if err != nil || value != "demo-slug" {
		t.Fatalf("Expr value = %#v, error = %v", value, err)
	}
	parsed, err := template.New("test").Funcs(set.TemplateFuncs(t.Context())).Parse(`{{ acme_slug "demo" }}`)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := parsed.Execute(&output, nil); err != nil {
		t.Fatal(err)
	}
	if output.String() != "demo-slug" {
		t.Fatalf("template output = %q", output.String())
	}
}
