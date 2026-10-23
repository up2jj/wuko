package scriptsdk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
)

func TestBashTransportUsesBuiltinsAndPreservesBytes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	paths := []string{bash}
	if bash != "/bin/bash" {
		if _, err := exec.LookPath("/bin/bash"); err == nil {
			paths = append(paths, "/bin/bash")
		}
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw := "  zażółć\\c\\x41%$(not-executed)\n\r\t\x007\x000\n\n"
			bridge := openBridge(t, step.Request{Vars: map[string]any{"raw": raw, "large": strings.Repeat("x", 128<<10)}})
			environment := map[string]string{"PATH": ""}
			// Any accidental executable-client call must fail, even in $(...) or
			// an inherited Bash process. Test the native transport independently.
			if err := bridge.InjectBash(environment, "/nonexistent/wuko-client-must-not-run"); err != nil {
				t.Fatal(err)
			}
			source := `set -euo pipefail
exec 9>caller-fd
trap 'printf trap-ran >caller-trap' EXIT
IFS= read -r line
[[ $line == untouched ]]
wuko_output unicode 'zażółć 🙂'
wuko_output quoted $'quote\'"\\\n$(not-executed)'
wuko_output_json typed '[9007199254740993,true,null]'
wuko_set_var_json published false
[[ $(wuko_helper upper hello) == HELLO ]]
[[ $(wuko_helper_json repeat '[3,"x"]') == '"xxx"' ]]
wuko_output large "$(wuko_get vars.large)"
if wuko_output_json bad 'invalid-json' >unexpected 2>expected; then exit 90; fi
[[ ! -s unexpected && -s expected ]]
"$BASH" -c 'wuko_output inherited yes'
for ((i=0; i<12; i++)); do
  (wuko_output "background_$i" "$i") &
done
wait
wuko_get vars.raw
printf preserved >&9
[[ $(<caller-fd) == preserved && ! -e caller-trap ]]
`
			command := exec.CommandContext(t.Context(), path, "--noprofile", "--norc", "-c", source)
			command.Dir = t.TempDir()
			command.Stdin = strings.NewReader("untouched\n")
			for key, value := range environment {
				command.Env = append(command.Env, key+"="+value)
			}
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			if stdout.String() != raw+"\n" || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			result := bridge.Result()
			if result.Outputs["unicode"] != "zażółć 🙂" || result.Outputs["quoted"] != "quote'\"\\\n$(not-executed)" || result.Outputs["inherited"] != "yes" || result.Variables["published"] != false || len(result.Outputs["large"].(string)) != 128<<10 {
				t.Fatalf("incorrect staged values: unicode=%v, quoted=%v, inherited=%v, published=%v", result.Outputs["unicode"], result.Outputs["quoted"], result.Outputs["inherited"], result.Variables["published"])
			}
			for i := range 12 {
				if result.Outputs[fmt.Sprintf("background_%d", i)] != fmt.Sprint(i) {
					t.Errorf("background call %d lost or crossed its result", i)
				}
			}
		})
	}
}

func TestBashTransportAuthenticatesLimitsAndCloses(t *testing.T) {
	bridge := openBridge(t, step.Request{})
	port, err := bridge.openBashListener()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, test := range []struct {
		name, token, body, want string
		status                  int
	}{
		{"unauthorized", "wrong", "output\x00x\x00hello\x00", "unauthorized", http.StatusUnauthorized},
		{"malformed", bridge.descriptor.Token, "output", "invalid Bash SDK request", http.StatusOK},
		{"too large", bridge.descriptor.Token, strings.Repeat("x", maxMessageBytes+1) + "\x00", "limit 10 MiB", http.StatusOK},
		{"invalid UTF8", bridge.descriptor.Token, "output\x00x\x00\xff\x00", "UTF-8", http.StatusOK},
		{"escaped JSON too large", bridge.descriptor.Token, "output\x00x\x00" + strings.Repeat("\n", maxMessageBytes/2) + "\x00", "exceeds 10 MiB", http.StatusOK},
		{"invalid arguments", bridge.descriptor.Token, "output-json\x00x\x00invalid\x00", "valid JSON", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:"+port+"/v1/bash", strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+test.token)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != test.status || !bytes.Contains(data, []byte(test.want)) {
				t.Fatalf("status=%d body=%q error=%v", response.StatusCode, data, err)
			}
		})
	}
	if len(bridge.Result().Outputs) != 0 {
		t.Fatal("rejected request staged an output")
	}
	if err := bridge.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:"+port, time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatal("Bash listener survived bridge close")
	}
}

func TestBashTransportKeepsCredentialsOutOfXtrace(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	bridge := openBridge(t, step.Request{})
	environment := map[string]string{"PATH": ""}
	if err := bridge.InjectBash(environment, "/nonexistent/wuko-client-must-not-run"); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), bash, "-xv", "-c", `set -euo pipefail; wuko_output result hello; [[ $- == *x* ]]; printf tracing-restored >&2`)
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("traced script: %v", err)
	}
	if bytes.Contains(output, []byte(bridge.descriptor.Token)) {
		t.Fatal("xtrace disclosed the bridge credential")
	}
	if !bytes.Contains(output, []byte("tracing-restored")) || bridge.Result().Outputs["result"] != "hello" {
		t.Fatal("tracing or SDK output was not preserved")
	}
}

func TestBashTransportDisconnectCancelsHostCall(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	bridge := openBridge(t, step.Request{Helpers: helper.Set{
		"wait": func(ctx context.Context, _ []any) (any, error) {
			close(started)
			defer close(finished)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}})
	port, err := bridge.openBashListener()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:"+port, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	body := "helper\x00wait\x00"
	if _, err := fmt.Fprintf(connection, "POST /v1/bash HTTP/1.0\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n%s", bridge.descriptor.Token, len(body), body); err != nil {
		t.Fatal(err)
	}
	<-started
	_ = connection.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not cancel the host call")
	}
}

func TestBashTransportReportsQueuedCancellationInBashFormat(t *testing.T) {
	bridge := openBridge(t, step.Request{})
	for range cap(bridge.slots) {
		bridge.slots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(bridge.slots) {
			<-bridge.slots
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/bash", strings.NewReader("output\x00x\x00y\x00"))
	request.Header.Set("Authorization", "Bearer "+bridge.descriptor.Token)
	recorder := httptest.NewRecorder()
	bridge.handle(recorder, request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.HasPrefix(body, "1\x00\x00SDK canceled: ") || !strings.HasSuffix(body, "\n\x00") {
		t.Fatalf("status=%d body=%q", recorder.Code, body)
	}
	if recorder.Header().Get("Content-Length") != fmt.Sprint(len(body)) {
		t.Fatalf("Content-Length=%s body length=%d", recorder.Header().Get("Content-Length"), len(body))
	}
}
