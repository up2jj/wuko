package engine

import (
	"errors"
	"fmt"

	"github.com/up2jj/wuko/process"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/validation"
	"github.com/up2jj/wuko/workflow"
)

// replacementAudit collects, during one validation pass, the steps that bear on whether the
// workflow may end by replacing the Wuko process. Validation is sequential, so it needs no lock.
type replacementAudit struct {
	replacers []workflow.Step
	cleaners  []workflow.Step
}

func (audit *replacementAudit) record(workflowStep workflow.Step, runner step.Runner) {
	if audit == nil {
		return
	}
	if replacer, ok := runner.(step.ProcessReplacer); ok && replacer.ReplacesProcess() {
		audit.replacers = append(audit.replacers, workflowStep)
	}
	if _, ok := runner.(step.Cleaner); ok {
		audit.cleaners = append(audit.cleaners, workflowStep)
	}
}

// scopeProcessReplacement keeps the host's replacement hook only for the selected workflow
// itself. Composite actions, child workflows, and concurrent branches run below depth zero
// and can never end the process. top additionally excludes runs nested in an existing
// runtime, which share the caller's lifecycle.
func scopeProcessReplacement(definition *workflow.Definition, options Options, top bool) Options {
	options.replaceStepID = ""
	if options.ReplaceProcess == nil {
		return options
	}
	if !top || options.depth != 0 || options.insideExecutor {
		options.ReplaceProcess = nil
		return options
	}
	if count := len(definition.Steps); count > 0 {
		last := definition.Steps[count-1]
		if last.Type != "" && last.Action == nil && len(last.ChildSequences()) == 0 {
			options.replaceStepID = last.ID
		}
	}
	return options
}

// processReplacementHook binds Request.ReplaceProcess. Validation receives a placeholder so a
// replacing runner can confirm the host supports replacement; placement is checked by
// validateProcessReplacement. A run binds the hook only for the final top-level step.
func processReplacementHook(stepID string, options Options, operationID string) func(process.Replacement) error {
	if options.ReplaceProcess == nil || options.depth != 0 || options.insideExecutor {
		return nil
	}
	if operationID == "validation" {
		return func(process.Replacement) error {
			return errors.New("process replacement is unavailable during validation")
		}
	}
	if stepID == "" || stepID != options.replaceStepID || options.runtime == nil {
		return nil
	}
	runtime := options.runtime
	return func(replacement process.Replacement) error {
		// Validation rejects the declarations that would leave work behind; this catches
		// services and resources that steps registered dynamically, such as plugin services.
		if count := runtime.background.count(); count > 0 {
			return fmt.Errorf("cannot replace the process while %d background service(s) are running", count)
		}
		if count := runtime.cleanups.len(); count > 0 {
			return fmt.Errorf("cannot replace the process while %d managed resource(s) await cleanup", count)
		}
		if err := options.ReplaceProcess(replacement); err != nil {
			return err
		}
		runtime.replaced.Store(true)
		return nil
	}
}

// validateProcessReplacement enforces that a replacing step is the final top-level step of a
// workflow with nothing left to do after it: no finally, defer, or background controls, no
// outputs to publish, and no managed resources whose cleanup the replacement would skip.
func validateProcessReplacement(definition *workflow.Definition, options Options, audit *replacementAudit) error {
	if audit == nil || len(audit.replacers) == 0 {
		return nil
	}
	replacer := audit.replacers[0]
	fail := func(located workflow.Step, err error) error {
		return definition.ValidationError(err, validation.CodeInvalidStep, located.ValidationPath(), located.Location, located.ID)
	}
	if len(audit.replacers) > 1 {
		second := audit.replacers[1]
		return fail(second, fmt.Errorf("step %q: only one step may replace the process; step %q already does", second.ID, replacer.ID))
	}
	if replacer.ID != options.replaceStepID {
		return fail(replacer, fmt.Errorf("step %q: replace must be the final top-level step of the workflow, outside every block and control", replacer.ID))
	}
	if len(definition.Finally) > 0 {
		return fail(replacer, fmt.Errorf("step %q: replace cannot be combined with workflow finally steps, which would never run", replacer.ID))
	}
	if len(definition.Outputs) > 0 {
		return fail(replacer, fmt.Errorf("step %q: replace cannot be combined with workflow outputs, which would never be published", replacer.ID))
	}
	var structural error
	walkSteps(definition.Steps, func(candidate workflow.Step) bool {
		switch {
		case len(candidate.Defer) > 0:
			structural = fail(candidate, fmt.Errorf("step %q: defer cannot be combined with replace step %q, because deferred cleanup would never run", candidate.ID, replacer.ID))
		case candidate.IsBackgroundControl():
			structural = fail(candidate, fmt.Errorf("step %q: background controls cannot be combined with replace step %q", candidate.ID, replacer.ID))
		}
		return structural == nil
	})
	if structural != nil {
		return structural
	}
	if len(audit.cleaners) > 0 {
		cleaner := audit.cleaners[0]
		return fail(cleaner, fmt.Errorf("step %q (%s) creates managed resources that are removed when the workflow ends; replace step %q would skip that cleanup", cleaner.ID, cleaner.Type, replacer.ID))
	}
	return nil
}

// walkSteps visits every declaration in steps and its nested sequences until visit returns false.
func walkSteps(steps []workflow.Step, visit func(workflow.Step) bool) bool {
	for _, candidate := range steps {
		if !visit(candidate) {
			return false
		}
		for _, child := range candidate.ChildSequences() {
			if !walkSteps(child.Steps, visit) {
				return false
			}
		}
	}
	return true
}
