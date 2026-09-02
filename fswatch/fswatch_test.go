package fswatch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fsnotify/fsnotify"
)

type recordingSource struct {
	added  []string
	events chan fsnotify.Event
	errors chan error
}

func newRecordingSource() *recordingSource {
	return &recordingSource{events: make(chan fsnotify.Event), errors: make(chan error)}
}

func (source *recordingSource) Add(name string) error {
	source.added = append(source.added, name)
	return nil
}
func (source *recordingSource) Close() error                        { return nil }
func (source *recordingSource) EventChannel() <-chan fsnotify.Event { return source.events }
func (source *recordingSource) ErrorChannel() <-chan error          { return source.errors }

func TestDescendableKeepsPatternsAliveAndPrunesUnreachableDirectories(t *testing.T) {
	root := "/repo"
	cases := []struct {
		patterns  []string
		directory string
		want      bool
	}{
		// A wildcard component refuses a hidden name, so ** cannot reach into .git.
		{[]string{"**/*.go"}, ".git", false},
		{[]string{"**/*.go"}, ".git/objects", false},
		{[]string{"**/*.go"}, "src", true},
		{[]string{"**/*.go"}, "src/deep/deeper", true},
		// A literal component may name a hidden directory, so this one must stay registered.
		{[]string{".github/**/*.yml"}, ".github", true},
		{[]string{".github/**/*.yml"}, ".github/workflows", true},
		{[]string{".github/**/*.yml"}, ".git", false},
		// A pattern that cannot match deeper prunes below its own depth.
		{[]string{"docs/*.md"}, "docs", true},
		{[]string{"docs/*.md"}, "docs/api", false},
		{[]string{"*.go"}, "src", false},
		// Any one pattern reaching the directory is enough to keep it.
		{[]string{"docs/*.md", "**/*.go"}, "docs/api", true},
		// The root itself is always registered, and nothing outside it ever is.
		{[]string{"docs/*.md"}, ".", true},
		{[]string{"**/*.go"}, "../outside", false},
	}
	for _, testCase := range cases {
		directory := filepath.Join(root, testCase.directory)
		got := descendable(patternComponents(testCase.patterns), root, directory)
		if got != testCase.want {
			t.Errorf("descendable(%v, %q) = %v, want %v", testCase.patterns, testCase.directory, got, testCase.want)
		}
	}
}

// Registration and matching must agree: a directory is watched exactly when a path under it
// could match, so no watch is spent on events matchingPath would discard.
func TestOpenRegistersOnlyReachableDirectories(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{".git/objects", ".github/workflows", "node_modules/pkg", "src/inner", "docs/api"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source := newRecordingSource()
	observer, err := Open(t.Context(), root, Config{Root: root, Patterns: []string{"**/*.go", ".github/**/*.yml", "docs/*.md"}, Events: EventNames()}, func() (Source, error) {
		return source, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	var relative []string
	for _, added := range source.added {
		path, err := filepath.Rel(root, added)
		if err != nil {
			t.Fatal(err)
		}
		relative = append(relative, filepath.ToSlash(path))
	}
	slices.Sort(relative)
	// node_modules and docs/api stay: **/*.go genuinely can match inside them. .git and its
	// subtree do not, because a wildcard component refuses a hidden name.
	want := []string{".", ".github", ".github/workflows", "docs", "docs/api", "node_modules", "node_modules/pkg", "src", "src/inner"}
	if !slices.Equal(relative, want) {
		t.Fatalf("registered %v, want %v", relative, want)
	}
}

// A pattern that cannot match below its own depth stops the walk there.
func TestOpenPrunesBelowPatternDepth(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"docs/api/v1", ".git/objects", "src"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source := newRecordingSource()
	observer, err := Open(t.Context(), root, Config{Root: root, Patterns: []string{"docs/*.md"}, Events: EventNames()}, func() (Source, error) {
		return source, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	var relative []string
	for _, added := range source.added {
		path, err := filepath.Rel(root, added)
		if err != nil {
			t.Fatal(err)
		}
		relative = append(relative, filepath.ToSlash(path))
	}
	slices.Sort(relative)
	if want := []string{".", "docs"}; !slices.Equal(relative, want) {
		t.Fatalf("registered %v, want %v", relative, want)
	}
}
