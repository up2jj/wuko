package workflow

// ChildRole identifies one nested step sequence owned by a step declaration.
type ChildRole uint8

const (
	// ChildSteps is the ordinary child body of a block or control.
	ChildSteps ChildRole = iota
	// ChildFinally is the cleanup sequence of an executor block.
	ChildFinally
	// ChildDefer is cleanup attached to an ordinary step.
	ChildDefer
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
// precede their finally sequence, and attached defer cleanup follows other children. Resolved
// composite-action implementations are intentionally not declaration children of the caller step.
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
	deferred := func(children []childSequenceRef) []childSequenceRef {
		if workflowStep.Defer != nil {
			children = append(children, childSequenceRef{role: ChildDefer, steps: &workflowStep.Defer})
		}
		return children
	}
	switch {
	case workflowStep.IsExecutorBlock():
		return deferred([]childSequenceRef{
			{role: ChildSteps, steps: &workflowStep.Steps},
			{role: ChildFinally, steps: &workflowStep.Finally},
		})
	case workflowStep.IsWorkingDirectoryBlock(), workflowStep.IsConditionalBlock():
		return deferred([]childSequenceRef{{role: ChildSteps, steps: &workflowStep.Steps}})
	case workflowStep.Concurrent != nil:
		return deferred([]childSequenceRef{{role: ChildSteps, steps: &workflowStep.Concurrent.Steps}})
	case workflowStep.Batch != nil:
		return deferred([]childSequenceRef{{role: ChildSteps, steps: &workflowStep.Batch.Steps}})
	case workflowStep.Foreach != nil:
		return deferred([]childSequenceRef{{role: ChildSteps, steps: &workflowStep.Foreach.Steps}})
	case workflowStep.Matrix != nil:
		return deferred([]childSequenceRef{{role: ChildSteps, steps: &workflowStep.Matrix.Steps}})
	default:
		return deferred(nil)
	}
}
