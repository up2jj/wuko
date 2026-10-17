//go:build darwin || linux

package process

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestKillTrackedGroupsStopsOnlyLiveTrackedGroups(t *testing.T) {
	start := func() *exec.Cmd {
		command := exec.Command("sleep", "30")
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			_ = command.Wait()
		})
		return command
	}
	tracked, released := start(), start()
	untrackTracked := TrackGroup(tracked.Process.Pid)
	defer untrackTracked()
	TrackGroup(released.Process.Pid)()

	KillTrackedGroups()

	exited := make(chan error, 1)
	go func() { exited <- tracked.Wait() }()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("tracked group survived KillTrackedGroups")
	}
	if !processGroupAlive(released.Process.Pid) {
		t.Fatal("untracked group was killed")
	}
}
