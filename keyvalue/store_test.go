package keyvalue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestStoreOperationsAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.Get(t.Context(), "missing"); err != nil || found || value != nil {
		t.Fatalf("missing get = %#v, %v, %v", value, found, err)
	}
	want := map[string]any{"enabled": true, "items": []any{"a", float64(2)}}
	stored, err := store.Set(t.Context(), "theme", want)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, map[string]any{"enabled": true, "items": []any{"a", json.Number("2")}}) {
		t.Fatalf("stored = %#v", stored)
	}
	if _, err := store.Set(t.Context(), "nothing", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Key != "nothing" || entries[1].Key != "theme" {
		t.Fatalf("entries = %#v", entries)
	}
	if value, found, err := store.Get(t.Context(), "nothing"); err != nil || !found || value != nil {
		t.Fatalf("null get = %#v, %v, %v", value, found, err)
	}
	removed, deleted, err := store.Delete(t.Context(), "theme")
	if err != nil || !deleted || removed == nil {
		t.Fatalf("delete = %#v, %v, %v", removed, deleted, err)
	}
	if value, deleted, err := store.Delete(t.Context(), "theme"); err != nil || deleted || value != nil {
		t.Fatalf("second delete = %#v, %v, %v", value, deleted, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"nothing\": null\n}\n" {
		t.Fatalf("file = %q", data)
	}
	for _, name := range []string{"preferences.json", "preferences.lock"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", name, info.Mode().Perm())
		}
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("store directory mode = %o", info.Mode().Perm())
	}
}

func TestOpenAndValueValidation(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", `a\\b`, " space"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(t.TempDir(), name); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	store, err := Open(t.TempDir(), "valid-name_1.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), "", true); err == nil {
		t.Fatal("expected empty-key error")
	}
	if _, err := store.Set(t.Context(), "bad", func() {}); err == nil {
		t.Fatal("expected incompatible-value error")
	}
	if _, err := store.SetIfDifferent(t.Context(), "", true); err == nil {
		t.Fatal("expected SetIfDifferent empty-key error")
	}
	if _, err := store.SetIfDifferent(t.Context(), "bad", func() {}); err == nil {
		t.Fatal("expected SetIfDifferent incompatible-value error")
	}
}

func TestMalformedStoreIsPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("[1,2"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), "key", "value"); err == nil || !strings.Contains(err.Error(), "decoding store") {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[1,2" {
		t.Fatalf("file changed to %q", data)
	}
}

func TestConcurrentUpdatesDoNotLoseValues(t *testing.T) {
	store, err := Open(t.TempDir(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := store.Set(t.Context(), string(rune('a'+i)), i); err != nil {
				t.Errorf("Set() error = %v", err)
			}
		})
	}
	wg.Wait()
	entries, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("entries = %d, want 20", len(entries))
	}
}

