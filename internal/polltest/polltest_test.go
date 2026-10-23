package polltest

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitReportsWhetherConditionHeld(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	if !Wait(time.Second, func() bool { return calls.Add(1) == 3 }) {
		t.Fatal("condition that becomes true was reported as timed out")
	}
	if Wait(20*time.Millisecond, func() bool { return false }) {
		t.Fatal("condition that never holds was reported as held")
	}
}
