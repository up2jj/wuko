package http

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestAuthenticationHelpers(t *testing.T) {
	tests := []struct {
		name string
		auth map[string]any
		want string
	}{
		{name: "bearer", auth: map[string]any{"bearer_token": "secret-token"}, want: "Bearer secret-token"},
		{name: "basic", auth: map[string]any{"basic": map[string]any{"username": "alice", "password": "secret"}}, want: "Basic YWxpY2U6c2VjcmV0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("Authorization"); got != tt.want {
					t.Errorf("Authorization = %q, want %q", got, tt.want)
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			runner, err := New(map[string]any{"url": server.URL, "auth": tt.auth})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(t.Context(), step.Request{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfiguredProxyOverridesDefaultSelection(t *testing.T) {
	proxySaw := make(chan *http.Request, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxySaw <- request.Clone(request.Context())
		fmt.Fprint(writer, "proxied")
	}))
	defer proxy.Close()
	parsedProxy, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsedProxy.User = url.UserPassword("proxy-user", "proxy-password")
	runner, err := New(map[string]any{
		"url":   "http://origin.invalid/resource",
		"proxy": map[string]any{"url": parsedProxy.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), step.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outputs["body"] != "proxied" {
		t.Fatalf("outputs = %#v", result.Outputs)
	}
	request := <-proxySaw
	if request.URL.Host != "origin.invalid" || request.Header.Get("Proxy-Authorization") != "Basic cHJveHktdXNlcjpwcm94eS1wYXNzd29yZA==" {
		t.Fatalf("proxy request URL = %s, headers = %#v", request.URL, request.Header)
	}
}

func TestTransportRetainsEnvironmentProxyFallback(t *testing.T) {
	built, err := New(map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := built.(*Runner).newTransport("")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.Proxy == nil {
		t.Fatal("default environment proxy selection was removed")
	}
}

func TestClientCertificateLoadsRelativeToWorkflow(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if len(request.TLS.PeerCertificates) == 0 {
			t.Error("client certificate was not presented")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	server.StartTLS()
	defer server.Close()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client-key.pem")
	writeTLSKeyPair(t, server.TLS.Certificates[0], certPath, keyPath)
	raw := map[string]any{
		"url":                server.URL,
		"client_certificate": map[string]any{"cert_file": "client.pem", "key_file": "client-key.pem"},
	}
	built, err := New(raw)
	if err != nil {
		t.Fatal(err)
	}
	runner := built.(*Runner)
	transport, err := runner.newTransport(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = pool
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := newClient(transport, jar).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestClientCertificateRejectsInvalidPair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "client.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "client-key.pem"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := New(map[string]any{
		"url":                "https://example.com",
		"client_certificate": map[string]any{"cert_file": "client.pem", "key_file": "client-key.pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.(*Runner).newTransport(dir); err == nil || !strings.Contains(err.Error(), "loading client certificate") {
		t.Fatalf("error = %v", err)
	}
}

func writeTLSKeyPair(t *testing.T, certificate tls.Certificate, certPath, keyPath string) {
	t.Helper()
	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range certificate.Certificate {
		if err := pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if err := certFile.Close(); err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticationTransportValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "empty auth", raw: map[string]any{"auth": map[string]any{}}, want: "bearer_token or basic"},
		{name: "both auth modes", raw: map[string]any{"auth": map[string]any{"bearer_token": "token", "basic": map[string]any{"username": "a", "password": "b"}}}, want: "mutually exclusive"},
		{name: "authorization conflict", raw: map[string]any{"headers": map[string]any{"authorization": "custom"}, "auth": map[string]any{"bearer_token": "token"}}, want: "mutually exclusive"},
		{name: "empty username", raw: map[string]any{"auth": map[string]any{"basic": map[string]any{"username": "", "password": "b"}}}, want: "username"},
		{name: "empty password", raw: map[string]any{"auth": map[string]any{"basic": map[string]any{"username": "a", "password": ""}}}, want: "password"},
		{name: "cookie conflict", raw: map[string]any{"headers": map[string]any{"COOKIE": "a=b"}, "cookies": map[string]any{"values": map[string]any{"a": "b"}}}, want: "mutually exclusive"},
		{name: "invalid cookie", raw: map[string]any{"cookies": map[string]any{"values": map[string]any{"bad name": "value"}}}, want: "invalid cookie"},
		{name: "unsupported proxy", raw: map[string]any{"proxy": map[string]any{"url": "socks5://proxy.example"}}, want: "http or https"},
		{name: "missing proxy url", raw: map[string]any{"proxy": map[string]any{}}, want: "proxy url"},
		{name: "missing certificate key", raw: map[string]any{"client_certificate": map[string]any{"cert_file": "client.pem"}}, want: "both required"},
		{name: "unknown nested field", raw: map[string]any{"auth": map[string]any{"token": "secret"}}, want: "field token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.raw["url"] = "https://example.com"
			_, err := New(tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestClientCertificateIsLoadedOncePerRunner(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.pem")
	keyPath := filepath.Join(dir, "client-key.pem")
	writeTLSKeyPair(t, server.TLS.Certificates[0], certPath, keyPath)
	built, err := New(map[string]any{
		"url":                server.URL,
		"client_certificate": map[string]any{"cert_file": "client.pem", "key_file": "client-key.pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := built.(*Runner)
	first, err := runner.transportFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Deleting the pair proves the next attempt reuses the parsed certificate
	// instead of reading it from disk again.
	if err := os.Remove(certPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	second, err := runner.transportFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("transport was rebuilt between attempts")
	}
	if _, err := runner.transportFor(t.TempDir()); err == nil || !strings.Contains(err.Error(), "loading client certificate") {
		t.Fatalf("another workflow directory reused the transport: %v", err)
	}
}
