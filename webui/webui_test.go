package webui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/form"
)

func TestRunCollectsFormStreamsProgressAndShowsResult(t *testing.T) {
	definition := &form.Definition{
		Title: "Deploy", Fields: []form.Field{{Variable: "target", Label: "Target", Type: form.TypeString, Required: true}},
		Result: form.ResultConfig{Success: form.ResultView{Title: "Done", Template: `<p>{{ .outputs.message }}</p>`}},
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	clientErr := make(chan error, 1)
	opener := func(target string) error {
		go func() {
			response, err := client.Get(target)
			if err != nil {
				clientErr <- err
				return
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
			if len(match) != 2 {
				clientErr <- fmt.Errorf("csrf token not found in %s", body)
				return
			}
			values := url.Values{"csrf": {match[1]}, "field_0": {"production"}}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target+"submit", strings.NewReader(values.Encode()))
			if err != nil {
				clientErr <- err
				return
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", "null")
			response, err = client.Do(request)
			if err != nil {
				clientErr <- err
				return
			}
			_ = response.Body.Close()
			for range 100 {
				response, err = client.Get(target)
				if err != nil {
					clientErr <- err
					return
				}
				body, _ = io.ReadAll(response.Body)
				_ = response.Body.Close()
				if strings.Contains(string(body), "deployed production") {
					clientErr <- nil
					return
				}
			}
			clientErr <- fmt.Errorf("result page was not served")
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := Run(ctx, definition, map[string]any{"target": "staging"}, nil,
		func(_ context.Context, submitted map[string]any, emit func(Progress)) Result {
			emit(Progress{Stage: "workflow", Kind: "step_started", Status: "running", StepID: "deploy"})
			return Result{Status: "succeeded", Outputs: map[string]any{"message": "deployed " + submitted["target"].(string)}}
		}, Options{OpenURL: opener, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-clientErr; err != nil {
		t.Fatal(err)
	}
}

func TestRunWaitsForLoadWorkerAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &form.Definition{Title: "Loading"}, nil, func(ctx context.Context, _ func(Progress)) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil, ctx.Err()
		}, nil, Options{NoOpen: true, Output: io.Discard})
	}()

	<-started
	cancel()
	<-canceled
	select {
	case err := <-done:
		t.Fatalf("Run() returned before its load worker stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	released = true
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestRunWaitsForExecutionWorkerAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	clientErr := make(chan error, 1)
	opener := func(target string) error {
		go func() {
			client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Get(target)
			if err != nil {
				clientErr <- err
				return
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
			if len(match) != 2 {
				clientErr <- fmt.Errorf("csrf token not found in %s", body)
				return
			}
			values := url.Values{"csrf": {match[1]}}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target+"submit", strings.NewReader(values.Encode()))
			if err != nil {
				clientErr <- err
				return
			}
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err = client.Do(request)
			if err == nil {
				_ = response.Body.Close()
			}
			clientErr <- err
		}()
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &form.Definition{Title: "Run"}, nil, nil, func(ctx context.Context, _ map[string]any, _ func(Progress)) Result {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return Result{Err: ctx.Err()}
		}, Options{OpenURL: opener, Output: io.Discard})
	}()

	<-started
	if err := <-clientErr; err != nil {
		t.Fatal(err)
	}
	cancel()
	<-canceled
	select {
	case err := <-done:
		t.Fatalf("Run() returned before its execution worker stopped: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	released = true
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}
