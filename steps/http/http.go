// Package http implements structured HTTP workflow requests.
package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/up2jj/wuko/step"
)

const maxResponseSize = 10 << 20

type Config struct {
	URL               string                   `yaml:"url"`
	Method            string                   `yaml:"method,omitempty"`
	Headers           map[string]string        `yaml:"headers,omitempty"`
	Query             map[string]string        `yaml:"query,omitempty"`
	Body              string                   `yaml:"body,omitempty"`
	JSON              any                      `yaml:"json,omitempty"`
	Form              map[string]string        `yaml:"form,omitempty"`
	Files             []FileConfig             `yaml:"files,omitempty"`
	Response          string                   `yaml:"response,omitempty"`
	Download          *DownloadConfig          `yaml:"download,omitempty"`
	SuccessStatuses   []int                    `yaml:"success_statuses,omitempty"`
	Auth              *AuthConfig              `yaml:"auth,omitempty"`
	Cookies           *CookiesConfig           `yaml:"cookies,omitempty"`
	Proxy             *ProxyConfig             `yaml:"proxy,omitempty"`
	ClientCertificate *ClientCertificateConfig `yaml:"client_certificate,omitempty"`
}

type AuthConfig struct {
	BearerToken string           `yaml:"bearer_token,omitempty"`
	Basic       *BasicAuthConfig `yaml:"basic,omitempty"`
}

type BasicAuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type CookiesConfig struct {
	Values map[string]string `yaml:"values,omitempty"`
	Jar    string            `yaml:"jar,omitempty"`
}

type ProxyConfig struct {
	URL string `yaml:"url"`
}

type ClientCertificateConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type FileConfig struct {
	Field string `yaml:"field"`
	Path  string `yaml:"path"`
}

type DownloadConfig struct {
	Path      string `yaml:"path"`
	Overwrite bool   `yaml:"overwrite,omitempty"`
}

type Runner struct {
	config   Config
	hasBody  bool
	hasJSON  bool
	hasForm  bool
	hasFiles bool
}

type statusError struct {
	method     string
	status     int
	retryAfter time.Duration
}

func (err statusError) Error() string {
	return fmt.Sprintf("HTTP request returned status %d", err.status)
}

func (statusError) ObservationAvailable() bool { return true }

func (err statusError) HTTPRequestMethod() string     { return err.method }
func (err statusError) HTTPStatusCode() int           { return err.status }
func (err statusError) HTTPRetryAfter() time.Duration { return err.retryAfter }

type requestError struct {
	method string
	err    error
}

func (err requestError) Error() string             { return err.err.Error() }
func (err requestError) Unwrap() error             { return err.err }
func (err requestError) HTTPRequestMethod() string { return err.method }
func (requestError) HTTPStatusCode() int           { return 0 }
func (requestError) HTTPRetryAfter() time.Duration { return 0 }

