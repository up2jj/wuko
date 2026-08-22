package workflow

// ChildRole identifies one nested step sequence owned by a step declaration.
type ChildRole uint8

const (
	// ChildSteps is the ordinary child body of a block or control.
	ChildSteps ChildRole = iota
	// ChildFinally is the cleanup sequence of an executor block.
	ChildFinally
)

// ChildSequence is a read-only structural view of one nested step sequence. Callers may inspect
// and traverse Steps, but must not append to or replace the returned slice.
type ChildSequence struct {
	Role  ChildRole
	Steps []Step
}

type childSequenceRef struct {
	role  ChildRole
	steps *[]Step
}

// ChildSequences returns the nested step sequences directly owned by workflowStep. Executor bodies
// precede their finally sequence. Resolved composite-action implementations are intentionally not
// declaration children of the caller step.
func (workflowStep Step) ChildSequences() []ChildSequence {
	references := workflowStep.childSequenceRefs()
	children := make([]ChildSequence, len(references))
	for i, reference := range references {
		children[i] = ChildSequence{Role: reference.role, Steps: *reference.steps}
	}
	return children
}

func (workflowStep *Step) transformChildSequences(transform func(ChildRole, []Step) ([]Step, error)) error {
	for _, reference := range workflowStep.childSequenceRefs() {
		children, err := transform(reference.role, *reference.steps)
		if err != nil {
			return err
		}
		*reference.steps = children
	}
	return nil
}

func (workflowStep *Step) childSequenceRefs() []childSequenceRef {
	switch {
	case workflowStep.IsExecutorBlock():
		return []childSequenceRef{
			{role: ChildSteps, steps: &workflowStep.Steps},
			{role: ChildFinally, steps: &workflowStep.Finally},
		}
	case workflowStep.IsWorkingDirectoryBlock(), workflowStep.IsConditionalBlock():
		return []childSequenceRef{{role: ChildSteps, steps: &workflowStep.Steps}}
	case workflowStep.Concurrent != nil:
		return []childSequenceRef{{role: ChildSteps, steps: &workflowStep.Concurrent.Steps}}
	case workflowStep.Batch != nil:
		return []childSequenceRef{{role: ChildSteps, steps: &workflowStep.Batch.Steps}}
	case workflowStep.Foreach != nil:
		return []childSequenceRef{{role: ChildSteps, steps: &workflowStep.Foreach.Steps}}
	case workflowStep.Matrix != nil:
		return []childSequenceRef{{role: ChildSteps, steps: &workflowStep.Matrix.Steps}}
	default:
		return nil
	}
}
