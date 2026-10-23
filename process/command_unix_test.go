//go:build darwin || linux

package process

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/up2jj/wuko/internal/testfs"
	"github.com/up2jj/wuko/ptyinteract"
)

func TestRunUsesWorkflowPATH(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const tool = "wuko-test-path-tool"
	first, second, skipped := filepath.Join(root, "first"), filepath.Join(root, "second"), filepath.Join(root, "skipped")
	for _, directory := range []string{first, second, skipped} {
		testfs.MkdirAll(t, directory, 0o755)
	}
	testfs.WriteFile(t, filepath.Join(first, tool), []byte("#!/bin/sh\nprintf first"), 0o755)
	testfs.WriteFile(t, filepath.Join(second, tool), []byte("#!/bin/sh\nprintf second"), 0o755)
	testfs.WriteFile(t, filepath.Join(skipped, tool), []byte("not executable"), 0o644)
	testfs.WriteFile(t, filepath.Join(root, tool), []byte("#!/bin/sh\nprintf current"), 0o755)
	for _, tty := range []bool{false, true} {
		mode := "pipes"
		if tty {
			mode = "tty"
		}
		for _, test := range []struct {
			name    string
			command string
			path    string
			args    []string
			unset   bool
			want    string
			missing bool
		}{
			{name: "absolute entries", path: first, want: "first"},
			{name: "search order", path: second + ":" + first, want: "second"},
			{name: "skip non-executable", path: skipped + ":" + first, want: "first"},
			{name: "relative entry", path: "first", want: "first"},
			{name: "empty entry", path: ":" + first, want: "current"},
			{name: "explicit relative command", command: "./first/" + tool, want: "first"},
			{name: "explicit absolute command", command: filepath.Join(second, tool), want: "second"},
			{name: "empty PATH excludes host", command: "sh", missing: true},
			{name: "nonempty PATH excludes host", command: "sh", path: first, missing: true},
			{name: "unset PATH retains host lookup", command: "sh", args: []string{"-c", "printf host"}, unset: true, want: "host"},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				options := Options{Command: test.command, Args: test.args, Dir: root, Env: map[string]string{"PATH": test.path}, TTY: tty}
				if options.Command == "" {
					options.Command = tool
				}
				if test.unset {
					delete(options.Env, "PATH")
				}
				if tty {
					plan, err := ptyinteract.Compile(nil)
					if err != nil {
						t.Fatal(err)
					}
					options.Interactions = plan
				}
				result, err := Run(t.Context(), options)
				if test.missing {
					if !errors.Is(err, exec.ErrNotFound) {
						t.Fatalf("Run() error = %v, want executable not found", err)
					}
					return
				}
				if err != nil || result.Stdout != test.want {
					t.Fatalf("Run() stdout = %q, error = %v; want %q", result.Stdout, err, test.want)
				}
			})
		}
	}
}
