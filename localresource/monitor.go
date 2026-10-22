package localresource

import (
	"errors"
	"fmt"
	"slices"
	"syscall"
	"time"
)

const sampleInterval = 50 * time.Millisecond

// ErrMemoryLimit reports that sampled aggregate resident memory exceeded its budget.
var ErrMemoryLimit = errors.New("workflow memory limit exceeded")

// Process identity includes start time, so a reused PID is never adopted as a
// previously observed descendant. Identity is checked again before every signal.
type identity struct {
	pid     int
	seconds int64
	micros  int64
}

type processInfo struct {
	identity
	parent  int
	stopped bool
	zombie  bool
	cpu     time.Duration
	memory  int64
}

type processMonitor interface {
	list() ([]processInfo, error)
	usage(processInfo) (processInfo, error)
	signal(identity, syscall.Signal) error
}

type treeMonitor struct {
	root     int
	rootID   identity
	rootSeen bool
	known    map[int]processInfo
	stopped  map[identity]bool
	limits   Limits
	credit   float64
	previous time.Time
	api      processMonitor
}

func newTreeMonitor(root int, limits Limits, api processMonitor) *treeMonitor {
	return &treeMonitor{root: root, limits: limits, api: api, known: make(map[int]processInfo), stopped: make(map[identity]bool)}
}

// sample carries CPU debt forward while processes are stopped. Unused credit is
// bounded to one sampling interval, so a long idle period cannot fund a burst.
func (monitor *treeMonitor) sample(now time.Time) error {
	all, err := monitor.api.list()
	if err != nil {
		return fmt.Errorf("enumerating workflow processes: %w", err)
	}
	tree := monitor.descendants(all)
	previousTree := monitor.known
	monitor.known = tree
	var memory int64
	var cpu time.Duration
	for pid, info := range tree {
		if info.zombie {
			continue
		}
		usage, err := monitor.api.usage(info)
		if processGone(err) {
			delete(tree, pid)
			continue
		}
		if err != nil {
			return fmt.Errorf("measuring workflow process %d: %w", pid, err)
		}
		previous := previousTree[pid]
		delta := usage.cpu
		if previous.identity == info.identity {
			delta -= previous.cpu
		}
		if delta > 0 {
			cpu += delta
		}
		memory += usage.memory
		tree[pid] = usage
	}
	for id := range monitor.stopped {
		if info, found := tree[id.pid]; !found || info.identity != id {
			delete(monitor.stopped, id)
		}
	}
	if monitor.limits.Memory > 0 && memory > monitor.limits.Memory {
		return fmt.Errorf("%w: sampled resident memory %dB > %dB", ErrMemoryLimit, memory, monitor.limits.Memory)
	}
	if monitor.limits.CPUs == 0 {
		return nil
	}
	if monitor.previous.IsZero() {
		monitor.previous = now
	}
	monitor.credit = min(monitor.limits.CPUs*float64(sampleInterval), monitor.credit+monitor.limits.CPUs*float64(now.Sub(monitor.previous))) - float64(cpu)
	monitor.previous = now
	return monitor.throttle(monitor.credit < 0)
}

func (monitor *treeMonitor) descendants(all []processInfo) map[int]processInfo {
	tree := make(map[int]processInfo)
	for _, info := range all {
		old, found := monitor.known[info.pid]
		if info.pid == monitor.root && !monitor.rootSeen {
			monitor.rootID, monitor.rootSeen = info.identity, true
		}
		if info.pid == monitor.root && info.identity == monitor.rootID || found && old.identity == info.identity {
			tree[info.pid] = info
		}
	}
	for changed := true; changed; {
		changed = false
		for _, info := range all {
			if _, found := tree[info.pid]; found {
				continue
			}
			if _, found := tree[info.parent]; found {
				tree[info.pid] = info
				changed = true
			}
		}
	}
	return tree
}

func (monitor *treeMonitor) throttle(stop bool) error {
	processes := make([]processInfo, 0, len(monitor.known))
	for _, info := range monitor.known {
		processes = append(processes, info)
	}
	// Stop the runner before its children to reduce new forks during suspension.
	slices.SortFunc(processes, func(a, b processInfo) int {
		if a.pid == monitor.root && b.pid != monitor.root {
			return -1
		}
		if b.pid == monitor.root && a.pid != monitor.root {
			return 1
		}
		return a.pid - b.pid
	})
	if !stop {
		slices.Reverse(processes)
	}
	for _, info := range processes {
		if info.zombie {
			continue
		}
		signal := syscall.SIGCONT
		if stop {
			if info.stopped || monitor.stopped[info.identity] {
				continue
			}
			signal = syscall.SIGSTOP
		} else if !monitor.stopped[info.identity] {
			continue
		}
		if err := monitor.api.signal(info.identity, signal); err != nil && !processGone(err) {
			return fmt.Errorf("signaling workflow process %d: %w", info.pid, err)
		}
		if stop {
			monitor.stopped[info.identity] = true
		} else {
			delete(monitor.stopped, info.identity)
		}
	}
	return nil
}

func (monitor *treeMonitor) resume() {
	for id := range monitor.stopped {
		_ = monitor.api.signal(id, syscall.SIGCONT)
	}
	clear(monitor.stopped)
}

func (monitor *treeMonitor) kill() error {
	var result error
	for _, info := range monitor.known {
		if err := monitor.api.signal(info.identity, syscall.SIGKILL); err != nil && !processGone(err) {
			result = errors.Join(result, fmt.Errorf("stopping workflow process %d: %w", info.pid, err))
		}
	}
	return result
}

func processGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT)
}
