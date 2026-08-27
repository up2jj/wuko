package engine

import "github.com/up2jj/wuko/workflow"

// controlStepRecords returns a stable, declaration-complete view of private control state.
// Canceled race losers may suppress cancellation errors without changing their status.
func controlStepRecords(declarations []workflow.Step, state *State, stats RunStats, suppressCancellation bool) map[string]any {
	records := make(map[string]any)
	byID := make(map[string]StepStats, len(stats.Steps))
	for _, item := range stats.Steps {
		byID[item.ID] = item
	}
	var visit func([]workflow.Step)
	visit = func(steps []workflow.Step) {
		for _, declaration := range steps {
			if children, transparent := transparentChildSequences(declaration); transparent {
				for _, child := range children {
					visit(child.Steps)
				}
				continue
			}
			if declaration.ID == "" || declaration.Return != nil {
				continue
			}
			item, exists := byID[declaration.ID]
			status := StatusSkipped
			var itemErr error
			if exists {
				status = item.Status
				itemErr = item.Error
			}
			record := map[string]any{"status": string(status), "error": controlRecordedError(itemErr, suppressCancellation), "outputs": nil}
			if status == StatusSucceeded && state != nil {
				if value, exists := state.Steps[declaration.ID]; exists {
					record["outputs"] = cloneAny(value)
				}
			}
			records[declaration.ID] = record
		}
	}
	visit(declarations)
	return records
}

func controlWrittenVars(state *State) map[string]any {
	result := make(map[string]any)
	if state == nil {
		return result
	}
	for name := range state.writtenVars {
		result[name] = cloneAny(state.Vars[name])
	}
	return result
}

func controlRecordedError(err error, suppressCancellation bool) any {
	if suppressCancellation && cancellationOnly(err) {
		return nil
	}
	if err == nil {
		return nil
	}
	return err.Error()
}

func allStepsSkipped(stats RunStats) bool {
	if len(stats.Steps) == 0 {
		return false
	}
	for _, item := range stats.Steps {
		if item.Status != StatusSkipped {
			return false
		}
	}
	return true
}
