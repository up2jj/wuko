// Package http implements structured HTTP workflow requests.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/up2jj/wuko/step"
)

const maxResponseSize = 10 << 20

type Config struct {
	URL             string            `yaml:"url"`
	Method          string            `yaml:"method,omitempty"`
	Headers         map[string]string `yaml:"headers,omitempty"`
	Query           map[string]string `yaml:"query,omitempty"`
	Body            string            `yaml:"body,omitempty"`
	JSON            any               `yaml:"json,omitempty"`
	Response        string            `yaml:"response,omitempty"`
	SuccessStatuses []int             `yaml:"success_statuses,omitempty"`
}

type Runner struct {
	config  Config
	hasBody bool
	hasJSON bool
	client  *http.Client
}

func Register(registry *step.Registry) error { return registry.Register("http", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasBody := raw["body"]
	_, hasJSON := raw["json"]
	if hasBody && hasJSON {
		return nil, fmt.Errorf("body and json are mutually exclusive")
	}
	if config.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if config.Method == "" {
		config.Method = http.MethodGet
	}
	if config.Response == "" {
		config.Response = "text"
	}
	runner := &Runner{config: config, hasBody: hasBody, hasJSON: hasJSON, client: newClient()}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func newClient() *http.Client {
	return &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if err := validateURL(request.URL); err != nil {
			return fmt.Errorf("redirect: %w", err)
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) == 0 {
			return nil
		}
		previous := via[len(via)-1].URL
		if previous.Scheme == "https" && request.URL.Scheme != "https" {
			return fmt.Errorf("redirect from https to http is not allowed")
		}
		if !strings.EqualFold(previous.Host, request.URL.Host) {
			return fmt.Errorf("redirect to a different origin is not allowed")
		}
		return nil
	}}
}

func (r *Runner) Validate(_ context.Context, _ step.Request) error { return r.validate(false) }

func (r *Runner) Run(ctx context.Context, _ step.Request) (step.Result, error) {
	if err := r.validate(true); err != nil {
		return step.Result{}, err
	}
	parsed, err := url.Parse(r.config.URL)
	if err != nil {
		return step.Result{}, fmt.Errorf("parsing url: %w", err)
	}
	query := parsed.Query()
	for key, value := range r.config.Query {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()

	var body io.Reader
	if r.hasJSON {
		data, err := json.Marshal(r.config.JSON)
		if err != nil {
			return step.Result{}, fmt.Errorf("encoding json request body: %w", err)
		}
		body = bytes.NewReader(data)
	} else if r.hasBody {
		body = strings.NewReader(r.config.Body)
	}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(r.config.Method), parsed.String(), body)
	if err != nil {
		return step.Result{}, fmt.Errorf("creating request: %w", err)
	}
	for key, value := range r.config.Headers {
		request.Header.Set(key, value)
	}
	if r.hasJSON && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return step.Result{}, fmt.Errorf("performing request: %w", err)
	}
	defer response.Body.Close()
	if response.ContentLength > maxResponseSize {
		return step.Result{}, fmt.Errorf("response body exceeds %d bytes", maxResponseSize)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return step.Result{}, fmt.Errorf("reading response body: %w", err)
	}
	if len(data) > maxResponseSize {
		return step.Result{}, fmt.Errorf("response body exceeds %d bytes", maxResponseSize)
	}
	value := any(string(data))
	if r.config.Response == "json" {
		value, err = decodeJSON(data)
		if err != nil {
			return step.Result{}, fmt.Errorf("decoding JSON response: %w", err)
		}
	}
	outputs := map[string]any{
		"status":  response.StatusCode,
		"headers": responseHeaders(response.Header),
		"body":    string(data),
		"value":   value,
	}
	result := step.Result{Outputs: outputs}
	if !r.success(response.StatusCode) {
		return result, fmt.Errorf("HTTP request returned status %d", response.StatusCode)
	}
	return result, nil
}

func (r *Runner) validate(resolved bool) error {
	if r.config.Method == "" {
		return fmt.Errorf("method must not be empty")
	}
	if r.config.Response != "text" && r.config.Response != "json" {
		if resolved || !templated(r.config.Response) {
			return fmt.Errorf("response must be text or json")
		}
	}
	for _, status := range r.config.SuccessStatuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("success status %d must be between 100 and 599", status)
		}
	}
	if !resolved && templated(r.config.URL) {
		return nil
	}
	parsed, err := url.Parse(r.config.URL)
	if err != nil {
		return fmt.Errorf("parsing url: %w", err)
	}
	return validateURL(parsed)
}

func validateURL(value *url.URL) error {
	if value.Scheme != "http" && value.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https")
	}
	if value.Host == "" {
		return fmt.Errorf("url must include a host")
	}
	if value.User != nil {
		return fmt.Errorf("url must not contain user information")
	}
	return nil
}

func (r *Runner) success(status int) bool {
	if len(r.config.SuccessStatuses) == 0 {
		return status >= 200 && status <= 299
	}
	return slices.Contains(r.config.SuccessStatuses, status)
}

func responseHeaders(header http.Header) map[string]any {
	result := make(map[string]any, len(header))
	for key, values := range header {
		items := make([]any, len(values))
		for i, value := range values {
			items[i] = value
		}
		result[key] = items
	}
	return result
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("response contains multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func templated(value string) bool { return strings.Contains(value, "{{") }
