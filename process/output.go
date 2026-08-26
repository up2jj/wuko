package process

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// OutputPolicy controls whether one child-process stream is forwarded, captured, both, or neither.
// The zero value is OutputTee to preserve the historical process behavior.
type OutputPolicy uint8

const (
	OutputTee OutputPolicy = iota
	OutputInherit
	OutputCapture
	OutputDiscard
)

// ParseOutputPolicy parses a workflow-facing output policy. Empty defaults to tee.
func ParseOutputPolicy(value string) (OutputPolicy, error) {
	switch value {
	case "", "tee":
		return OutputTee, nil
	case "inherit":
		return OutputInherit, nil
	case "capture":
		return OutputCapture, nil
	case "discard":
		return OutputDiscard, nil
	default:
		return OutputTee, fmt.Errorf("must be inherit, capture, tee, or discard")
	}
}

// ParseCaptureLimit parses a positive binary byte size. Empty means unlimited.
func ParseCaptureLimit(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	for _, unit := range []struct {
		suffix     string
		multiplier int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1},
	} {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSuffix(value, unit.suffix)
		if number == "" || strings.HasPrefix(number, "+") || strings.HasPrefix(number, "-") {
			break
		}
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil || parsed <= 0 || parsed > math.MaxInt64/unit.multiplier {
			break
		}
		return parsed * unit.multiplier, nil
	}
	return 0, fmt.Errorf("must be a positive integer followed by B, KiB, MiB, GiB, or TiB")
}

// Streams reports whether output is forwarded to the configured writer.
func (policy OutputPolicy) Streams() bool {
	return policy == OutputTee || policy == OutputInherit
}

// Captures reports whether output is retained in the process result.
func (policy OutputPolicy) Captures() bool {
	return policy == OutputTee || policy == OutputCapture
}

// Valid reports whether policy is one of the defined output policies.
func (policy OutputPolicy) Valid() bool {
	return policy <= OutputDiscard
}
