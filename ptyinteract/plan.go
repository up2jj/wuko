// Package ptyinteract executes ordered writes and prompt-driven interactions against a pseudo-terminal.
package ptyinteract

import (
	"fmt"
	"regexp"
	"time"
)

const (
	// DefaultTimeout bounds a prompt wait when no timeout is configured.
	DefaultTimeout = 30 * time.Second
	// MaxUnmatchedBytes bounds output retained while waiting for one prompt.
	MaxUnmatchedBytes = 1 << 20
)

// Spec describes one already-rendered PTY interaction.
type Spec struct {
	Expect     string
	HasExpect  bool
	Send       string
	Newline    bool
	Timeout    time.Duration
	TimeoutSet bool
	Sensitive  bool
}

type interaction struct {
	expect    string
	pattern   *regexp.Regexp
	send      []byte
	timeout   time.Duration
	sensitive bool
}

// Plan is an immutable compiled interaction sequence. It is safe to reuse across attempts.
type Plan struct {
	interactions []interaction
}

// Compile validates and compiles an interaction sequence.
func Compile(specs []Spec) (*Plan, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("interactions must contain at least one interaction")
	}
	compiled := make([]interaction, len(specs))
	for i, spec := range specs {
		item := interaction{send: []byte(spec.Send), sensitive: spec.Sensitive}
		if spec.Newline {
			item.send = append(item.send, '\r')
		}
		if !spec.HasExpect {
			if spec.TimeoutSet {
				return nil, fmt.Errorf("interaction %d: timeout requires expect", i+1)
			}
			compiled[i] = item
			continue
		}
		if spec.Expect == "" {
			return nil, fmt.Errorf("interaction %d: expect must not be empty", i+1)
		}
		pattern, err := regexp.Compile(spec.Expect)
		if err != nil {
			return nil, fmt.Errorf("interaction %d: compiling expect: %w", i+1, err)
		}
		timeout := spec.Timeout
		if !spec.TimeoutSet {
			timeout = DefaultTimeout
		}
		if timeout <= 0 {
			return nil, fmt.Errorf("interaction %d: timeout must be positive", i+1)
		}
		item.expect = spec.Expect
		item.pattern = pattern
		item.timeout = timeout
		compiled[i] = item
	}
	return &Plan{interactions: compiled}, nil
}

// Len returns the number of interactions in the plan.
func (p *Plan) Len() int {
	if p == nil {
		return 0
	}
	return len(p.interactions)
}

// FailureKind identifies why an interaction sequence stopped.
type FailureKind string

const (
	FailureTimeout  FailureKind = "timeout"
	FailureOverflow FailureKind = "overflow"
	FailureExited   FailureKind = "exited"
	FailureWrite    FailureKind = "write"
)

// Error reports an operational failure at one interaction without exposing its send value.
type Error struct {
	Index  int
	Expect string
	Kind   FailureKind
	Err    error
}

func (e *Error) Error() string {
	switch e.Kind {
	case FailureTimeout:
		return fmt.Sprintf("interaction %d timed out waiting for regex %q", e.Index, e.Expect)
	case FailureOverflow:
		return fmt.Sprintf("interaction %d exceeded %d unmatched output bytes waiting for regex %q", e.Index, MaxUnmatchedBytes, e.Expect)
	case FailureExited:
		return fmt.Sprintf("process exited before interaction %d matched regex %q", e.Index, e.Expect)
	case FailureWrite:
		return fmt.Sprintf("interaction %d write failed: %v", e.Index, e.Err)
	default:
		return fmt.Sprintf("interaction %d failed: %v", e.Index, e.Err)
	}
}

func (e *Error) Unwrap() error { return e.Err }
