package expression

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func TestBuildConventionalCommit(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{
			name: "basic",
			config: map[string]any{
				"type": "FEAT", "scope": "Workflow", "subject": "add commit support",
			},
			want: "feat(Workflow): add commit support",
		},
		{
			name: "breaking body",
			config: map[string]any{
				"type": "feat", "scope": "api", "subject": "replace authentication", "breaking": true,
				"body": "\r\nClients must use OAuth.\r\n\r\nAPI keys are no longer accepted.\r\n",
			},
			want: "feat(api)!: replace authentication\n\nClients must use OAuth.\n\nAPI keys are no longer accepted.",
		},
		{
			name: "unchecked task",
			config: map[string]any{
				"type": "fix", "subject": "prevent expired sessions", "task": "WUKO-142",
			},
			want: "fix: prevent expired sessions WUKO-142",
		},
		{
			name: "checked task with body",
			config: map[string]any{
				"type": "fix", "subject": "prevent expired sessions", "task": "WUKO-142",
				"task_regex": `WUKO-[0-9]+`, "body": "Clear cached credentials.",
			},
			want: "fix: prevent expired sessions WUKO-142\n\nClear cached credentials.",
		},
		{
			name: "custom type compatibility",
			config: map[string]any{
				"type": "custom", "subject": "support a project convention", "types": []any{"custom"},
			},
			want: "custom: support a project convention",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildConventionalCommit(tt.config)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildConventionalCommitRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"missing type", map[string]any{"subject": "message"}, "type is required"},
		{"missing subject", map[string]any{"type": "feat"}, "subject is required"},
		{"unknown type", map[string]any{"type": "custom", "subject": "message"}, "not allowed"},
		{"scope required", map[string]any{"type": "feat", "subject": "message", "force_scope": true}, "scope is required"},
		{"scope allowlist", map[string]any{"type": "feat", "scope": "other", "subject": "message", "scopes": []any{"api"}}, "allowed scopes"},
		{"regex without task", map[string]any{"type": "feat", "subject": "message", "task_regex": `WUKO-[0-9]+`}, "requires task"},
		{"task mismatch", map[string]any{"type": "feat", "subject": "message", "task": "OTHER-1", "task_regex": `WUKO-[0-9]+`}, "does not match"},
		{"empty task match", map[string]any{"type": "feat", "subject": "message", "task": "WUKO-1", "task_regex": `.*`}, "empty task"},
		{"bad regex", map[string]any{"type": "feat", "subject": "message", "task": "WUKO-1", "task_regex": `(`}, "compiling task_regex"},
		{"unknown option", map[string]any{"type": "feat", "subject": "message", "strict": true}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildConventionalCommit(tt.config)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestInspectConventionalCommitMatchesUpstreamBehavior(t *testing.T) {
	tests := []struct {
		name    string
		message string
		options map[string]any
		valid   bool
		class   string
	}{
		{"default", "feat: message", nil, true, "conventional"},
		{"uppercase", "FIX: message", nil, true, "conventional"},
		{"breaking", "fix!: message", nil, true, "conventional"},
		{"scope", "feat(customer.registration): add OAuth", nil, true, "conventional"},
		{"hash scope", "feat(#scope): message", nil, true, "conventional"},
		{"allowed multiple scopes", "feat(api, client): message", map[string]any{"scopes": []any{"api", "client"}}, true, "conventional"},
		{"custom type adds conventional types", "feat: message", map[string]any{"types": []any{"custom"}}, true, "conventional"},
		{"custom type", "custom: message", map[string]any{"types": []any{"custom"}}, true, "conventional"},
		{"body", "feat: message\n\nA body.\n\nAnother paragraph.", nil, true, "conventional"},
		{"crlf body", "feat: message\r\n\r\nA body.\r\n", nil, true, "conventional"},
		{"comments", "feat: message\n# editor comment\n", nil, true, "conventional"},
		{"verbose diff", "feat: message\n\nBody\n" + verboseCommitMarker + "\ndiff --git a/a b/a", nil, true, "conventional"},
		{"merge exemption", "Merge branch 'dev' into 'main'", nil, true, "merge"},
		{"lower merge exemption", "merge branch 'dev' into 'main'", nil, true, "merge"},
		{"fixup exemption", "fixup! feature: implement something", nil, true, "autosquash"},
		{"strict merge", "Merge branch 'dev' into 'main'", map[string]any{"strict": true}, false, ""},
		{"strict fixup", "fixup! feature: implement something", map[string]any{"strict": true}, false, ""},
		{"bad type", "wrong: message", nil, false, ""},
		{"missing separator", "feat message", nil, false, ""},
		{"empty subject", "feat: ", nil, false, ""},
		{"body without blank line", "feat: message\nbody", nil, false, ""},
		{"forced scope missing", "feat: message", map[string]any{"force_scope": true}, false, ""},
		{"empty scope", "feat(): message", nil, false, ""},
		{"bad scope character", "feat(%scope): message", nil, false, ""},
		{"disallowed scope", "feat(other): message", map[string]any{"scopes": []any{"api"}}, false, ""},
		{"merge sort is not merge", "MergeSort implemented", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := InspectConventionalCommit(tt.message, tt.options)
			if tt.valid {
				if err != nil {
					t.Fatal(err)
				}
				if !result.Valid || result.Classification != tt.class {
					t.Fatalf("result = %#v", result)
				}
				return
			}
			if err == nil {
				t.Fatalf("InspectConventionalCommit(%q) succeeded: %#v", tt.message, result)
			}
			valid, predicateErr := IsConventionalCommit(tt.message, tt.options)
			if predicateErr != nil || valid {
				t.Fatalf("predicate = %v, error = %v", valid, predicateErr)
			}
		})
	}
}

func TestInspectConventionalCommitReturnsStructuredValues(t *testing.T) {
	message := "feat(api)!: replace authentication WUKO-142\n\nClients must use OAuth."
	result, err := InspectConventionalCommit(message, map[string]any{"task_regex": `WUKO-[0-9]+`})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Message != message || result.CleanedMessage != message || result.Classification != "conventional" ||
		result.Type != "feat" || result.Scope != "api" || result.Subject != "replace authentication" || !result.Breaking ||
		result.Body != "Clients must use OAuth." || result.Task != "WUKO-142" {
		t.Fatalf("result = %#v", result)
	}
}

func TestTaskRegexAppliesToHeaderAndExemptions(t *testing.T) {
	options := map[string]any{"task_regex": `WUKO-[0-9]+`}
	for _, message := range []string{
		"fix: correct session handling WUKO-12\n\nA body follows.",
		"fixup! feat: add session handling WUKO-12",
		"Merge branch 'dev' into 'main' WUKO-12",
	} {
		result, err := InspectConventionalCommit(message, options)
		if err != nil {
			t.Fatalf("InspectConventionalCommit(%q): %v", message, err)
		}
		if result.Task != "WUKO-12" {
			t.Fatalf("task = %q", result.Task)
		}
	}
	if _, err := InspectConventionalCommit("fix: correct session handling", options); err == nil || !strings.Contains(err.Error(), "task") {
		t.Fatalf("missing task error = %v", err)
	}
}

func TestIsConventionalCommitReturnsOptionErrors(t *testing.T) {
	valid, err := IsConventionalCommit("feat: message", map[string]any{"task_regex": "("})
	if err == nil || valid || !strings.Contains(err.Error(), "compiling task_regex") {
		t.Fatalf("valid = %v, error = %v", valid, err)
	}
}

func TestConventionalCommitTemplateAndExprHelpers(t *testing.T) {
	config := `dict "type" "fix" "scope" "auth" "subject" "correct sessions" "task" "WUKO-12"`
	tmpl := template.Must(template.New("commit").Funcs(TemplateFuncs()).Parse(
		`{{ ` + config + ` | buildConventionalCommit }}` + "\n" +
			`{{ "fix(auth): correct sessions WUKO-12" | isConventionalCommit (dict "task_regex" "WUKO-[0-9]+") }}`,
	))
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, nil); err != nil {
		t.Fatal(err)
	}
	if rendered.String() != "fix(auth): correct sessions WUKO-12\ntrue" {
		t.Fatalf("template = %q", rendered.String())
	}

	built, err := Eval(`buildConventionalCommit({"type": "fix", "scope": "auth", "subject": "correct sessions", "task": "WUKO-12"})`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := Eval(`isConventionalCommit("fix(auth): correct sessions WUKO-12", {"task_regex": "WUKO-[0-9]+"})`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if built != "fix(auth): correct sessions WUKO-12" || valid != true {
		t.Fatalf("built = %#v, valid = %#v", built, valid)
	}
}

func TestConventionalCommitHelpersRejectInvalidArguments(t *testing.T) {
	for _, source := range []string{
		`buildConventionalCommit({"type": "feat"})`,
		`isConventionalCommit("feat: message", {"unknown": true})`,
	} {
		if _, err := Eval(source, map[string]any{}); err == nil {
			t.Fatalf("Eval(%q) succeeded", source)
		}
	}
}

func TestInspectConventionalCommitRejectsBlankScope(t *testing.T) {
	for _, options := range []map[string]any{
		nil,
		{"scopes": []any{"api"}, "force_scope": true},
	} {
		result, err := InspectConventionalCommit("feat(   ): message", options)
		if err == nil || !strings.Contains(err.Error(), "scope must not be blank") {
			t.Fatalf("options %v: error = %v, result = %#v", options, err, result)
		}
	}
}

func TestBuildConventionalCommitRejectsCommentBodyLines(t *testing.T) {
	config := map[string]any{"type": "feat", "subject": "message", "body": "First line.\n#123 refs"}
	if _, err := BuildConventionalCommit(config); err == nil || !strings.Contains(err.Error(), "strips comment lines") {
		t.Fatalf("error = %v", err)
	}
}

func TestInspectConventionalCommitDecodesRuneAfterMerge(t *testing.T) {
	// The character after "merge" must be decoded as a rune: reading a single byte of a
	// multi-byte letter would exempt the message from validation as a merge commit.
	result, err := InspectConventionalCommit("mergeשלום into main", nil)
	if err == nil || result.Classification == "merge" {
		t.Fatalf("error = %v, result = %#v", err, result)
	}
	merge, err := InspectConventionalCommit("Merge branch 'dev' into 'main'", nil)
	if err != nil || merge.Classification != "merge" {
		t.Fatalf("error = %v, result = %#v", err, merge)
	}
}

func TestTaskRegexRequiresATokenBoundary(t *testing.T) {
	options := map[string]any{"task_regex": `WUKO-[0-9]+`}
	if _, err := InspectConventionalCommit("fix: correct sessions notWUKO-12", options); err == nil ||
		!strings.Contains(err.Error(), "task_regex") {
		t.Fatalf("error = %v", err)
	}
	result, err := InspectConventionalCommit("fix: correct sessions WUKO-12", options)
	if err != nil || result.Task != "WUKO-12" || result.Subject != "correct sessions" {
		t.Fatalf("error = %v, result = %#v", err, result)
	}
}
