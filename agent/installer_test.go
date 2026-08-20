package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	testingfst "testing/fstest"
)

func TestDiscoverOnlyReturnsAgentsAvailableOnPath(t *testing.T) {
	lookup := func(command string) (string, error) {
		if command == "claude" {
			return "/usr/local/bin/claude", nil
		}
		return "", errors.New("not found")
	}

	discovered := Discover("/home/test", lookup)
	if len(discovered) != 1 {
		t.Fatalf("discovered %d agents, want 1", len(discovered))
	}
	if discovered[0].Name != "claude" || discovered[0].Executable != "/usr/local/bin/claude" {
		t.Fatalf("discovered = %#v", discovered[0])
	}
	if want := filepath.Join("/home/test", ".claude", "skills"); discovered[0].SkillDirectory != want {
		t.Fatalf("skill directory = %q, want %q", discovered[0].SkillDirectory, want)
	}
}

func TestInstallCopiesAndReplacesSkills(t *testing.T) {
	source := testingfst.MapFS{
		"wuko-test/SKILL.md":           &testingfst.MapFile{Data: []byte("version 1")},
		"wuko-test/agents/openai.yaml": &testingfst.MapFile{Data: []byte("metadata")},
		"not-a-skill/README.md":        &testingfst.MapFile{Data: []byte("ignored")},
	}
	destination := t.TempDir()

	installed, err := Install(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0] != "wuko-test" {
		t.Fatalf("installed = %#v", installed)
	}

	skillPath := filepath.Join(destination, "wuko-test", "SKILL.md")
	metadataPath := filepath.Join(destination, "wuko-test", "agents", "openai.yaml")
	assertFileContents(t, skillPath, "version 1")
	assertFileContents(t, metadataPath, "metadata")

	source["wuko-test/SKILL.md"] = &testingfst.MapFile{Data: []byte("version 2")}
	if _, err := Install(source, destination); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, skillPath, "version 2")
}

func TestInstallRejectsEmptySource(t *testing.T) {
	_, err := Install(testingfst.MapFS{}, t.TempDir())
	if err == nil {
		t.Fatal("expected empty source error")
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
