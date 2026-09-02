package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestCookieJarPersistsAcrossRunsAndStatusErrors(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			if cookie, err := request.Cookie("configured"); err != nil || cookie.Value != "first" {
				t.Errorf("configured cookie = %v, %v", cookie, err)
			}
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "saved", Path: "/", HttpOnly: true})
			writer.WriteHeader(http.StatusTeapot)
			return
		}
		if cookie, err := request.Cookie("session"); err != nil || cookie.Value != "saved" {
			t.Errorf("persisted cookie = %v, %v", cookie, err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runDir := t.TempDir()
	first, err := New(map[string]any{
		"url":     server.URL,
		"cookies": map[string]any{"values": map[string]any{"configured": "first"}, "jar": "state/cookies.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(t.Context(), step.Request{RunDir: runDir}); err == nil || !strings.Contains(err.Error(), "status 418") {
		t.Fatalf("first error = %v", err)
	} else {
		var observation step.ObservationError
		if !errors.As(err, &observation) || !observation.ObservationAvailable() {
			t.Fatalf("status error does not expose an observation: %T %v", err, err)
		}
	}
	second, err := New(map[string]any{"url": server.URL, "cookies": map[string]any{"jar": "state/cookies.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Run(t.Context(), step.Request{RunDir: runDir}); err != nil {
		t.Fatal(err)
	}
	jarPath := filepath.Join(runDir, "state", "cookies.txt")
	data, err := os.ReadFile(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# Netscape HTTP Cookie File") || !strings.Contains(text, "#HttpOnly_") || !strings.Contains(text, "\tsession\tsaved") {
		t.Fatalf("jar = %q", text)
	}
	info, err := os.Stat(jarPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("jar mode = %04o", info.Mode().Perm())
	}
}

func TestPersistentJarLoadsDeletesAndWritesDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	initial := "# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\tzeta\tlast\n#HttpOnly_.example.test\tTRUE\t/\tTRUE\t0\talpha\tfirst\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	jar, closeJar, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://api.example.test/")
	if got := cookieValue(jar.Cookies(target), "alpha"); got != "first" {
		t.Fatalf("alpha = %q", got)
	}
	jar.SetCookies(target, []*http.Cookie{{Name: "zeta", Value: "", Path: "/", Domain: "example.test", MaxAge: -1}})
	jar.SetCookies(target, []*http.Cookie{{Name: "middle", Value: "value", Path: "/", Domain: "example.test", MaxAge: 60}})
	if err := closeJar(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "\tzeta\t") || !strings.Contains(text, "\tmiddle\tvalue") {
		t.Fatalf("jar = %q", text)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 4 || strings.Compare(lines[2], lines[3]) >= 0 {
		t.Fatalf("cookie lines are not sorted: %#v", lines)
	}
}

func TestPersistentJarSupportsConcurrentUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	jar, closeJar, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://example.test/")
	const workers = 32
	const updates = 25
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := range workers {
		group.Go(func() {
			<-start
			name := fmt.Sprintf("cookie%d", worker)
			for update := range updates {
				jar.SetCookies(target, []*http.Cookie{{Name: name, Value: fmt.Sprint(update), Path: "/"}})
				_ = jar.Cookies(target)
			}
		})
	}
	close(start)
	group.Wait()
	if err := closeJar(); err != nil {
		t.Fatal(err)
	}

	reloaded, closeReloaded, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for worker := range workers {
		name := fmt.Sprintf("cookie%d", worker)
		if got := cookieValue(reloaded.Cookies(target), name); got != fmt.Sprint(updates-1) {
			t.Errorf("%s = %q, want %d", name, got, updates-1)
		}
	}
	if err := closeReloaded(); err != nil {
		t.Fatal(err)
	}
}

func TestCookieJarRejectsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cookies.txt"), []byte("not-a-cookie\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := New(map[string]any{"url": "https://example.com", "cookies": map[string]any{"jar": "cookies.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: dir})
	if err == nil || !strings.Contains(err.Error(), "expected 7") {
		t.Fatalf("error = %v", err)
	}
}

func TestCookieJarLockPreservesUpdatesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	first, closeFirst, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://example.test/")
	first.SetCookies(target, []*http.Cookie{{Name: "first", Value: "one", Path: "/"}})

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := openPersistentJar(canceled, path); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock error = %v", err)
	}

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		second, closeSecond, err := openPersistentJar(t.Context(), path)
		if err == nil {
			second.SetCookies(target, []*http.Cookie{{Name: "second", Value: "two", Path: "/"}})
			err = closeSecond()
		}
		finished <- err
	}()
	<-started
	if err := closeFirst(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\tfirst\tone") || !strings.Contains(string(data), "\tsecond\ttwo") {
		t.Fatalf("jar = %q", data)
	}
}

func cookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func TestJarEntryValidation(t *testing.T) {
	tests := []string{
		"example.test\tMAYBE\t/\tFALSE\t0\tname\tvalue",
		"example.test\tFALSE\trelative\tFALSE\t0\tname\tvalue",
		"example.test\tFALSE\t/\tFALSE\tnow\tname\tvalue",
	}
	for index, line := range tests {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			if _, err := parseJarEntry(line); err == nil {
				t.Fatalf("parseJarEntry(%q) succeeded", line)
			}
		})
	}
}

