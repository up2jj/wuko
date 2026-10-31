package reference

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/workflow"
)

func TestSectionIgnoresHeadingsInFencedExamples(t *testing.T) {
	t.Parallel()
	source := []byte("# Manual\n\n## `demo`\n\nAn example.\n\n```yaml\n# Not a heading\n## Neither is this\nvalue: 1\n```\n\n### Details\n\nMore.\n\n## Next\n\nExcluded.\n")
	got, ok := section(source, "demo")
	want := "An example.\n\n```yaml\n# Not a heading\n## Neither is this\nvalue: 1\n```\n\n### Details\n\nMore."
	if !ok || got != want {
		t.Fatalf("section (-want +got):\n%s", cmp.Diff(want, got))
	}
	if _, ok := section(source, "missing"); ok {
		t.Fatal("found missing section")
	}
}

func TestFieldsFlattenInlineConfigAndPreserveCustomGrammar(t *testing.T) {
	t.Parallel()
	type base struct {
		Command   string `yaml:"command"`
		ExitCodes []int  `yaml:"allowed_exit_codes"`
	}
	type config struct {
		Base      base               `yaml:",inline"`
		ExitCodes any                `yaml:"allowed_exit_codes"`
		Timeout   *workflow.Duration `yaml:"timeout"`
		Nested    []struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"nested"`
		Ignored string `yaml:"-"`
	}
	got := fields(reflect.TypeFor[config](), nil)
	want := []Field{{Name: "command", Type: "string"}, {Name: "allowed_exit_codes", Type: "any"}, {Name: "timeout", Type: "custom YAML (see documentation)"}, {Name: "nested", Type: "array", Items: &Field{Name: "[]", Type: "object", Fields: []Field{{Name: "enabled", Type: "boolean"}}}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("fields (-want +got):\n%s", diff)
	}
}

func TestCatalogUsesOnlyEmbeddedContentAndExplicitContracts(t *testing.T) {
	t.Parallel()
	definitions := map[string]step.Registration{"print": {Config: struct {
		Message string `yaml:"message"`
		Level   string `yaml:"level"`
	}{}, Outputs: step.ClosedOutputs(), Builder: func(map[string]any) (step.Runner, error) { t.Fatal("reference called a step builder"); return nil, nil }}}
	c, err := New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.Step("print")
	if err != nil {
		t.Fatal(err)
	}
	if item.Fields[0].Required == nil || !*item.Fields[0].Required || item.Fields[1].Default != "info" || len(item.Rules) == 0 {
		t.Fatalf("missing reviewed semantics: %#v", item)
	}
	if item.Outputs.Kind != "object" || item.Outputs.Open {
		t.Fatalf("lost closed output contract: %#v", item.Outputs)
	}
	if len(item.Examples) == 0 || !strings.HasPrefix(item.Examples[0].Command, "wuko reference example print ") {
		t.Fatal("missing example navigation")
	}
	if len(c.Search("there-is-no-such-reference-entry")) != 0 {
		t.Fatal("unexpected search result")
	}
	if len(c.Search("PRINT")) == 0 {
		t.Fatal("search should ignore case")
	}
	var first, second bytes.Buffer
	if err := WriteJSON(&first, "step", item); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&second, "step", item); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first.String(), second.String()); diff != "" {
		t.Fatalf("unstable JSON (-want +got):\n%s", diff)
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Kind          string          `json:"kind"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Kind != "step" {
		t.Fatalf("unexpected envelope: %s", first.String())
	}
	if bytes.Contains(first.Bytes(), []byte(`"workflow":`)) {
		t.Fatal("step metadata should index examples, not dump their workflows")
	}
	for _, related := range item.Related {
		if _, err := c.Topic(related); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMetadataRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	for _, spec := range []semantics{{Required: []string{"missing"}}, {Defaults: map[string]any{"missing": false}}, {Choices: map[string][]string{"missing": {"one"}}}} {
		if _, err := annotate([]Field{{Name: "present", Type: "string"}}, spec); err == nil {
			t.Fatal("accepted metadata naming an unknown field")
		}
	}
}
