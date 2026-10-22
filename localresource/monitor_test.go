package localresource

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

type recordedSignal struct {
	id     identity
	signal syscall.Signal
}
type fakeMonitor struct {
	processes []processInfo
	signals   []recordedSignal
	usageErr  error
	listErr   error
}

func (api *fakeMonitor) list() ([]processInfo, error)                { return api.processes, api.listErr }
func (api *fakeMonitor) usage(info processInfo) (processInfo, error) { return info, api.usageErr }
func (api *fakeMonitor) signal(id identity, signal syscall.Signal) error {
	api.signals = append(api.signals, recordedSignal{id, signal})
	return nil
}
func testProcess(pid, parent int, cpu time.Duration, memory int64) processInfo {
	return processInfo{identity: identity{pid: pid, seconds: 100}, parent: parent, cpu: cpu, memory: memory}
}

func TestMonitorSharesMemoryBudgetAndTracksReparentedChildren(t *testing.T) {
	api := &fakeMonitor{processes: []processInfo{
		testProcess(12, 11, 0, 40), testProcess(10, 1, 0, 10), testProcess(11, 10, 0, 40), testProcess(99, 1, 0, 10000),
	}}
	monitor := newTreeMonitor(10, Limits{Memory: 100}, api)
	if err := monitor.sample(time.Now()); err != nil {
		t.Fatal(err)
	}
	api.processes[0].parent = 1
	api.processes[0].memory = 60
	err := monitor.sample(time.Now())
	if err == nil || !strings.Contains(err.Error(), "110B > 100B") {
		t.Fatalf("sample() = %v", err)
	}
}

func TestMonitorThrottlesAggregateCPUAndRepaysDebt(t *testing.T) {
	api := &fakeMonitor{processes: []processInfo{testProcess(10, 1, 0, 1), testProcess(11, 10, 0, 1)}}
	monitor := newTreeMonitor(10, Limits{CPUs: 1}, api)
	now := time.Now()
	if err := monitor.sample(now); err != nil {
		t.Fatal(err)
	}
	api.processes[0].cpu, api.processes[1].cpu = sampleInterval, sampleInterval
	if err := monitor.sample(now.Add(sampleInterval)); err != nil {
		t.Fatal(err)
	}
	if len(api.signals) != 2 || api.signals[0].id.pid != 10 || api.signals[0].signal != syscall.SIGSTOP || api.signals[1].signal != syscall.SIGSTOP {
		t.Fatalf("signals = %#v", api.signals)
	}
	if err := monitor.sample(now.Add(2 * sampleInterval)); err != nil {
		t.Fatal(err)
	}
	if len(api.signals) != 4 || api.signals[2].id.pid != 11 || api.signals[2].signal != syscall.SIGCONT || api.signals[3].signal != syscall.SIGCONT {
		t.Fatalf("signals = %#v", api.signals)
	}
}

func TestMonitorDoesNotResumeExternallyStoppedProcesses(t *testing.T) {
	root := testProcess(10, 1, time.Second, 1)
	root.stopped = true
	api := &fakeMonitor{processes: []processInfo{root}}
	monitor := newTreeMonitor(10, Limits{CPUs: 1}, api)
	if err := monitor.sample(time.Now()); err != nil {
		t.Fatal(err)
	}
	monitor.resume()
	if len(api.signals) != 0 {
		t.Fatalf("signals = %#v", api.signals)
	}
}

func TestMonitorRejectsReusedPIDs(t *testing.T) {
	api := &fakeMonitor{processes: []processInfo{testProcess(10, 1, 0, 1), testProcess(11, 10, 0, 1)}}
	monitor := newTreeMonitor(10, Limits{}, api)
	if err := monitor.sample(time.Now()); err != nil {
		t.Fatal(err)
	}
	api.processes[0].seconds++
	api.processes[1].seconds++
	api.processes[1].parent = 1
	if err := monitor.sample(time.Now()); err != nil {
		t.Fatal(err)
	}
	monitor.kill()
	if len(monitor.known) != 0 || len(api.signals) != 0 {
		t.Fatalf("adopted reused PIDs: %#v", monitor.known)
	}
}

func TestMonitorFailsClosedWhenMeasurementsFail(t *testing.T) {
	api := &fakeMonitor{processes: []processInfo{testProcess(10, 1, 0, 1)}, usageErr: syscall.EPERM}
	monitor := newTreeMonitor(10, Limits{Memory: 100}, api)
	if err := monitor.sample(time.Now()); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("sample() = %v", err)
	}
	monitor.kill()
	if len(api.signals) != 1 || api.signals[0].signal != syscall.SIGKILL {
		t.Fatalf("lost discovered process on monitoring error: %#v", api.signals)
	}
}
