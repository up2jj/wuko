package process

import (
	"syscall"
	"testing"

	"github.com/up2jj/wuko/ptyinteract"
)

func TestStartedPIDReportsLiveChildOnce(t *testing.T) {
	for _, tty := range []bool{false, true} {
		name := "pipes"
		if tty {
			name = "PTY"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			options := Options{Command: "sh", Args: []string{"-c", "exit 0"}, TTY: tty, StdoutPolicy: OutputDiscard, StderrPolicy: OutputDiscard,
				StartedPID: func(pid int) {
					calls++
					if pid <= 0 {
						t.Errorf("child PID = %d", pid)
						return
					}
					if err := syscall.Kill(pid, 0); err != nil {
						t.Errorf("child not live in start callback: %v", err)
					}
				},
			}
			if tty {
				// A scripted PTY does not need user terminal handoff.
				options.Interactions = &ptyinteract.Plan{}
			}
			if _, err := Run(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("start callback called %d times", calls)
			}
		})
	}
}
