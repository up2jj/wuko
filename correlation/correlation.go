// Package correlation defines opaque identities used to correlate reporting events.
package correlation

import "crypto/rand"

// InvocationID identifies one reporting session, such as a CLI or UI invocation.
type InvocationID string

// RunID identifies one workflow run attempt.
type RunID string

// StepRunID identifies one concrete step occurrence within a workflow run.
type StepRunID string

// Sequence establishes delivery order across progress and diagnostic events.
type Sequence uint64

// NewInvocationID returns a new opaque invocation identity.
func NewInvocationID() InvocationID { return InvocationID("inv_" + rand.Text()) }

// NewRunID returns a new opaque workflow-run identity.
func NewRunID() RunID { return RunID("run_" + rand.Text()) }

// NewStepRunID returns a new opaque step-run identity.
func NewStepRunID() StepRunID { return StepRunID("step_" + rand.Text()) }
