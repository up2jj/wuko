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
	"strings"

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
	Response          string                   `yaml:"response,omitempty"`
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

type Runner struct {
	config  Config
	hasBody bool
	hasJSON bool
}

type statusError struct{ status int }

func (err statusError) Error() string {
	return fmt.Sprintf("HTTP request returned status %d", err.status)
}

func (statusError) ObservationAvailable() bool { return true }

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
	runner := &Runner{config: config, hasBody: hasBody, hasJSON: hasJSON}
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
	response, err := client.Do(request)
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
	result = step.Result{Outputs: outputs}
	if !r.success(response.StatusCode) {
		return result, statusError{status: response.StatusCode}
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
	for _, status := range r.config.SuccessStatuses {
		if status < 100 || status > 599 {
			return fmt.Errorf("success status %d must be between 100 and 599", status)
		}
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
