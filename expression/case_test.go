package expression

import (
	"strings"
	"testing"
)

func TestCaseHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		run   func(string) string
		value string
		want  string
	}{
		{name: "alternate", run: AlternateCase, value: "hello, WORLD!", want: "hElLo, WoRlD!"},
		{name: "camel", run: CamelCase, value: "user first name", want: "userFirstName"},
		{name: "capitalize", run: Capitalize, value: "iPhone  API", want: "IPhone  API"},
		{name: "constant", run: ConstantCase, value: "max retry count", want: "MAX_RETRY_COUNT"},
		{name: "dot", run: DotCase, value: "user first name", want: "user.first.name"},
		{name: "kebab", run: KebabCase, value: "userFirstName", want: "user-first-name"},
		{name: "pascal", run: PascalCase, value: "user first name", want: "UserFirstName"},
		{name: "sentence", run: SentenceCase, value: "FIRST one. SECOND one! THIRD? YES", want: "First one. Second one! Third? Yes"},
		{name: "snake", run: SnakeCase, value: "userFirstName", want: "user_first_name"},
		{name: "swap", run: SwapCase, value: "Hello World", want: "hELLO wORLD"},
		{name: "title", run: TitleCase, value: "tHE  QUICK", want: "The  Quick"},
		{name: "train", run: TrainCase, value: "user first name", want: "User-First-Name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.run(test.value); got != test.want {
				t.Fatalf("value = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCaseHelpersSplitWordsLikeTXC(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  string
	}{
		{value: "userFirstName", want: "user_first_name"},
		{value: "parseHTTPResponse", want: "parse_http_response"},
		{value: "version2Point0", want: "version_2_point_0"},
		{value: "snake_case-kebab.dot", want: "snake_case_kebab_dot"},
		{value: "ŻółtyHTTPSerwerⅧ", want: "żółty_http_serwer_ⅷ"},
		{value: "", want: ""},
		{value: "---", want: ""},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			if got := SnakeCase(test.value); got != test.want {
				t.Fatalf("SnakeCase(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestCaseHelpersPreserveLineEndingsAndResetLineState(t *testing.T) {
	t.Parallel()
	if got, want := AlternateCase("abc\r\ndef\n\n"), "aBc\r\ndEf\n\n"; got != want {
		t.Fatalf("AlternateCase() = %q, want %q", got, want)
	}
	if got, want := SnakeCase("userFirst\r\nHTTPServer\n\n"), "user_first\r\nhttp_server\n\n"; got != want {
		t.Fatalf("SnakeCase() = %q, want %q", got, want)
	}
}

func TestCapitalizeTitleAndSentenceCaseRemainDistinct(t *testing.T) {
	t.Parallel()
	value := "iPhone API. NEXT item"
	if got, want := Capitalize(value), "IPhone API. NEXT Item"; got != want {
		t.Fatalf("Capitalize() = %q, want %q", got, want)
	}
	if got, want := TitleCase(value), "Iphone Api. Next Item"; got != want {
		t.Fatalf("TitleCase() = %q, want %q", got, want)
	}
	if got, want := SentenceCase(value), "Iphone api. Next item"; got != want {
		t.Fatalf("SentenceCase() = %q, want %q", got, want)
	}
}

func TestCaseHelpersAcrossTemplateAndExpr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		template   string
		expression string
		want       string
	}{
		{name: "alternate", template: `{{ "hello" | alternateCase }}`, expression: `alternateCase("hello")`, want: "hElLo"},
		{name: "camel", template: `{{ "user first name" | camelCase }}`, expression: `camelCase("user first name")`, want: "userFirstName"},
		{name: "capitalize", template: `{{ "iPhone case" | capitalize }}`, expression: `capitalize("iPhone case")`, want: "IPhone Case"},
		{name: "constant", template: `{{ "max retry count" | constantCase }}`, expression: `constantCase("max retry count")`, want: "MAX_RETRY_COUNT"},
		{name: "dot", template: `{{ "user first name" | dotCase }}`, expression: `dotCase("user first name")`, want: "user.first.name"},
		{name: "kebab", template: `{{ "userFirstName" | kebabCase }}`, expression: `kebabCase("userFirstName")`, want: "user-first-name"},
		{name: "pascal", template: `{{ "user first name" | pascalCase }}`, expression: `pascalCase("user first name")`, want: "UserFirstName"},
		{name: "sentence", template: `{{ "FIRST. SECOND" | sentenceCase }}`, expression: `sentenceCase("FIRST. SECOND")`, want: "First. Second"},
		{name: "snake", template: `{{ "userFirstName" | snakeCase }}`, expression: `snakeCase("userFirstName")`, want: "user_first_name"},
		{name: "swap", template: `{{ "Hello" | swapCase }}`, expression: `swapCase("Hello")`, want: "hELLO"},
		{name: "title", template: `{{ "tHE title" | titleCase }}`, expression: `titleCase("tHE title")`, want: "The Title"},
		{name: "train", template: `{{ "user first name" | trainCase }}`, expression: `trainCase("user first name")`, want: "User-First-Name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotTemplate, err := renderTemplate(test.template)
			if err != nil {
				t.Fatalf("template: %v", err)
			}
			gotExpr, err := Eval(test.expression, map[string]any{})
			if err != nil {
				t.Fatalf("Expr: %v", err)
			}
			if gotTemplate != test.want || gotExpr != test.want {
				t.Fatalf("template = %q, Expr = %#v, want %q", gotTemplate, gotExpr, test.want)
			}
		})
	}
}

func TestCaseHelpersRejectNonStrings(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"alternateCase", "camelCase", "capitalize", "constantCase", "dotCase", "kebabCase",
		"pascalCase", "sentenceCase", "snakeCase", "swapCase", "titleCase", "trainCase",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Eval(name+"(1)", map[string]any{}); err == nil {
				t.Fatalf("Expr %s accepted a number", name)
			}
			if _, err := renderTemplate("{{ 1 | " + name + " }}"); err == nil {
				t.Fatalf("template %s accepted a number", name)
			}
		})
	}
}

func TestCaseCatalogExcludesRandomCase(t *testing.T) {
	t.Parallel()
	if _, exists := TemplateFuncs()["randomCase"]; exists {
		t.Fatal("randomCase must not be exposed to templates")
	}
	_, err := Eval(`randomCase("value")`, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "unknown name randomCase") {
		t.Fatalf("Eval() error = %v, want unknown randomCase", err)
	}
}
