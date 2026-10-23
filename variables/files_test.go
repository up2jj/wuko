package variables

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/up2jj/wuko/internal/testfs"
)

func TestLoadFilesMergesLeftToRightAndNormalizesKeys(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, "defaults.JSON"), `{
  "Name": "json",
  "Nested": {"Keep": true, "Old": "value"},
  "Items": ["one", "two"]
}`)
	testfs.Write(t, filepath.Join(root, "override.toml"), `name = "toml"
[nested]
new = "value"
`)

	values, err := LoadFiles(t.Context(), root, []string{"defaults.JSON", "override.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if values["name"] != "toml" {
		t.Fatalf("name = %#v", values["name"])
	}
	if diff := cmp.Diff(map[string]any{"new": "value"}, values["nested"]); diff != "" {
		t.Fatalf("nested mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]any{"one", "two"}, values["items"]); diff != "" {
		t.Fatalf("items mismatch (-want +got):\n%s", diff)
	}
	if _, exists := values["Name"]; exists {
		t.Fatalf("uppercase key was preserved: %#v", values)
	}
}

func TestLoadFilesPreservesNullAndEmptyObjects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, "values.json"), `{
  "Optional": null,
  "Empty": {},
  "Nested": {"Missing": null, "EmptyObject": {}}
}`)

	values, err := LoadFiles(t.Context(), root, []string{"values.json"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"optional": nil,
		"empty":    map[string]any{},
		"nested": map[string]any{
			"missing":     nil,
			"emptyobject": map[string]any{},
		},
	}
	if diff := cmp.Diff(want, values); diff != "" {
		t.Fatalf("values mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadFilesRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	testfs.Write(t, filepath.Join(root, "malformed.json"), `{"name":`)
	testfs.Write(t, filepath.Join(root, "array.json"), `["value"]`)
	testfs.Write(t, filepath.Join(root, "trailing.json"), `{"name":"value"} true`)
	testfs.Write(t, filepath.Join(root, "values.yaml"), `name: value`)

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty path", path: "", want: "must not be empty"},
		{name: "unsupported extension", path: "values.yaml", want: "unsupported variable file extension"},
		{name: "missing file", path: "missing.json", want: "reading variable file"},
		{name: "malformed JSON", path: "malformed.json", want: "reading variable file"},
		{name: "array root", path: "array.json", want: "reading variable file"},
		{name: "trailing JSON", path: "trailing.json", want: "reading variable file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadFiles(t.Context(), root, []string{test.path})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadFilesHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	values, err := LoadFiles(ctx, t.TempDir(), []string{"missing.json"})
	if err != context.Canceled || values != nil {
		t.Fatalf("values = %#v, error = %v", values, err)
	}
}

func TestValidatePathDefersTemplatedExtension(t *testing.T) {
	t.Parallel()
	if err := ValidatePath(`{{ .vars.file }}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePath(`{{ .vars.name }}.yaml`); err == nil {
		t.Fatal("static unsupported extension was accepted")
	}
}
