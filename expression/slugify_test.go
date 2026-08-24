package expression

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		options map[string]any
		want    string
	}{
		{name: "default slug", value: "  Déjà vu / API  ", want: "deja-vu-api"},
		{name: "drops non latin unicode", value: "東京 API", want: "api"},
		{name: "underscore separator", value: "Hello, world!", options: map[string]any{"separator": "_"}, want: "hello_world"},
		{name: "dot separator", value: "Hello, world!", options: map[string]any{"separator": "."}, want: "hello.world"},
		{name: "git hierarchy", value: "Feature / Payment API", options: map[string]any{"mode": "git"}, want: "feature/payment-api"},
		{name: "git flattened", value: "Feature / Payment API", options: map[string]any{"mode": "git", "preserve_slash": false}, want: "feature-payment-api"},
		{name: "git underscore", value: "Feature / Payment API", options: map[string]any{"mode": "git", "separator": "_"}, want: "feature/payment_api"},
		{name: "git edge punctuation", value: "/Feature//.lock/", options: map[string]any{"mode": "git"}, want: "feature/lock"},
		{name: "git trailing punctuation", value: "feature...", options: map[string]any{"mode": "git"}, want: "feature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Slugify(tt.value, tt.options)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Slugify(%q, %#v) = %q, want %q", tt.value, tt.options, got, tt.want)
			}
		})
	}
}

func TestSlugifyRejectsInvalidOptionsAndEmptyResults(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		options map[string]any
		want    string
	}{
		{name: "empty result", value: "---", want: "result is empty"},
		{name: "unknown option", value: "value", options: map[string]any{"unknown": true}, want: "unknown slugify option"},
		{name: "invalid mode", value: "value", options: map[string]any{"mode": "url"}, want: "must be \"slug\" or \"git\""},
		{name: "invalid separator type", value: "value", options: map[string]any{"separator": true}, want: "separator"},
		{name: "invalid separator value", value: "value", options: map[string]any{"separator": "--"}, want: "separator"},
		{name: "invalid preserve slash type", value: "value", options: map[string]any{"preserve_slash": "yes"}, want: "preserve_slash"},
		{name: "dot separator in git mode", value: "value", options: map[string]any{"mode": "git", "separator": "."}, want: "not supported in git mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Slugify(tt.value, tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSlugifyTemplateAndExprParity(t *testing.T) {
	gotTemplate, err := renderTemplate(`{{ "Feature / Payment API" | slugify (dict "mode" "git") }}`)
	if err != nil {
		t.Fatal(err)
	}
	gotExpr, err := Eval(`slugify("Feature / Payment API", {"mode": "git"})`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if gotTemplate != "feature/payment-api" || gotExpr != gotTemplate {
		t.Fatalf("template = %q, Expr = %#v", gotTemplate, gotExpr)
	}
}

func TestSlugifyTemplateArgumentErrors(t *testing.T) {
	for _, source := range []string{
		`{{ slugify 42 }}`,
		`{{ "value" | slugify (dict "separator" "--") }}`,
	} {
		if _, err := renderTemplate(source); err == nil {
			t.Fatalf("template %q succeeded", source)
		}
	}
}
