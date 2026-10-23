package keyvalue

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenScopedSeparatesLocalAndGlobalValues(t *testing.T) {
	localDir, globalDir := t.TempDir(), t.TempDir()
	local, err := OpenScoped(localDir, globalDir, Local, "settings")
	if err != nil {
		t.Fatal(err)
	}
	global, err := OpenScoped(localDir, globalDir, Global, "settings")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Set(t.Context(), "theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if _, err := global.Set(t.Context(), "editor", "vim"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := global.Get(t.Context(), "theme"); err != nil || found || value != nil {
		t.Fatalf("global theme = %#v, %v, %v; want absent", value, found, err)
	}
	if value, found, err := local.Get(t.Context(), "editor"); err != nil || found || value != nil {
		t.Fatalf("local editor = %#v, %v, %v; want absent", value, found, err)
	}
	if _, err := os.Stat(filepath.Join(localDir, "settings.json")); err != nil {
		t.Fatalf("local store file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(globalDir, "settings.json")); err != nil {
		t.Fatalf("global store file: %v", err)
	}
}

func TestOpenScopedSeparatesNamesWithinScope(t *testing.T) {
	localDir := t.TempDir()
	first, err := OpenScoped(localDir, "", Local, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenScoped(localDir, "", Local, "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Set(t.Context(), "key", "one"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := second.Get(t.Context(), "key"); err != nil || found {
		t.Fatalf("second store sees first store key: found=%v err=%v", found, err)
	}
}

func TestOpenScopedReopenSeesPersistedValues(t *testing.T) {
	for _, scope := range []string{Local, Global} {
		t.Run(scope, func(t *testing.T) {
			localDir, globalDir := t.TempDir(), t.TempDir()
			store, err := OpenScoped(localDir, globalDir, scope, "state")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Set(t.Context(), "count", 3); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenScoped(localDir, globalDir, scope, "state")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := reopened.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := []Entry{{Key: "count", Value: mustNormalizeScoped(t, 3)}}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("entries = %#v, want %#v", entries, want)
			}
		})
	}
}

func mustNormalizeScoped(t *testing.T, value any) any {
	t.Helper()
	normalized, err := Normalize(value)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestOpenScopedRejectsInvalidInputs(t *testing.T) {
	localDir, globalDir := t.TempDir(), t.TempDir()
	tests := []struct {
		name      string
		localDir  string
		globalDir string
		scope     string
		store     string
		want      string
	}{
		{name: "unknown scope", localDir: localDir, globalDir: globalDir, scope: "project", store: "settings", want: `scope must be "local" or "global"`},
		{name: "empty scope", localDir: localDir, globalDir: globalDir, scope: "", store: "settings", want: "scope must be"},
		{name: "scope is case sensitive", localDir: localDir, globalDir: globalDir, scope: "LOCAL", store: "settings", want: "scope must be"},
		{name: "local unavailable", globalDir: globalDir, scope: Local, store: "settings", want: "local key-value storage is unavailable"},
		{name: "global unavailable", localDir: localDir, scope: Global, store: "settings", want: "global key-value storage is unavailable"},
		{name: "traversal name", localDir: localDir, globalDir: globalDir, scope: Local, store: "../escape", want: "invalid store name"},
		{name: "dot name", localDir: localDir, globalDir: globalDir, scope: Global, store: "..", want: "invalid store name"},
		{name: "empty name", localDir: localDir, globalDir: globalDir, scope: Local, store: "", want: "invalid store name"},
		{name: "separator in name", localDir: localDir, globalDir: globalDir, scope: Local, store: "a/b", want: "invalid store name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenScoped(test.localDir, test.globalDir, test.scope, test.store)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OpenScoped() = %#v, %v; want error containing %q", store, err, test.want)
			}
		})
	}
}

func TestOpenScopedAllowsReservedNamesUnlikeWorkflowScoped(t *testing.T) {
	localDir := t.TempDir()
	if _, err := OpenScoped(localDir, "", Local, "once"); err != nil {
		t.Fatalf("OpenScoped() reserved name error = %v", err)
	}
	if _, err := OpenWorkflowScoped(localDir, "", Local, "Once"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("OpenWorkflowScoped() error = %v, want reserved", err)
	}
}
