package localresource

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/up2jj/wuko/process"
	"golang.org/x/sys/unix"
)

// Run starts a verified cgroup v2 scope through the current user's systemd manager.
// The supervising process stops the scope's remaining members before returning.
func Run(ctx context.Context, limits Limits, options process.Options) (process.Result, error) {
	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return process.Result{}, fmt.Errorf("local resource limits require systemd-run and a running systemd user manager on Linux: %w", err)
	}
	controller, err := exec.LookPath("systemctl")
	if err != nil {
		return process.Result{}, fmt.Errorf("local resource limits require systemctl for scope cleanup: %w", err)
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return process.Result{}, fmt.Errorf("local resource limits require cgroup v2: %w", err)
	}
	unit := "wuko-" + uuid.NewString() + ".scope"
	args := scopeArgs(limits, unit, options.Command, options.Args)
	options.Command, options.Args = runner, args
	options.TerminationParentOnly = true
	options.TerminationGracePeriod = 7 * time.Second
	defer func() {
		// Stop any descendant that outlived the CLI, including commands in separate
		// process groups. An already collected scope needs no cleanup.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanupCtx, controller, "--user", "kill", "--kill-whom=all", "--signal=KILL", unit).Run()
	}()
	return process.Run(ctx, options)
}

func verifyWorker(limits Limits) error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("verifying local resource limits: %w", err)
	}
	dir, err := unifiedCgroup(string(data))
	if err != nil {
		return err
	}
	var info unix.Statfs_t
	if err := unix.Statfs(dir, &info); err != nil {
		return fmt.Errorf("verifying local resource cgroup: %w", err)
	}
	if info.Type != unix.CGROUP2_SUPER_MAGIC {
		return fmt.Errorf("local resource scope is not on cgroup v2")
	}
	if err := verifyFiles(dir, limits, os.ReadFile); err != nil {
		return fmt.Errorf("local resource limits were not enforced; check systemd user controller delegation: %w", err)
	}
	return nil
}