func TestSetIfDifferentIsAtomicAndDoesNotRewriteUnchangedValue(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "changed")
	if err != nil {
		t.Fatal(err)
	}
	var changedCount int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			changed, err := store.SetIfDifferent(t.Context(), "fingerprint", "same")
			if err != nil {
				t.Errorf("SetIfDifferent() error = %v", err)
				return
			}
			if changed {
				mu.Lock()
				changedCount++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if changedCount != 1 {
		t.Fatalf("changed count = %d, want 1", changedCount)
	}
	path := filepath.Join(dir, "changed.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	changed, err := store.SetIfDifferent(t.Context(), "fingerprint", "same")
	if err != nil || changed {
		t.Fatalf("unchanged update = %v, %v", changed, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("store modification time changed: before=%s after=%s", before.ModTime(), after.ModTime())
	}
}

func TestConcurrentProcessUpdatesDoNotLoseValues(t *testing.T) {
	dir := t.TempDir()
	barrierPath := filepath.Join(dir, "barrier")
	barrier, err := os.OpenFile(barrierPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Close()
	if err := syscall.Flock(int(barrier.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 12)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStoreProcessHelper$")
		command.Env = append(os.Environ(),
			"WUKO_KV_HELPER_ROOT="+dir,
			"WUKO_KV_HELPER_KEY="+fmt.Sprintf("key-%02d", i),
			"WUKO_KV_HELPER_BARRIER="+barrierPath,
		)
		command.Stdout = &outputs[i]
		command.Stderr = &outputs[i]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = command
	}
	if err := syscall.Flock(int(barrier.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%s", err, outputs[i].String())
		}
	}
	store, err := Open(dir, "processes")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(commands) {
		t.Fatalf("entries = %d, want %d", len(entries), len(commands))
	}
}

func TestStoreProcessHelper(t *testing.T) {
	root := os.Getenv("WUKO_KV_HELPER_ROOT")
	if root == "" {
		return
	}
	barrier, err := os.Open(os.Getenv("WUKO_KV_HELPER_BARRIER"))
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Close()
	if err := syscall.Flock(int(barrier.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(barrier.Fd()), syscall.LOCK_UN)
	store, err := Open(root, "processes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(t.Context(), os.Getenv("WUKO_KV_HELPER_KEY"), true); err != nil {
		t.Fatal(err)
	}
}

func TestLockAcquisitionHonorsContext(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "busy")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "busy.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, _, err := store.Get(ctx, "key"); err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("error = %v", err)
	}
}

func TestCanceledContextDoesNotReadStore(t *testing.T) {
	store, err := Open(t.TempDir(), "canceled")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := store.Get(ctx, "key"); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error = %v", err)
	}
}

func TestReadsLeaveANeverWrittenStoreUntouched(t *testing.T) {
	root := filepath.Join(t.TempDir(), "values")
	store, err := Open(root, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.Get(t.Context(), "theme"); err != nil || found || value != nil {
		t.Fatalf("get = %#v, %v, %v", value, found, err)
	}
	entries, err := store.List(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("list = %#v, %v", entries, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("reading created the values root: %v", err)
	}
}

func TestReadsDoNotCreateALockFileBesideAnExistingStore(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "preferences.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.Get(t.Context(), "theme")
	if err != nil || !found || value != "dark" {
		t.Fatalf("get = %#v, %v, %v", value, found, err)
	}
	if _, err := os.Stat(filepath.Join(root, "preferences.lock")); !os.IsNotExist(err) {
		t.Fatalf("reading created a lock file: %v", err)
	}
}

func TestConcurrentUpdatesOfOneKeyAccumulate(t *testing.T) {
	store, err := Open(t.TempDir(), "counter")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 16
	var group sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _, err := store.Update(t.Context(), "runs", func(current any, found bool) (any, error) {
				if !found {
					return 1, nil
				}
				count, err := current.(json.Number).Int64()
				if err != nil {
					return nil, err
				}
				return count + 1, nil
			})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	value, found, err := store.Get(t.Context(), "runs")
	if err != nil || !found {
		t.Fatalf("get = %#v, %v, %v", value, found, err)
	}
	if value.(json.Number).String() != fmt.Sprint(writers) {
		t.Fatalf("counter = %v, want %d", value, writers)
	}
}

func TestUpdateReportsWhetherTheValueChanged(t *testing.T) {
	store, err := Open(t.TempDir(), "settings")
	if err != nil {
		t.Fatal(err)
	}
	keep := func(current any, _ bool) (any, error) { return "dark", nil }
	value, changed, err := store.Update(t.Context(), "theme", keep)
	if err != nil || !changed || value != "dark" {
		t.Fatalf("first update = %#v, %v, %v", value, changed, err)
	}
	value, changed, err = store.Update(t.Context(), "theme", keep)
	if err != nil || changed || value != "dark" {
		t.Fatalf("repeated update = %#v, %v, %v", value, changed, err)
	}
	if _, _, err := store.Update(t.Context(), "theme", func(any, bool) (any, error) {
		return nil, fmt.Errorf("boom")
	}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failing update error = %v", err)
	}
	if value, _, _ := store.Get(t.Context(), "theme"); value != "dark" {
		t.Fatalf("failed update wrote %#v", value)
	}
}

func TestOpenWorkflowScopedRefusesReservedStores(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"changed", "picker", "CHANGED", "Picker"} {
		if _, err := OpenWorkflowScoped(dir, dir, Local, name); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("OpenWorkflowScoped(%q) error = %v, want a reserved-name error", name, err)
		}
		if _, err := Open(dir, name); err != nil {
			t.Errorf("Open(%q) error = %v, want the owner to keep access", name, err)
		}
	}
	if _, err := OpenWorkflowScoped(dir, dir, Local, "changed-files"); err != nil {
		t.Errorf("OpenWorkflowScoped(\"changed-files\") error = %v", err)
	}
}

func TestClearRemovesEveryKey(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir, "preferences")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"theme", "layout"} {
		if _, err := store.Set(t.Context(), key, key); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.Clear(t.Context())
	if err != nil || removed != 2 {
		t.Fatalf("clear = %d, %v", removed, err)
	}
	entries, err := store.List(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "preferences.json"))
	if err != nil || string(data) != "{}\n" {
		t.Fatalf("store file = %q, %v", data, err)
	}
	if removed, err := store.Clear(t.Context()); err != nil || removed != 0 {
		t.Fatalf("repeated clear = %d, %v", removed, err)
	}
}

func TestEmptyStoreFileReadsAsAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "interrupted.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir, "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	if _, err := store.Set(t.Context(), "theme", "dark"); err != nil {
		t.Fatalf("set on an empty file: %v", err)
	}
	if value, found, err := store.Get(t.Context(), "theme"); err != nil || !found || value != "dark" {
		t.Fatalf("get = %#v, %v, %v", value, found, err)
	}
}
