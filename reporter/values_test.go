package reporter

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/engine"
)

func TestParseValueInclude(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		names []string
		want  ValueInclude
		err   string
	}{
		{names: nil, want: ValueInclude{}},
		{names: []string{"outputs"}, want: ValueInclude{StepOutputs: true}},
		{names: []string{"vars", " outputs"}, want: ValueInclude{StepOutputs: true, Vars: true}},
		{names: []string{"env"}, err: `unknown report value "env"`},
	} {
		got, err := ParseValueInclude(test.names)
		if test.err != "" {
			if err == nil || !strings.Contains(err.Error(), test.err) {
				t.Fatalf("ParseValueInclude(%q) error = %v", test.names, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("ParseValueInclude(%q) = %#v, %v", test.names, got, err)
		}
	}
}

func TestReportWithValuesSelectsAndLimitsValues(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", ValueStringLimit)
	huge := make([]any, 0, ValueLimit/8)
	for range ValueLimit / 8 {
		huge = append(huge, "12345678")
	}
	outcome := Outcome{Status: engine.StatusFailed, Values: &engine.RunValues{
		Steps: map[string]any{"read": map[string]any{"body": long}, "list": map[string]any{"items": huge}},
		Vars:  map[string]any{"name": "wuko"},
	}}

	report := reportWithValues(outcome, ValueInclude{StepOutputs: true})
	if report.Vars != nil || !report.ValuesTruncated {
		t.Fatalf("report vars=%#v truncated=%v", report.Vars, report.ValuesTruncated)
	}
	body := report.StepOutputs["read"].(map[string]any)["body"].(string)
	if !strings.HasSuffix(body, "…<truncated>") || len(body) > ValueStringLimit+len("…<truncated>") || !strings.HasPrefix(long, strings.TrimSuffix(body, "…<truncated>")) {
		t.Fatalf("body was not truncated on a rune boundary: %d bytes", len(body))
	}
	if list, ok := report.StepOutputs["list"].(string); !ok || !strings.HasPrefix(list, "<truncated: ") {
		t.Fatalf("list = %#v", report.StepOutputs["list"])
	}
	if outcome.Values.Steps["read"].(map[string]any)["body"] != long {
		t.Fatal("limiting modified the shared outcome values")
	}

	report = reportWithValues(outcome, ValueInclude{Vars: true})
	if diff := cmp.Diff(map[string]any{"name": "wuko"}, report.Vars); diff != "" || report.StepOutputs != nil || report.ValuesTruncated {
		t.Fatalf("vars (-want +got):\n%s", diff)
	}
	if report := reportWithValues(outcome, ValueInclude{}); report.StepOutputs != nil || report.Vars != nil {
		t.Fatalf("unrequested values reported: %#v", report)
	}
}
