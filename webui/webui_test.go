package webui

import (
	"context"
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
