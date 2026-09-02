package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

const (
	defaultHTTPEvery   = 5 * time.Second
	defaultHTTPTimeout = 10 * time.Second
	maxHTTPBody        = 10 << 20
)

// HTTPBuilder creates polling HTTP sources. Client may be supplied by tests or embedders.
type HTTPBuilder struct {
	Client *http.Client
}

func (HTTPBuilder) Type() string { return "http" }

type httpConfig struct {
	Every   string      `yaml:"every,omitempty"`
	Timeout string      `yaml:"timeout,omitempty"`
	Trigger string      `yaml:"trigger,omitempty"`
	Request httpRequest `yaml:"request"`
}

type httpRequest struct {
	URL      string            `yaml:"url"`
	Method   string            `yaml:"method,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty"`
	Body     string            `yaml:"body,omitempty"`
	Response string            `yaml:"response,omitempty"`
}

type normalizedHTTPConfig struct {
	every   time.Duration
	timeout time.Duration
	trigger string
	request httpRequest
}

func (builder HTTPBuilder) Validate(raw map[string]any) error {
	_, err := builder.normalize(raw, false)
	return err
}

func (builder HTTPBuilder) Open(ctx context.Context, request OpenRequest) (Source, error) {
	config, err := builder.normalize(request.Config, true)
	if err != nil {
		return nil, err
	}
	client, closeIdle := builder.client()
	source := &httpSource{config: config, client: client, closeIdle: closeIdle}
	initial, err := source.poll(ctx)
	if err != nil {
		closeIdle()
		return nil, err
	}
	source.initial = cloneMap(initial)
	source.last = httpFingerprint(initial)
	return source, nil
}

func (HTTPBuilder) normalize(raw map[string]any, resolved bool) (normalizedHTTPConfig, error) {
	declared := httpConfig{Every: defaultHTTPEvery.String(), Timeout: defaultHTTPTimeout.String(), Trigger: "change"}
	if err := decodeConfig(raw, &declared); err != nil {
		return normalizedHTTPConfig{}, err
	}
	every := defaultHTTPEvery
	if resolved || !templated(declared.Every) {
		var err error
		every, err = time.ParseDuration(declared.Every)
		if err != nil || every <= 0 {
			return normalizedHTTPConfig{}, fmt.Errorf("every must be a positive duration")
		}
	}
	timeout := defaultHTTPTimeout
	if resolved || !templated(declared.Timeout) {
		var err error
		timeout, err = time.ParseDuration(declared.Timeout)
		if err != nil || timeout <= 0 {
			return normalizedHTTPConfig{}, fmt.Errorf("timeout must be a positive duration")
		}
	}
	if (resolved || !templated(declared.Trigger)) && declared.Trigger != "change" && declared.Trigger != "always" {
		return normalizedHTTPConfig{}, fmt.Errorf("trigger must be change or always")
	}
	if declared.Request.URL == "" {
		return normalizedHTTPConfig{}, fmt.Errorf("request url is required")
	}
	if resolved || !templated(declared.Request.URL) {
		parsed, err := url.Parse(declared.Request.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return normalizedHTTPConfig{}, fmt.Errorf("request url must be an absolute http or https URL")
		}
	}
	if declared.Request.Method == "" {
		declared.Request.Method = http.MethodGet
	}
	declared.Request.Method = strings.ToUpper(declared.Request.Method)
	if declared.Request.Response == "" {
		declared.Request.Response = "text"
	}
	if (resolved || !templated(declared.Request.Response)) && declared.Request.Response != "text" && declared.Request.Response != "json" {
		return normalizedHTTPConfig{}, fmt.Errorf("request response must be text or json")
	}
	return normalizedHTTPConfig{every: every, timeout: timeout, trigger: declared.Trigger, request: declared.Request}, nil
}

func templated(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "}}")
}

func (builder HTTPBuilder) client() (*http.Client, func()) {
	if builder.Client != nil {
		return builder.Client, func() {}
	}
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := transport.Clone()
		client := &http.Client{Transport: cloned, CheckRedirect: safeObserveRedirect}
		return client, cloned.CloseIdleConnections
	}
	return &http.Client{Transport: http.DefaultTransport, CheckRedirect: safeObserveRedirect}, func() {}
}

func safeObserveRedirect(request *http.Request, via []*http.Request) error {
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
}

type httpSource struct {
	config    normalizedHTTPConfig
	client    *http.Client
	closeIdle func()
	initial   map[string]any
	last      map[string]any
}

func (source *httpSource) Initial() any { return cloneMap(source.initial) }

func (source *httpSource) Next(ctx context.Context) (any, error) {
	for {
		timer := time.NewTimer(source.config.every)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		observation, err := source.poll(ctx)
		if err != nil {
			return nil, err
		}
		fingerprint := httpFingerprint(observation)
		changed := !reflect.DeepEqual(source.last, fingerprint)
		source.last = fingerprint
		if source.config.trigger == "always" || changed {
			return observation, nil
		}
	}
}

func (source *httpSource) poll(ctx context.Context) (map[string]any, error) {
	pollCtx, cancel := context.WithTimeout(ctx, source.config.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(pollCtx, source.config.request.Method, source.config.request.URL, bytes.NewBufferString(source.config.request.Body))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP observation request: %w", err)
	}
	for key, value := range source.config.request.Headers {
		request.Header.Set(key, value)
	}
	response, err := source.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("performing HTTP observation request: %w", err)
	}
	defer response.Body.Close()
	if response.ContentLength > maxHTTPBody {
		return nil, fmt.Errorf("HTTP observation body exceeds %d bytes", maxHTTPBody)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPBody+1))
	if err != nil {
		return nil, fmt.Errorf("reading HTTP observation body: %w", err)
	}
	if len(data) > maxHTTPBody {
		return nil, fmt.Errorf("HTTP observation body exceeds %d bytes", maxHTTPBody)
	}
	body := string(data)
	value := any(body)
	if source.config.request.Response == "json" {
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, fmt.Errorf("decoding HTTP observation JSON: %w", err)
		}
	}
	headers := make(map[string]any, len(response.Header))
	for key, values := range response.Header {
		items := make([]any, len(values))
		for index, item := range values {
			items[index] = item
		}
		headers[key] = items
	}
	result := map[string]any{"status": response.StatusCode, "headers": headers, "body": body, "value": value}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result["error"] = fmt.Sprintf("HTTP request returned status %d", response.StatusCode)
	}
	return result, nil
}

func (*httpSource) NewBatch() Batch { return &latestBatch{root: "http"} }

func (source *httpSource) Metadata() map[string]any {
	return map[string]any{"every": source.config.every.String(), "timeout": source.config.timeout.String(), "trigger": source.config.trigger}
}

func (source *httpSource) Close() error {
	source.closeIdle()
	return nil
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	default:
		return value
	}
}

// httpFingerprint decides what "changed" means: the status and the bytes the endpoint returned.
// The decoded value stays out because body already answers the question. value is json.Unmarshal
// of body, or body itself for a text response, so keeping it would deep-copy and deep-compare the
// whole parsed tree on every poll to reach a verdict body has already reached.
func httpFingerprint(observation map[string]any) map[string]any {
	fingerprint := make(map[string]any, 3)
	for _, key := range []string{"status", "body", "error"} {
		if value, ok := observation[key]; ok {
			fingerprint[key] = cloneValue(value)
		}
	}
	return fingerprint
}