func Register(registry *step.Registry) error { return registry.Register("http", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	_, hasBody := raw["body"]
	_, hasJSON := raw["json"]
	_, hasForm := raw["form"]
	_, hasFiles := raw["files"]
	_, hasResponse := raw["response"]
	_, hasDownload := raw["download"]
	if (hasBody && hasJSON) || ((hasBody || hasJSON) && (hasForm || hasFiles)) {
		return nil, fmt.Errorf("body, json, and form/files request bodies are mutually exclusive")
	}
	if hasDownload && config.Download == nil {
		return nil, fmt.Errorf("download must configure a path")
	}
	if hasDownload && hasResponse {
		return nil, fmt.Errorf("download and response are mutually exclusive")
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
	runner := &Runner{
		config: config, hasBody: hasBody, hasJSON: hasJSON,
		hasForm: hasForm, hasFiles: hasFiles,
	}
	if err := runner.validate(false); err != nil {
		return nil, err
	}
	return runner, nil
}

func newClient(transport http.RoundTripper, jar http.CookieJar) *http.Client {
	return &http.Client{Transport: transport, Jar: jar, CheckRedirect: func(request *http.Request, via []*http.Request) error {
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

func (r *Runner) Run(ctx context.Context, execution step.Request) (result step.Result, resultErr error) {
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
	var download *downloadTarget
	if r.config.Download != nil {
		download, err = prepareDownload(ctx, execution.RunDir, *r.config.Download)
		if err != nil {
			return step.Result{}, err
		}
		defer func() {
			if err := download.Cleanup(); err != nil && resultErr == nil {
				result = step.Result{}
				resultErr = fmt.Errorf("cleaning up download: %w", err)
			}
		}()
	}

	transport, err := r.newTransport(execution.WorkflowDir)
	if err != nil {
		return step.Result{}, err
	}
	defer transport.CloseIdleConnections()
	jar, closeJar, err := r.openJar(ctx, execution.RunDir)
	if err != nil {
		return step.Result{}, err
	}
	defer func() {
		if err := closeJar(); err != nil {
			result = step.Result{}
			resultErr = fmt.Errorf("saving cookie jar: %w", err)
		}
	}()
	if r.config.Cookies != nil {
		cookies := make([]*http.Cookie, 0, len(r.config.Cookies.Values))
		for name, value := range r.config.Cookies.Values {
			cookies = append(cookies, &http.Cookie{Name: name, Value: value, Path: "/"})
		}
		jar.SetCookies(parsed, cookies)
	}
	client := newClient(transport, jar)

	var body io.Reader
	var multipartBody *multipartRequestBody
	var multipartReader io.ReadCloser
	if r.hasJSON {
		data, err := json.Marshal(r.config.JSON)
		if err != nil {
			return step.Result{}, fmt.Errorf("encoding json request body: %w", err)
		}
		body = bytes.NewReader(data)
	} else if r.hasBody {
		body = strings.NewReader(r.config.Body)
	} else if r.hasFiles {
		multipartBody, err = prepareMultipartBody(ctx, execution.WorkflowDir, r.config.Form, r.config.Files)
		if err != nil {
			return step.Result{}, err
		}
		multipartReader, err = multipartBody.Open()
		if err != nil {
			return step.Result{}, err
		}
		body = multipartReader
	} else if r.hasForm {
		values := make(url.Values, len(r.config.Form))
		for key, value := range r.config.Form {
			values.Set(key, value)
		}
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, strings.ToUpper(r.config.Method), parsed.String(), body)
	if err != nil {
		if multipartReader != nil {
			multipartReader.Close()
		}
		return step.Result{}, fmt.Errorf("creating request: %w", err)
	}
	if multipartBody != nil {
		request.ContentLength = multipartBody.ContentLength()
		request.GetBody = multipartBody.Open
	}
	for key, value := range r.config.Headers {
		request.Header.Set(key, value)
	}
	if r.config.Auth != nil {
		if r.config.Auth.BearerToken != "" {
			request.Header.Set("Authorization", "Bearer "+r.config.Auth.BearerToken)
		} else if r.config.Auth.Basic != nil {
			request.SetBasicAuth(r.config.Auth.Basic.Username, r.config.Auth.Basic.Password)
		}
	}
	if r.hasJSON && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if r.hasForm && !r.hasFiles && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if multipartBody != nil {
		request.Header.Set("Content-Type", multipartBody.ContentType())
	}
	if download == nil {
		applyConditionalHeaders(request.Header, execution.PreviousAttempt)
	}
	response, err := client.Do(request)
	if err != nil {
		return step.Result{}, fmt.Errorf("performing request: %w", requestError{method: request.Method, err: err})
	}
	defer response.Body.Close()
	if download != nil && r.success(response.StatusCode) {
		size, err := download.Write(ctx, response.Body)
		if err != nil {
			return step.Result{}, fmt.Errorf("downloading response: %w", err)
		}
		return step.Result{Outputs: map[string]any{
			"status": response.StatusCode, "headers": responseHeaders(response.Header),
			"path": download.Path(), "size": size,
		}}, nil
	}
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
	if response.StatusCode == http.StatusNotModified {
		if outputs, ok := revalidatedOutputs(execution.PreviousAttempt, response.Header); ok {
			return step.Result{Outputs: outputs}, nil
		}
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
	result = step.Result{Outputs: outputs}
	if !r.success(response.StatusCode) {
		return result, statusError{
			method: request.Method, status: response.StatusCode,
			retryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	return result, nil
}

func (r *Runner) openJar(ctx context.Context, runDir string) (http.CookieJar, func() error, error) {
	if r.config.Cookies == nil || r.config.Cookies.Jar == "" {
		jar, err := cookiejar.New(nil)
		return jar, func() error { return nil }, err
	}
	path, err := resolvePath(runDir, r.config.Cookies.Jar)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving cookie jar: %w", err)
	}
	return openPersistentJar(ctx, path)
}

func (r *Runner) newTransport(workflowDir string) (*http.Transport, error) {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default HTTP transport has unsupported type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	if r.config.Proxy != nil {
		proxyURL, err := url.Parse(r.config.Proxy.URL)
		if err != nil {
			return nil, fmt.Errorf("proxy url is invalid")
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	if r.config.ClientCertificate == nil {
		return transport, nil
	}
	certFile, err := resolvePath(workflowDir, r.config.ClientCertificate.CertFile)
	if err != nil {
		return nil, fmt.Errorf("resolving client certificate: %w", err)
	}
	keyFile, err := resolvePath(workflowDir, r.config.ClientCertificate.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("resolving client certificate key: %w", err)
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading client certificate: %w", err)
	}
	tlsConfig := &tls.Config{}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
	transport.TLSClientConfig = tlsConfig
	return transport, nil
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
	if r.config.Download != nil && (resolved || !templated(r.config.Download.Path)) && strings.TrimSpace(r.config.Download.Path) == "" {
		return fmt.Errorf("download path must not be empty")
	}
	for _, status := range r.config.SuccessStatuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("success status %d must be between 100 and 599", status)
		}
	}
	if err := r.validateRequestBody(resolved); err != nil {
		return err
	}
	if err := r.validateAuthentication(resolved); err != nil {
		return err
	}
	if err := r.validateCookies(resolved); err != nil {
		return err
	}
	if err := r.validateTransport(resolved); err != nil {
		return err
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

func (r *Runner) validateRequestBody(resolved bool) error {
	if r.hasFiles && len(r.config.Files) == 0 {
		return fmt.Errorf("files must contain at least one file")
	}
	if r.hasFiles && headerExists(r.config.Headers, "Content-Type") {
		return fmt.Errorf("files and Content-Type header are mutually exclusive")
	}
	if r.hasFiles {
		for field := range r.config.Form {
			if strings.ContainsAny(field, "\r\n") {
				return fmt.Errorf("form field %q must not contain newlines", field)
			}
		}
	}
	for i, file := range r.config.Files {
		if (resolved || !templated(file.Field)) && strings.TrimSpace(file.Field) == "" {
			return fmt.Errorf("files[%d] field must not be empty", i)
		}
		if (resolved || !templated(file.Path)) && strings.TrimSpace(file.Path) == "" {
			return fmt.Errorf("files[%d] path must not be empty", i)
		}
		if (resolved || !templated(file.Field)) && strings.ContainsAny(file.Field, "\r\n") {
			return fmt.Errorf("files[%d] field must not contain newlines", i)
		}
	}
	return nil
}

func (r *Runner) validateAuthentication(resolved bool) error {
	if r.config.Auth == nil {
		return nil
	}
	if headerExists(r.config.Headers, "Authorization") {
		return fmt.Errorf("auth and Authorization header are mutually exclusive")
	}
	if r.config.Auth.BearerToken != "" && r.config.Auth.Basic != nil {
		return fmt.Errorf("auth bearer_token and basic are mutually exclusive")
	}
	if r.config.Auth.BearerToken == "" && r.config.Auth.Basic == nil {
		return fmt.Errorf("auth must configure bearer_token or basic")
	}
	if r.config.Auth.BearerToken != "" {
		if (resolved || !templated(r.config.Auth.BearerToken)) && strings.TrimSpace(r.config.Auth.BearerToken) == "" {
			return fmt.Errorf("auth bearer_token must not be empty")
		}
		return nil
	}
	if resolved || !templated(r.config.Auth.Basic.Username) {
		if strings.TrimSpace(r.config.Auth.Basic.Username) == "" {
			return fmt.Errorf("auth basic username must not be empty")
		}
	}
	if resolved || !templated(r.config.Auth.Basic.Password) {
		if r.config.Auth.Basic.Password == "" {
			return fmt.Errorf("auth basic password must not be empty")
		}
	}
	return nil
}

func (r *Runner) validateCookies(resolved bool) error {
	if r.config.Cookies == nil {
		return nil
	}
	if headerExists(r.config.Headers, "Cookie") && (len(r.config.Cookies.Values) > 0 || r.config.Cookies.Jar != "") {
		return fmt.Errorf("cookies and Cookie header are mutually exclusive")
	}
	if r.config.Cookies.Jar != "" && (resolved || !templated(r.config.Cookies.Jar)) && strings.TrimSpace(r.config.Cookies.Jar) == "" {
		return fmt.Errorf("cookies jar must not be empty")
	}
	for name, value := range r.config.Cookies.Values {
		if err := (&http.Cookie{Name: name, Value: "value"}).Valid(); err != nil {
			return fmt.Errorf("invalid cookie %q: %w", name, err)
		}
		if !resolved && templated(value) {
			continue
		}
		if err := (&http.Cookie{Name: name, Value: value}).Valid(); err != nil {
			return fmt.Errorf("invalid cookie %q: %w", name, err)
		}
	}
	return nil
}

func (r *Runner) validateTransport(resolved bool) error {
	if r.config.Proxy != nil {
		if r.config.Proxy.URL == "" {
			return fmt.Errorf("proxy url is required")
		}
		if resolved || !templated(r.config.Proxy.URL) {
			parsed, err := url.Parse(r.config.Proxy.URL)
			if err != nil {
				return fmt.Errorf("proxy url is invalid")
			}
			if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("proxy url must use http or https and include a host")
			}
		}
	}
	if r.config.ClientCertificate != nil {
		certificate := r.config.ClientCertificate
		if certificate.CertFile == "" || certificate.KeyFile == "" {
			return fmt.Errorf("client_certificate cert_file and key_file are both required")
		}
		if (resolved || (!templated(certificate.CertFile) && !templated(certificate.KeyFile))) && (strings.TrimSpace(certificate.CertFile) == "" || strings.TrimSpace(certificate.KeyFile) == "") {
			return fmt.Errorf("client_certificate cert_file and key_file must not be empty")
		}
	}
	return nil
}

func headerExists(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func resolvePath(baseDir, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	resolved, err := filepath.Abs(filepath.Join(baseDir, path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
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

func applyConditionalHeaders(header http.Header, previous *step.Result) {
	if previous == nil || previous.Outputs == nil {
		return
	}
	previousHeaders, ok := previous.Outputs["headers"].(map[string]any)
	if !ok {
		return
	}
	if !requestHeaderExists(header, "If-None-Match") {
		if etag := outputHeader(previousHeaders, "ETag"); etag != "" {
			header.Set("If-None-Match", etag)
		}
	}
	if !requestHeaderExists(header, "If-Modified-Since") {
		if modified := outputHeader(previousHeaders, "Last-Modified"); modified != "" {
			header.Set("If-Modified-Since", modified)
		}
	}
}

func requestHeaderExists(header http.Header, name string) bool {
	for key := range header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func revalidatedOutputs(previous *step.Result, current http.Header) (map[string]any, bool) {
	if previous == nil || previous.Outputs == nil {
		return nil, false
	}
	body, hasBody := previous.Outputs["body"]
	value, hasValue := previous.Outputs["value"]
	previousHeaders, hasHeaders := previous.Outputs["headers"].(map[string]any)
	if !hasBody || !hasValue || !hasHeaders {
		return nil, false
	}
	headers := make(map[string]any, len(previousHeaders)+len(current))
	for key, values := range previousHeaders {
		headers[key] = values
	}
	for key, values := range responseHeaders(current) {
		for existing := range headers {
			if strings.EqualFold(existing, key) && existing != key {
				delete(headers, existing)
			}
		}
		headers[key] = values
	}
	return map[string]any{
		"status":  http.StatusNotModified,
		"headers": headers,
		"body":    body,
		"value":   value,
	}, true
}

func outputHeader(headers map[string]any, name string) string {
	for key, raw := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		values, ok := raw.([]any)
		if !ok || len(values) == 0 {
			return ""
		}
		value, _ := values[0].(string)
		return value
	}
	return ""
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	return date.Sub(now)
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
