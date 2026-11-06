package shim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInlineShimRoundTripsContent(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"version: 1\nname: check\nsteps: []",
		"version: 1\nname: check\nsteps: []\n",
		"version: 1\r\nname: check\r\nsteps: []\r\n",
		"#wuko-inline-workflow\n# wuko-shim: {}\n'quotes' $HOME `echo expansion` $(echo expansion)\n\n",
		strings.Repeat("long line", 10000),
	} {
		t.Run(content[:min(30, len(content))], func(t *testing.T) {
			t.Parallel()
			spec := Spec{File: "/work/check.yaml", Inline: true, Content: content,
				MissingMessage: "missing\n#wuko-inline-workflow\ninstall"}
			data, err := Render("check", spec)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(spec, got); diff != "" {
				t.Fatalf("spec (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInlineShimRejectsInvalidContentAndSources(t *testing.T) {
	t.Parallel()
	for _, spec := range []Spec{
		{File: "/work/check.yaml", Inline: true},
		{Workflow: "check", Inline: true, Content: "content"},
		{Project: "/work", Inline: true, Content: "content"},
		{Locator: "https://example.test/check.yaml", Inline: true, Content: "content"},
		{File: "/work/check.yaml", Content: "content"},
	} {
		if _, err := Render("check", spec); err == nil {
			t.Fatalf("Render(%+v) succeeded", spec)
		}
	}
	for _, payload := range []string{"", "#wuko-inline-workflow\nuncommented YAML\n"} {
		data := "#!/bin/sh\n# wuko-shim: {\"file\":\"/work/check.yaml\",\"inline\":true}\n" + payload
		if _, err := Parse([]byte(data)); !errors.Is(err, errInvalidMetadata) {
			t.Fatalf("Parse = %v, want invalid metadata", err)
		}
	}
}

func TestInlineShimToleratesEditorNormalization(t *testing.T) {
	t.Parallel()
	content := "version: 1\n\nname: check\n"
	data, err := Render("check", Spec{File: "/work/check.yaml", Inline: true, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), " \n") {
		t.Fatalf("rendered trailing whitespace:\n%s", data)
	}
	// Editors add final newlines and strip trailing spaces from hand-written blocks.
	edited := strings.ReplaceAll(string(data)+"\n\n", "# \n", "#\n")
	got, ok, err := InlineWorkflow([]byte(edited))
	if err != nil || !ok || string(got) != content {
		t.Fatalf("InlineWorkflow = %q, %v, %v", got, ok, err)
	}
}

func TestListReportsInvalidShimsAndKeepsOthers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Install(dir, "good", Spec{Workflow: "check"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad"), []byte("#!/bin/sh\n# wuko-shim: {\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shims, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(shims) != 2 || shims[0].Name != "bad" || !errors.Is(shims[0].Err, errInvalidMetadata) || shims[1].Err != nil {
		t.Fatalf("List = %+v", shims)
	}
}

func TestInstallWithoutForceNeverReplacesAppearingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "check")
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulates a file created between Install's check and its write.
	if err := writeExecutable(path, []byte("shim"), false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("writeExecutable = %v, want ErrExist", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "mine" {
		t.Fatalf("file replaced with %q", data)
	}
}
