package localresource

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/up2jj/wuko/process"
	"golang.org/x/sys/unix"
)

// Run provides sampled, best-effort process-tree limits on macOS. The supervisor
// remains outside the budget so it can resume a throttled runner on cancellation.
func Run(ctx context.Context, limits Limits, options process.Options) (process.Result, error) {
	api, err := newDarwinMonitor()
	if err != nil {
		return process.Result{}, fmt.Errorf("local resource monitoring is unavailable: %w", err)
	}
	if options.Stderr != nil {
		fmt.Fprintln(options.Stderr, "wuko: macOS resource limits use sampled process-tree monitoring; CPU and memory may briefly exceed the budget")
	}
	return supervise(ctx, limits, options, api)
}

func supervise(ctx context.Context, limits Limits, options process.Options, api processMonitor) (result process.Result, runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result process.Result
		err    error
	}
	done := make(chan outcome, 1)
	started := make(chan int)
	previousStarted := options.StartedPID
	options.StartedPID = func(pid int) {
		if previousStarted != nil {
			previousStarted(pid)
		}
		started <- pid
	}
	options.TerminationParentOnly = true
	options.TerminationGracePeriod = 7 * time.Second
	go func() {
		result, err := process.Run(ctx, options)
		done <- outcome{result, err}
	}()
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	var monitor *treeMonitor
	var monitorErr error
	ctxDone := ctx.Done()
	active := true
	sample := func(now time.Time) {
		if monitor == nil {
			return
		}
		if !active {
			if all, err := monitor.api.list(); err == nil {
				for pid, info := range monitor.descendants(all) {
					monitor.known[pid] = info
				}
			}
			return
		}
		if err := monitor.sample(now); err != nil {
			monitorErr = err
			if errors.Is(err, ErrMemoryLimit) {
				monitorErr = errors.Join(monitorErr, monitor.kill())
			}
			monitor.resume()
			active = false
			cancel()
		}
	}
	defer func() {
		if monitor != nil {
			monitor.resume()
			// Keep identities of dying processes: proc_info may return ESRCH before
			// the process table reports the task as exited. Cleanup needs no usage.
			if all, err := monitor.api.list(); err == nil {
				for pid, info := range monitor.descendants(all) {
					monitor.known[pid] = info
				}
			}
			runErr = errors.Join(runErr, monitor.kill(), waitForTreeExit(monitor))
		}
	}()
	for {
		select {
		case pid := <-started:
			monitor = newTreeMonitor(pid, limits, api)
			if !active {
				monitor.limits = Limits{}
			}
			sample(time.Now())
		case completed := <-done:
			return completed.result, errors.Join(monitorErr, completed.err)
		case <-ctxDone:
			active, ctxDone = false, nil
			if monitor != nil {
				monitor.resume()
				monitor.limits = Limits{}
			}
		case <-ticker.C:
			sample(time.Now())
		}
	}
}

func waitForTreeExit(monitor *treeMonitor) error {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		all, err := monitor.api.list()
		if err != nil {
			return err
		}
		alive := false
		for _, info := range all {
			if known, found := monitor.known[info.pid]; found && known.identity == info.identity && !info.zombie {
				alive = true
				break
			}
		}
		if !alive {
			return nil
		}
		select {
		case <-timer.C:
			return fmt.Errorf("workflow descendants did not exit after SIGKILL")
		case <-ticker.C:
		}
	}
}

func verifyWorker(Limits) error {
	return fmt.Errorf("cgroup resource workers are only supported on Linux")
}

type darwinMonitor struct{ nanosecondsPerTick float64 }

func newDarwinMonitor() (*darwinMonitor, error) {
	// XNU proc_taskinfo CPU counters are Mach absolute-time ticks, including on
	// Apple Silicon where a tick is not a nanosecond. Use the kernel timebase.
	data, err := unix.SysctlRaw("hw.tbfrequency")
	if err != nil {
		return nil, err
	}
	var frequency uint64
	switch len(data) {
	case 4:
		frequency = uint64(binary.NativeEndian.Uint32(data))
	case 8:
		frequency = binary.NativeEndian.Uint64(data)
	}
	if frequency == 0 {
		return nil, fmt.Errorf("invalid kernel timebase frequency")
	}
	api := &darwinMonitor{nanosecondsPerTick: float64(time.Second) / float64(frequency)}
	all, err := api.list()
	if err != nil {
		return nil, err
	}
	for _, info := range all {
		if info.pid == os.Getpid() {
			_, err := api.usage(info)
			return api, err
		}
	}
	return nil, fmt.Errorf("cannot enumerate the supervisor process")
}

func (*darwinMonitor) list() ([]processInfo, error) {
	all, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	result := make([]processInfo, 0, len(all))
	for _, info := range all {
		if info.Proc.P_pid > 0 {
			result = append(result, darwinIdentity(info))
		}
	}
	return result, nil
}

func darwinIdentity(info unix.KinfoProc) processInfo {
	return processInfo{
		identity: identity{pid: int(info.Proc.P_pid), seconds: info.Proc.P_starttime.Sec, micros: int64(info.Proc.P_starttime.Usec)},
		parent:   int(info.Eproc.Ppid), stopped: info.Proc.P_stat == 4, zombie: info.Proc.P_stat == 5,
	}
}

func (api *darwinMonitor) usage(info processInfo) (processInfo, error) {
	// PROC_PIDTASKALLINFO (2) returns proc_bsdinfo (136 bytes) followed by
	// proc_taskinfo (96 bytes). Identity and usage come from the same syscall.
	var data [232]byte
	written, _, errno := unix.Syscall6(
		//lint:ignore SA1019 proc_info has no supported x/sys wrapper
		unix.SYS_PROC_INFO, 2, uintptr(info.pid), 2, 0, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
	)
	runtime.KeepAlive(&data)
	if errno != 0 {
		return processInfo{}, errno
	}
	if written != uintptr(len(data)) {
		return processInfo{}, fmt.Errorf("proc_info returned %d bytes, want %d", written, len(data))
	}
	if int(binary.NativeEndian.Uint32(data[12:])) != info.pid || int64(binary.NativeEndian.Uint64(data[120:])) != info.seconds || int64(binary.NativeEndian.Uint64(data[128:])) != info.micros {
		return processInfo{}, syscall.ESRCH
	}
	info.memory = int64(binary.NativeEndian.Uint64(data[144:]))
	ticks := binary.NativeEndian.Uint64(data[152:]) + binary.NativeEndian.Uint64(data[160:])
	info.cpu = time.Duration(float64(ticks) * api.nanosecondsPerTick)
	return info, nil
}

func (*darwinMonitor) signal(id identity, signal syscall.Signal) error {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", id.pid)
	if err != nil {
		// x/sys maps a zero-length kern.proc.pid result (an exited PID) to EIO.
		if errors.Is(err, syscall.EIO) {
			return syscall.ESRCH
		}
		return err
	}
	if darwinIdentity(*info).identity != id {
		return syscall.ESRCH
	}
	return syscall.Kill(id.pid, signal)
}
