package scriptsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/up2jj/wuko/helper"
	"github.com/up2jj/wuko/step"
)

func TestBashClientValuesAndErrors(t *testing.T) {
	b := openBridge(t, step.Request{
		Vars:   map[string]any{"items": []any{nil, "last"}, "nil": nil},
		Secret: func(ref string) (string, error) { return "resolved:" + ref, nil },
	})
	descriptor, err := b.writeDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"output", "text", "--help\n'\"\\$()"}, ""},
		{[]string{"output-json", "large", "9007199254740993"}, ""},
		{[]string{"output-json", "null", "null"}, ""},
		{[]string{"set-var-json", "flag", "false"}, ""},
		{[]string{"set-var", "text", ""}, ""},
		{[]string{"get", "vars.items.1"}, "last\n"},
		{[]string{"get-json", "vars.items.1"}, "\"last\"\n"},
		{[]string{"get", "vars.nil"}, "null\n"},
		{[]string{"get-json", "args.count"}, "2\n"},
		{[]string{"helper", "upper", "hello"}, "HELLO\n"},
		{[]string{"helper-json", "repeat", "[3,\"x\"]"}, "\"xxx\"\n"},
		{[]string{"helper-json", "list", "[true,null,3]"}, "[true,null,3]\n"},
		{[]string{"secret", "test:token"}, "resolved:test:token\n"},
		{[]string{"call", "output", `{"name":"generic","value":true}`}, "null\n"},
	} {
		t.Run(strings.Join(test.args[:min(2, len(test.args))], "/"), func(t *testing.T) {
			var out bytes.Buffer
			if err := RunBashClient(t.Context(), descriptor, test.args, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != test.want {
				t.Fatalf("stdout = %q, want %q", out.String(), test.want)
			}
		})
	}
	result := b.Result()
	if result.Outputs["large"] != int64(9007199254740993) || result.Outputs["text"] != "--help\n'\"\\$()" || result.Variables["flag"] != false || result.Variables["text"] != "" || result.Outputs["generic"] != true {
		t.Fatalf("result = %#v", result)
	}
	for _, args := range [][]string{
		nil, {"unknown"}, {"output", "x"}, {"output-json", "x", "true false"},
		{"output-json", "x", "NaN"}, {"output", "x", string([]byte{0xff})},
		{"helper"}, {"helper-json", "list", "null"}, {"helper-json", "list", "{}"},
		{"helper", "missing"}, {"get", "vars.missing"}, {"get", "vars.items.-1"}, {"get", "vars.items.no"},
		{"get", "vars.nil.x"}, {"get", "a", "b"}, {"secret"}, {"render"}, {"step", "shell", "[]"},
		{"run-workflow", "test", "null"}, {"call", "context", "{}", "extra"},
	} {
		var out bytes.Buffer
		if err := RunBashClient(t.Context(), descriptor, args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("args %#v: error=%v, stdout=%q", args, err, out.String())
		}
	}
	if err := RunBashClient(t.Context(), "", []string{"get"}, io.Discard); err == nil {
		t.Fatal("missing descriptor succeeded")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RunBashClient(t.Context(), descriptor, []string{"get"}, io.Discard); err == nil {
		t.Fatal("expired descriptor succeeded")
	}
}

func TestBashClientCancellationAndAuthentication(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	b := openBridge(t, step.Request{Helpers: helper.Set{
		"wait": func(ctx context.Context, _ []any) (any, error) {
			close(started)
			defer close(finished)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}})
	descriptor, err := b.writeDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunBashClient(ctx, descriptor, []string{"helper", "wait"}, io.Discard) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	<-finished
	bad := b.descriptor
	bad.Token = "wrong"
	data, _ := json.Marshal(bad)
	if err := os.WriteFile(descriptor, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunBashClient(t.Context(), descriptor, []string{"helper", "wait"}, io.Discard); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("authentication error = %v", err)
	}
}

func TestBashInjectionRejectsUnsupportedEnvironment(t *testing.T) {
	b := openBridge(t, step.Request{})
	for _, environment := range []map[string]string{{"POSIXLY_CORRECT": ""}, {"SHELLOPTS": "braceexpand:posix:hashall"}} {
		if err := b.InjectBash(environment, "/unused/wuko"); err == nil {
			t.Fatalf("accepted POSIX environment: %#v", environment)
		}
	}
	if err := b.InjectBash(map[string]string{}, "relative/wuko"); err == nil {
		t.Fatal("accepted a relative executable path")
	}
}

func TestContextPathSelectsRawValues(t *testing.T) {
	data := json.RawMessage(`{"args":{"big":9007199254740993,"list":["a",{"b":true}],"none":null},"vars":{"s":"x<y"}}`)
	for _, test := range []struct {
		path, want, err string
	}{
		{path: "args.big", want: `9007199254740993`},
		{path: "args.list.1.b", want: `true`},
		{path: "args.list", want: `["a",{"b":true}]`},
		{path: "vars.s", want: `"x<y"`},
		{path: "args.missing", err: "does not exist"},
		{path: "args.list.2", err: "invalid array index"},
		{path: "args.list.-1", err: "invalid array index"},
		{path: "args.none.x", err: "cannot traverse"},
		{path: "args.big.x", err: "cannot traverse"},
	} {
		t.Run(test.path, func(t *testing.T) {
			got, err := contextPath(data, test.path)
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("error = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil || string(got) != test.want {
				t.Fatalf("contextPath = %s, %v; want %s", got, err, test.want)
			}
		})
	}
}
