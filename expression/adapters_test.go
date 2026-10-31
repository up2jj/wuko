package expression

import (
	"strings"
	"testing"
)

func TestCompileHintsTemplateWrittenAsExpression(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ source, want string }{
		{"{{ .vars.timeout }}", "write vars.timeout instead of {{ .vars.timeout }}"},
		{"{{- .steps.probe.exit_code -}}", "write steps.probe.exit_code instead of"},
		{"{{ printf \"%ds\" .vars.seconds }}", "write the expression without {{ }}"},
	} {
		_, err := Compile(test.source)
		if err == nil || !strings.Contains(err.Error(), "hint: this field is an Expr expression, not a template") || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("Compile(%q) error = %v, want %q", test.source, err, test.want)
		}
	}
	if _, err := Compile("vars.x +"); err == nil || strings.Contains(err.Error(), "hint:") {
		t.Fatalf("ordinary syntax error = %v, want no template hint", err)
	}
}
