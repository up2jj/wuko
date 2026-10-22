package localresource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/up2jj/wuko/process"
	"golang.org/x/sys/unix"
)

func helperOptions(mode string) process.Options {
	return process.Options{Command: os.Args[0], Args: []string{"-test.run=^TestResourceHelperProcess$", "--", mode},
		Env: map[string]string{"WUKO_RESOURCE_TEST_HELPER": "1"}, StdoutPolicy: process.OutputDiscard, StderrPolicy: process.OutputDiscard}
}

func TestResourceHelperProcess(t *testing.T) {
	if os.Getenv("WUKO_RESOURCE_TEST_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "cpu":
		var initial, current unix.Rusage
		if err := unix.Getrusage(unix.RUSAGE_SELF, &initial); err != nil {
			os.Exit(2)
		}
		start := initial.Utime.Nano() + initial.Stime.Nano()
		for {
			if err := unix.Getrusage(unix.RUSAGE_SELF, &current); err != nil {
				os.Exit(2)
			}
			if current.Utime.Nano()+current.Stime.Nano()-start >= int64(200*time.Millisecond) {
				break
			}
		}
	case "memory":
		data := make([]byte, 96<<20)
		for index := range data {
			data[index] = byte(index)
		}
		time.Sleep(time.Hour)
		runtime.KeepAlive(data)
	case "tree":
		for range 2 {
			options := helperOptions("memory")
			command := exec.Command(options.Command, options.Args...)
			command.Env = append(os.Environ(), "WUKO_RESOURCE_TEST_HELPER=1")
			// Real workflow children create their own process groups too.
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := command.Start(); err != nil {
				os.Exit(2)
			}
			if path := os.Getenv("WUKO_RESOURCE_TEST_PIDS"); path != "" {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					os.Exit(2)
				}
				fmt.Fprintln(file, command.Process.Pid)
				file.Close()
			}
		}
		time.Sleep(time.Hour)
	case "wait":
		time.Sleep(time.Hour)
	}
	os.Exit(0)
}

func TestDarwinNativeCPUAccountingUsesKernelTimebase(t *testing.T) {
	api, err := newDarwinMonitor()
	if err != nil {
		t.Fatal(err)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	measured, err := api.usage(darwinIdentity(*info))
	if err != nil {
		t.Fatal(err)
	}
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	want := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	if delta := want - measured.cpu; delta < -20*time.Millisecond || delta > 20*time.Millisecond {
		t.Fatalf("native CPU %s differs from getrusage %s (factor %g)", measured.cpu, want, api.nanosecondsPerTick)
	}
	if measured.memory <= 0 {
		t.Fatalf("resident memory = %d", measured.memory)
	}
	wrong := measured.identity
	wrong.seconds++
	if err := api.signal(wrong, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("signal accepted wrong identity: %v", err)
	}
}

func TestDarwinCPUThrottleLimitsRealWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := Run(ctx, Limits{CPUs: .25}, helperOptions("cpu")); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("200ms CPU workload finished in %s at 0.25 CPUs", elapsed)
	}
}

func TestDarwinMemoryLimitStopsAggregateTreeAndSeparateGroups(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "pids")
	options := helperOptions("tree")
	options.Env["WUKO_RESOURCE_TEST_PIDS"] = path
	_, err := Run(ctx, Limits{Memory: 200 << 20}, options)
	if err == nil || !strings.Contains(err.Error(), "workflow memory limit exceeded") {
		t.Fatalf("Run() = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pids := strings.Fields(string(data))
	if len(pids) != 2 {
		t.Fatalf("child PIDs = %q", data)
	}
	for _, text := range pids {
		pid, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		// A killed orphan can remain a zombie until launchd reaps it.
		info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && info.Proc.P_pid == int32(pid) && info.Proc.P_stat != 5 {
			t.Errorf("descendant %d survived cleanup (state %d)", pid, info.Proc.P_stat)
		}
	}
}

func TestDarwinCancellationResumesAndStopsThrottledWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, Limits{CPUs: .01}, helperOptions("cpu"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("throttled worker did not resume during cancellation")
	}
}