func TestCookieJarCloseFailureKeepsStatusError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	runDir := t.TempDir()
	jarDir := filepath.Join(runDir, "state")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// Sealing the directory mid-request makes the jar write fail once the response
		// has already produced a retryable status.
		if err := os.Chmod(jarDir, 0o500); err != nil {
			t.Errorf("chmod: %v", err)
		}
		writer.Header().Set("Retry-After", "7")
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	t.Cleanup(func() { _ = os.Chmod(jarDir, 0o700) })
	runner, err := New(map[string]any{"url": server.URL, "cookies": map[string]any{"jar": "state/cookies.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), step.Request{RunDir: runDir})
	if err == nil || strings.Contains(err.Error(), "saving cookie jar") {
		t.Fatalf("error = %v", err)
	}
	var retryable interface {
		HTTPStatusCode() int
		HTTPRetryAfter() time.Duration
	}
	if !errors.As(err, &retryable) {
		t.Fatalf("error does not carry a status: %T %v", err, err)
	}
	if retryable.HTTPStatusCode() != http.StatusTooManyRequests || retryable.HTTPRetryAfter() != 7*time.Second {
		t.Fatalf("status = %d, retry after = %v", retryable.HTTPStatusCode(), retryable.HTTPRetryAfter())
	}
}

func TestPersistentJarBoundsCookieLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.txt")
	initial := "# Netscape HTTP Cookie File\n" +
		".example.test\tTRUE\t/\tFALSE\t0\tbloated\t" + strings.Repeat("x", maxJarLineBytes) + "\n" +
		".example.test\tTRUE\t/\tFALSE\t0\tsmall\tvalue\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	jar, closeJar, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://api.example.test/")
	if got := cookieValue(jar.Cookies(target), "small"); got != "value" {
		t.Fatalf("small = %q", got)
	}
	if got := cookieValue(jar.Cookies(target), "bloated"); got != "" {
		t.Fatalf("over-long line was loaded: %d bytes", len(got))
	}
	// An oversized cookie stays usable for this run, it just never reaches the file.
	huge := strings.Repeat("y", maxJarEntryBytes)
	jar.SetCookies(target, []*http.Cookie{{Name: "fresh", Value: huge, Path: "/"}})
	if got := cookieValue(jar.Cookies(target), "fresh"); got != huge {
		t.Fatalf("fresh cookie is not usable in memory: %d bytes", len(got))
	}
	if err := closeJar(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "bloated") || strings.Contains(text, "\tfresh\t") {
		t.Fatalf("oversized cookies were written: %d bytes", len(text))
	}
	if !strings.Contains(text, "\tsmall\tvalue") {
		t.Fatalf("jar = %q", text)
	}
	reopened, closeReopened, err := openPersistentJar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cookieValue(reopened.Cookies(target), "small"); got != "value" {
		t.Fatalf("small after reopen = %q", got)
	}
	if err := closeReopened(); err != nil {
		t.Fatal(err)
	}
}
