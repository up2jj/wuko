package shell

import (
	"strings"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestBashSDKRequiresCompatibleShell(t *testing.T) {
	for _, raw := range []map[string]any{
		{"command": "sh"}, {"script": "true", "shell": "/bin/sh"},
		{"command": "bash", "args": []any{"-ic", "true"}},
		{"command": "bash", "args": []any{"-pc", "true"}},
		{"command": "bash", "args": []any{"--posix", "-c", "true"}},
		{"command": "bash", "args": []any{"-o", "posix", "-c", "true"}},
		{"command": "bash", "args": []any{"-euo", "posix", "-c", "true"}},
		{"command": "bash", "args": []any{"-euo", "pipefail", "-ic", "true"}},
		{"command": "bash", "args": []any{"-c", "-i", "true"}},
		{"command": "bash", "args": []any{"-c", "-o", "posix", "true"}},
		{"command": "bash", "args": []any{"-rc", "true"}},
		{"command": "bash", "args": []any{"--restricted", "-c", "true"}},
		{"command": "bash", "tty": true},
		{"command": "bash", "args": []any{"-e"}, "tty": true},
		{"argv": map[string]any{"expr": "['sh', '-c', 'true']"}},
	} {
		raw["sdk"] = "bash"
		runner, err := New(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = runner.(*sdkRunner).command(step.Request{})
		if err == nil || !strings.Contains(err.Error(), "Bash") && !strings.Contains(err.Error(), "POSIX") {
			t.Fatalf("config=%#v error=%v", raw, err)
		}
	}
	for _, args := range [][]string{{"-c", "printf -- -i"}, {"--noprofile", "-euc", "true"}, {"-o", "pipefail", "-c", "true"}, {"-O", "extglob", "script.bash"}} {
		r := &Runner{config: Config{SDK: "bash"}}
		if _, _, err := r.sdkCommand("/bin/bash", args); err != nil {
			t.Fatalf("args=%#v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"-c", "true"}, {"script.bash"}, {"-e", "--", "script.bash"}} {
		r := &Runner{config: Config{SDK: "bash", TTY: true}}
		if _, _, err := r.sdkCommand("bash", args); err != nil {
			t.Fatalf("tty args=%#v: %v", args, err)
		}
	}
}
