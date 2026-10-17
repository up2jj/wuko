//go:build darwin || linux

package process

import (
	"sync"
	"syscall"
)

// liveGroups records the process groups wuko started and has not finished with. Every child
// runs in a group of its own so terminal signals reach wuko first, which also means nothing
// stops those groups if wuko exits without its normal teardown.
var liveGroups = struct {
	sync.Mutex
	counts map[int]int
}{counts: make(map[int]int)}

// TrackGroup records a live process group until the returned function is called. Call that
// only after the group leader has been reaped, so the ID cannot have been reused.
func TrackGroup(groupID int) (untrack func()) {
	liveGroups.Lock()
	liveGroups.counts[groupID]++
	liveGroups.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			liveGroups.Lock()
			defer liveGroups.Unlock()
			if liveGroups.counts[groupID]--; liveGroups.counts[groupID] <= 0 {
				delete(liveGroups.counts, groupID)
			}
		})
	}
}

// KillTrackedGroups sends SIGKILL to every tracked process group. It is the last resort of a
// forced shutdown, when wuko exits before its steps and plugins could stop their own processes.
func KillTrackedGroups() {
	liveGroups.Lock()
	defer liveGroups.Unlock()
	for groupID := range liveGroups.counts {
		_ = signalProcessGroup(groupID, syscall.SIGKILL)
	}
}
