// Package localresource supervises one local workflow invocation and its descendants.
package localresource

import (
	"fmt"
	"math"
	"strconv"

	"github.com/up2jj/wuko/process"
)

// Limits describes a shared budget. Zero leaves that resource unlimited.
type Limits struct {
	CPUs   float64
	Memory int64
}

// Parse accepts a positive CPU count and a binary memory size. Empty means unlimited.
func Parse(cpus, memory string) (Limits, error) {
	var limits Limits
	if cpus != "" {
		value, err := strconv.ParseFloat(cpus, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.01 || value > 1_000_000 {
			return Limits{}, fmt.Errorf("--cpus must be a finite number from 0.01 through 1000000")
		}
		limits.CPUs = value
	}
	if memory != "" {
		value, err := process.ParseCaptureLimit(memory)
		if err != nil {
			return Limits{}, fmt.Errorf("--memory: %w", err)
		}
		limits.Memory = value
	}
	return limits, nil
}

const workerArgument = "__wuko_resource_worker_v1"

// WorkerArgs validates the Linux scope before allowing a re-executed CLI to run.
// Other invocations pass through unchanged.
func WorkerArgs(args []string) ([]string, error) {
	if len(args) == 0 || args[0] != workerArgument {
		return args, nil
	}
	if len(args) < 4 {
		return nil, fmt.Errorf("invalid resource worker arguments")
	}
	limits, err := Parse(args[1], args[2])
	if err != nil {
		return nil, err
	}
	if err := verifyWorker(limits); err != nil {
		return nil, err
	}
	return args[3:], nil
}
