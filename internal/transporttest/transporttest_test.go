package transporttest

import (
	"io"
	"net/http"
	"testing"
)

func TestClientAnswersThroughRoundTripFunc(t *testing.T) {
	t.Parallel()
	client := Client(func(request *http.Request) (*http.Response, error) {
		return Response(http.StatusTeapot, "path "+request.URL.Path), nil
	})
	response, err := client.Get("http://example.test/brew")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusTeapot || response.Status != "418 I'm a teapot" || string(body) != "path /brew" {
		t.Fatalf("response = %d %q %q", response.StatusCode, response.Status, body)
	}
}
