// Package transporttest fakes HTTP round trips, so tests of HTTP clients can answer requests
// in process without starting a server.
package transporttest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
)

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls roundTrip(request).
func (roundTrip RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

// Client returns a client that sends every request through roundTrip.
func Client(roundTrip RoundTripFunc) *http.Client {
	return &http.Client{Transport: roundTrip}
}

// Response returns a response with status and body and an empty header.
func Response[Body string | []byte](status int, body Body) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
	}
}
