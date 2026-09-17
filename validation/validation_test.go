package validation_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/up2jj/wuko/validation"
)

func TestCollectorSortsDeduplicatesAndUnwraps(t *testing.T) {
	firstCause := errors.New("cause")
	first := validation.Issue{Code: validation.CodeInvalidValue, Message: "later", Cause: firstCause}.
		WithSpan(validation.Span{Source: "workflow.yaml", Line: 4, Column: 2})
	earlier := validation.Issue{Code: validation.CodeMissingField, Message: "earlier"}.
		WithSpan(validation.Span{Source: "workflow.yaml", Line: 2, Column: 1})

	var collector validation.Collector
	collector.Add(first)
	collector.Add(earlier)
	collector.Add(first)
	err := collector.Err()
	if !errors.Is(err, firstCause) {
		t.Fatalf("errors.Is(cause) = false: %v", err)
	}
	issues := validation.Issues(fmt.Errorf("wrapped: %w", err))
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	if issues[0].Message != "earlier" {
		t.Fatalf("first issue = %q", issues[0].Message)
	}
}

func TestSuggestion(t *testing.T) {
	tests := []struct {
		value      string
		candidates []string
		want       string
	}{
		{"sttaus", []string{"body", "status", "headers"}, "status"},
		{"completely-different", []string{"status"}, ""},
		{"STATUS", []string{"status"}, "status"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			if got := validation.Suggest(test.value, test.candidates); got != test.want {
				t.Fatalf("Suggest() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSourceIndexAttachesSpanAndRedactsExcerpt(t *testing.T) {
	data := []byte("name: sample\nsteps:\n  - id: fetch\n    token: super-secret\n")
	index, err := validation.NewSourceIndex("workflow.yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	issue := index.Attach(validation.InvalidValue(validation.Path("steps[0].token"), "bad", nil))
	if issue.Span.Line != 4 || issue.Span.Column != 12 {
		t.Fatalf("span = %+v", issue.Span)
	}
	if strings.Contains(issue.SourceLine, "super-secret") || !strings.Contains(issue.SourceLine, "<redacted>") {
		t.Fatalf("excerpt was not redacted: %q", issue.SourceLine)
	}
}

func TestSafeProjectionExcludesExcerptAndCause(t *testing.T) {
	issue := validation.Issue{
		Code: validation.CodeInvalidValue, Message: "safe", Hint: "hint",
		SourceLine: "token: secret", Cause: errors.New("raw secret"),
	}
	data, err := json.Marshal(validation.NewDocument([]validation.Issue{issue}))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token: secret", "raw secret", "source_line", "cause"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("JSON contains %q: %s", forbidden, data)
		}
	}
	direct, err := json.Marshal(issue.WithSpan(validation.Span{Source: "https://user:password@example.test/workflow.yaml?token=secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(direct), "password") || strings.Contains(string(direct), "token=secret") {
		t.Fatalf("direct issue JSON leaked source credentials: %s", direct)
	}
}

func TestConstructorsOwnWording(t *testing.T) {
	issue := validation.UnknownStepOutput("steps[0].with.url", "fetch", "sttaus", []string{"body", "status"})
	if issue.Code != validation.CodeUnknownStepOutput {
		t.Fatalf("code = %q", issue.Code)
	}
	if issue.Message != `step "fetch" has no output "sttaus"` {
		t.Fatalf("message = %q", issue.Message)
	}
	if issue.Hint != `did you mean "status"?` {
		t.Fatalf("hint = %q", issue.Hint)
	}
}

func TestDuplicateDeclarationCarriesRelatedSpan(t *testing.T) {
	previous := validation.Span{Source: "workflow.yaml", Line: 3, Column: 9}
	issue := validation.DuplicateDeclaration("steps[2].id", "step id", "build", previous)
	if issue.Code != validation.CodeDuplicateDeclaration || len(issue.Related) != 1 || issue.Related[0].Span != previous {
		t.Fatalf("issue = %#v", issue)
	}
}

func TestSourceExcerptRedactsTopLevelAndFlowKeys(t *testing.T) {
	lines := []string{"token: abc123", "password: abc123", "api_key: abc123", "with: {command: x, token: abc123}", "type: tui_password"}
	index := validation.NewLazySourceIndex("wuko.yaml", []byte(strings.Join(lines, "\n")+"\n"))
	for number := 1; number < len(lines); number++ {
		if got := index.Excerpt(number); strings.Contains(got, "abc123") {
			t.Fatalf("Excerpt(%d) = %q, leaked value", number, got)
		}
	}
	if got := index.Excerpt(len(lines)); got != "type: tui_password" {
		t.Fatalf("non-sensitive line was redacted: %q", got)
	}
}
