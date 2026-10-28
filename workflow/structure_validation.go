package workflow

import (
	"errors"
	"fmt"
	"strings"

	controlpkg "github.com/up2jj/wuko/control"
)

// ValidateStructure validates the workflow declaration independently of templates and runtime context.
func (definition *Definition) ValidateStructure() error {
	return validateDefinitionStructure(definition, true)
}

func validateDefinitionStructure(definition *Definition, allowActions bool) error {
	if err := validateDefinitionHeader(definition); err != nil {
		return err
	}
	if definition.HasTargets() {
		for _, name := range definition.TargetNames() {
			if !identifierPattern.MatchString(name) {
				return fmt.Errorf("invalid target name %q", name)
			}
			selected, err := definition.SelectTarget(name)
			if err != nil {
				return err
			}
			if err := validateDefinitionStructure(selected, allowActions); err != nil {
				return fmt.Errorf("target %q: %w", name, err)
			}
		}
		return nil
	}
	if err := definition.ValidateOutputContract(); err != nil {
		return err
	}
	if err := validateNeedsPlacement(definition.Steps, false); err != nil {
		return err
	}
	if err := validateNeedsPlacement(definition.Finally, false); err != nil {
		return fmt.Errorf("finally: %w", err)
	}

	seen := make(map[string]struct{}, len(definition.Steps)+len(definition.Finally))
	if err := collectScopeIDs(definition.Steps, seen); err != nil {
		return err
	}
	if err := collectScopeIDs(definition.Finally, seen); err != nil {
		return fmt.Errorf("finally: %w", err)
	}
	if err := validateSteps(definition.Steps, allowActions, scopeTop, seen); err != nil {
		return err
	}
	if err := validateSteps(definition.Finally, allowActions, scopeFinally, seen); err != nil {
		return fmt.Errorf("finally: %w", err)
	}
	if err := validateCleanupServices(definition.Steps, false); err != nil {
		return err
	}
	if err := validateCleanupServices(definition.Finally, true); err != nil {
		return fmt.Errorf("finally: %w", err)
	}
	if err := validateLifecycleSteps(definition.Install, allowActions, "install"); err != nil {
		return err
	}
	if err := validateLifecycleSteps(definition.Uninstall, allowActions, "uninstall"); err != nil {
		return err
	}
	for name := range definition.Env {
		if !environmentPattern.MatchString(name) {
			return fmt.Errorf("invalid environment name %q", name)
		}
	}
	for alias, name := range definition.DependsOn {
		if !identifierPattern.MatchString(alias) {
			return fmt.Errorf("invalid dependency alias %q", alias)
		}
		if !ValidWorkflowName(name) {
			return fmt.Errorf("dependency %q has invalid workflow name %q", alias, name)
		}
	}
	return nil
}

func validateLifecycleSteps(steps []Step, allowActions bool, name string) error {
	if len(steps) == 0 {
		return nil
	}
	if containsObserve(steps) {
		return fmt.Errorf("%s: observe is not supported in lifecycle hooks", name)
	}
	seen := make(map[string]struct{}, len(steps))
	if err := validateNeedsPlacement(steps, false); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := collectScopeIDs(steps, seen); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := validateSteps(steps, allowActions, scopeTop, seen); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func containsObserve(steps []Step) bool {
	for _, workflowStep := range steps {
		if workflowStep.IsObserve() {
			return true
		}
		for _, child := range workflowStep.ChildSequences() {
			if containsObserve(child.Steps) {
				return true
			}
		}
	}
	return false
}

func validateNeedsPlacement(steps []Step, concurrentChildren bool) error {
	for index, workflowStep := range steps {
		needs := workflowStep.hasNeeds || len(workflowStep.Needs) > 0
		after := workflowStep.hasAfter || len(workflowStep.After) > 0
		if (needs || after) && !concurrentChildren {
			label := workflowStep.ID
			if label == "" {
				label = fmt.Sprintf("step %d", index+1)
			} else {
				label = fmt.Sprintf("step %q", label)
			}
			field := "needs"
			if !needs {
				field = "after"
			}
			return fmt.Errorf("%s: %s is only supported on direct children of concurrent", label, field)
		}
		for _, child := range workflowStep.ChildSequences() {
			childrenAreConcurrent := workflowStep.Concurrent != nil && child.Role == ChildSteps
			if err := validateNeedsPlacement(child.Steps, childrenAreConcurrent); err != nil {
				return err
			}
		}
	}
	return nil
}

type stepScope uint8

const (
	scopeTop stepScope = iota
	scopeControl
	scopeConcurrent
	scopeFinally
	scopeDefer
	scopeExecutor
	scopeExecutorControl
	scopeExecutorFinally
	scopeExecutorDefer
)

func validateStepScope(steps []Step, allowActions bool, scope stepScope, inherited map[string]struct{}) error {
	seen := make(map[string]struct{}, len(inherited)+len(steps))
	for id := range inherited {
		seen[id] = struct{}{}
	}
	if err := collectScopeIDs(steps, seen); err != nil {
		return err
	}
	return validateSteps(steps, allowActions, scope, seen)
}

func collectScopeIDs(steps []Step, seen map[string]struct{}) error {
	for i, workflowStep := range steps {
		if workflowStep.IsTryCatch() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			if workflowStep.Try != nil {
				if err := collectScopeIDs(workflowStep.Try.Steps, seen); err != nil {
					return fmt.Errorf("step %q try: %w", workflowStep.ID, err)
				}
			}
			if workflowStep.Catch != nil {
				if err := collectScopeIDs(workflowStep.Catch.Steps, seen); err != nil {
					return fmt.Errorf("step %q catch: %w", workflowStep.ID, err)
				}
			}
			continue
		}
		if workflowStep.IsCancelOn() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			for monitorIndex, monitor := range workflowStep.CancelOn.Monitors {
				if !identifierPattern.MatchString(monitor.ID) {
					return fmt.Errorf("step %q cancel_on monitor %d has invalid id %q", workflowStep.ID, monitorIndex+1, monitor.ID)
				}
				if _, exists := seen[monitor.ID]; exists {
					return fmt.Errorf("duplicate step id %q", monitor.ID)
				}
				seen[monitor.ID] = struct{}{}
				declaration := cancelOnMonitorDeclaration(monitor)
				if declaration.ID == "" {
					for _, child := range declaration.ChildSequences() {
						if err := collectScopeIDs(child.Steps, seen); err != nil {
							return fmt.Errorf("step %q cancel_on monitor %q: %w", workflowStep.ID, monitor.ID, err)
						}
					}
				}
			}
			if err := collectScopeIDs(workflowStep.CancelOn.Steps, seen); err != nil {
				return fmt.Errorf("step %q cancel_on body: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsObserve() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			continue
		}
		if workflowStep.IsLoop() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			continue
		}
		if workflowStep.IsOnce() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			continue
		}
		if workflowStep.IsAttempt() {
			if !identifierPattern.MatchString(workflowStep.ID) {
				return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
			}
			if _, exists := seen[workflowStep.ID]; exists {
				return fmt.Errorf("duplicate step id %q", workflowStep.ID)
			}
			seen[workflowStep.ID] = struct{}{}
			continue
		}
		container := workflowStep.IsExecutorBlock() || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsWorktreeBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil
		if container {
			if workflowStep.IsWorktreeBlock() {
				if !identifierPattern.MatchString(workflowStep.ID) {
					return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
				}
				if _, exists := seen[workflowStep.ID]; exists {
					return fmt.Errorf("duplicate step id %q", workflowStep.ID)
				}
				seen[workflowStep.ID] = struct{}{}
			}
			for _, child := range workflowStep.ChildSequences() {
				if err := collectScopeIDs(child.Steps, seen); err != nil {
					context := fmt.Sprintf("step %d", i+1)
					if workflowStep.IsExecutorBlock() {
						context += " executor"
						if child.Role == ChildFinally {
							context += " finally"
						}
					}
					if child.Role == ChildDefer {
						context += " defer"
					}
					return fmt.Errorf("%s: %w", context, err)
				}
			}
			continue
		}
		if workflowStep.Return != nil {
			continue
		}
		if workflowStep.Require != nil {
			continue
		}
		if !identifierPattern.MatchString(workflowStep.ID) {
			return fmt.Errorf("step %d has invalid id %q", i+1, workflowStep.ID)
		}
		if _, exists := seen[workflowStep.ID]; exists {
			return fmt.Errorf("duplicate step id %q", workflowStep.ID)
		}
		seen[workflowStep.ID] = struct{}{}
		if err := collectScopeIDs(workflowStep.Defer, seen); err != nil {
			return fmt.Errorf("step %q defer: %w", workflowStep.ID, err)
		}
	}
	return nil
}

func validateSteps(steps []Step, allowActions bool, scope stepScope, seen map[string]struct{}) (resultErr error) {
	var current Step
	defer func() {
		if resultErr == nil {
			return
		}
		var located *locatedValidationError
		if !errors.As(resultErr, &located) {
			resultErr = &locatedValidationError{location: current.Location, err: resultErr}
		}
	}()
	for i, workflowStep := range steps {
		current = workflowStep
		if workflowStep.IsTryCatch() {
			if err := validateTryCatchEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsCancelOn() {
			if err := validateCancelOnEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsObserve() {
			if err := validateObserveEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsOnce() {
			if err := validateOnceEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsAttempt() {
			if err := validateAttemptEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.IsExecutorBlock() {
			if err := validateExecutorBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsEnvironmentBlock() {
			if err := validateEnvironmentBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			if err := validateWorkingDirectoryBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsWorktreeBlock() {
			if err := validateWorktreeEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			if err := validateConditionalBlock(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Require != nil {
			return fmt.Errorf("step %d: require is only supported in workflow step files", i+1)
		}
		if workflowStep.Return != nil {
			if err := validateReturnEntry(workflowStep, scope); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			if err := validateConcurrentEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil {
			if workflowStep.Loop != nil {
				if err := validateLoopEntry(workflowStep, scope, allowActions, seen); err != nil {
					return fmt.Errorf("step %q: %w", workflowStep.ID, err)
				}
				continue
			}
			if err := validateControlEntry(workflowStep, scope, allowActions, seen); err != nil {
				return fmt.Errorf("step %q: %w", workflowStep.ID, err)
			}
			continue
		}
		if workflowStep.Finally != nil {
			return fmt.Errorf("step %q: finally is only supported by executor blocks", workflowStep.ID)
		}
		if workflowStep.Defer != nil {
			if len(workflowStep.Defer) == 0 {
				return fmt.Errorf("step %q: defer must contain at least one step", workflowStep.ID)
			}
			deferScope := scopeDefer
			switch scope {
			case scopeTop:
			case scopeExecutor:
				deferScope = scopeExecutorDefer
			case scopeConcurrent, scopeControl, scopeExecutorControl:
				return fmt.Errorf("step %q: defer is only supported in sequential scopes", workflowStep.ID)
			case scopeFinally, scopeDefer, scopeExecutorFinally, scopeExecutorDefer:
				return fmt.Errorf("step %q: defer is not supported inside cleanup", workflowStep.ID)
			}
			if err := validateSteps(workflowStep.Defer, allowActions, deferScope, seen); err != nil {
				return fmt.Errorf("step %q defer: %w", workflowStep.ID, err)
			}
		}
		if executorStepScope(scope) && !workflowStep.Uses.Empty() {
			return fmt.Errorf("step %q: actions are not supported inside executor blocks", workflowStep.ID)
		}
		if (workflowStep.Type == "") == workflowStep.Uses.Empty() {
			return fmt.Errorf("step %q must set exactly one of type or uses", workflowStep.ID)
		}
		if !workflowStep.Uses.Empty() && !allowActions {
			return fmt.Errorf("step %q: nested composite actions are not supported", workflowStep.ID)
		}
		if workflowStep.Uses.Empty() && workflowStep.SHA256 != "" {
			return fmt.Errorf("step %q: sha256 requires uses", workflowStep.ID)
		}
		if workflowStep.With == nil {
			workflowStep.With = make(map[string]any)
			steps[i] = workflowStep
		}
	}
	return nil
}

func validateTryCatchEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Once != nil || workflowStep.CancelOn != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Attempt != nil || workflowStep.With != nil || workflowStep.Steps != nil {
		return fmt.Errorf("try/catch cannot be combined with other step fields")
	}
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("try/catch requires a valid id")
	}
	if scope == scopeFinally || scope == scopeDefer || scope == scopeExecutorFinally || scope == scopeExecutorDefer {
		return fmt.Errorf("try/catch is not supported inside cleanup")
	}
	if err := validateTryCatchDeclaration(workflowStep); err != nil {
		return err
	}
	if err := tryCatchContainsForbidden(workflowStep.Try.Steps); err != nil {
		return fmt.Errorf("try: %w", err)
	}
	if err := tryCatchContainsForbidden(workflowStep.Catch.Steps); err != nil {
		return fmt.Errorf("catch: %w", err)
	}
	if err := validateSteps(workflowStep.Try.Steps, allowActions, scope, seen); err != nil {
		return fmt.Errorf("try: %w", err)
	}
	if err := validateSteps(workflowStep.Catch.Steps, allowActions, scope, seen); err != nil {
		return fmt.Errorf("catch: %w", err)
	}
	return nil
}

func validateCancelOnEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Once != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Attempt != nil || workflowStep.With != nil || workflowStep.Steps != nil {
		return fmt.Errorf("cancel_on cannot be combined with other step fields")
	}
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("cancel_on requires a valid id")
	}
	// Participants race concurrently against the enclosing scope's own steps, which an
	// executor block cannot serve: every branch would share the one open session.
	if executorStepScope(scope) {
		return fmt.Errorf("cancel_on controls are not supported inside executor blocks")
	}
	if err := workflowStep.CancelOn.Validate(); err != nil {
		return err
	}
	if err := cancelOnContainsForbidden(workflowStep.CancelOn.Monitors); err != nil {
		return fmt.Errorf("monitors: %w", err)
	}
	if err := cancelOnContainsForbidden(workflowStep.CancelOn.Steps); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	// Each participant is its own sequential scope, but it may not reach past the nesting
	// the control itself sits in: children keep the enclosing scope's restrictions.
	for _, monitor := range workflowStep.CancelOn.Monitors {
		declaration := cancelOnMonitorDeclaration(monitor)
		if err := validateSteps([]Step{declaration}, allowActions, scope, seen); err != nil {
			return fmt.Errorf("monitor %q: %w", monitor.ID, err)
		}
	}
	if err := validateSteps(workflowStep.CancelOn.Steps, allowActions, scope, seen); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	return nil
}

func validateObserveEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("observe requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Once != nil || workflowStep.CancelOn != nil || workflowStep.IsTryCatch() || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Attempt != nil || workflowStep.With != nil || workflowStep.Steps != nil {
		return fmt.Errorf("observe cannot be combined with other step fields")
	}
	if scope != scopeTop {
		return fmt.Errorf("observe is only supported in the main sequential workflow scope")
	}
	if !allowActions {
		return fmt.Errorf("observe is not supported inside composite actions")
	}
	if err := workflowStep.Observe.Validate(); err != nil {
		return err
	}
	return validateStepScope(workflowStep.Observe.Steps, allowActions, scopeControl, enclosing)
}

func validateOnceEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("once requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.CancelOn != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Attempt != nil || workflowStep.With != nil || workflowStep.Steps != nil {
		return fmt.Errorf("once cannot be combined with ordinary step fields")
	}
	if scope == scopeFinally || scope == scopeDefer || scope == scopeExecutorFinally || scope == scopeExecutorDefer {
		return fmt.Errorf("once is not supported inside cleanup")
	}
	if err := workflowStep.Once.Validate(); err != nil {
		return err
	}
	if err := onceContainsDefer(workflowStep.Once.Steps); err != nil {
		return err
	}
	if err := onceContainsReturn(workflowStep.Once.Steps); err != nil {
		return err
	}
	// The body gets its own id scope because it runs on a private state clone, but it may
	// not reach past the nesting the once itself sits in: children keep the enclosing
	// scope's restrictions, exactly as try/catch and cancel_on bodies do.
	return validateStepScope(workflowStep.Once.Steps, allowActions, scope, enclosing)
}

func onceContainsDefer(steps []Step) error {
	for _, step := range steps {
		if step.Defer != nil {
			return fmt.Errorf("defer is not supported inside once")
		}
		for _, child := range step.ChildSequences() {
			if err := onceContainsDefer(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

// onceContainsReturn rejects a return anywhere in a once body. executeOnce runs the body
// on a cloned state and records the outcome durably; the clone's returning flag is never
// propagated, so a return would be swallowed and the swallowing baked into the record.
// Cleanup sequences are left to validateSteps, which reports them against their own scope.
func onceContainsReturn(steps []Step) error {
	for _, step := range steps {
		if step.Return != nil {
			return fmt.Errorf("return is not supported inside once")
		}
		for _, child := range step.ChildSequences() {
			if child.Role == ChildDefer || child.Role == ChildFinally {
				continue
			}
			if err := onceContainsReturn(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWorkingDirectoryBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.validateBlock(); err != nil {
		return err
	}
	return validateSteps(workflowStep.Steps, allowActions, scope, seen)
}

func validateEnvironmentBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.validateBlock(); err != nil {
		return err
	}
	return validateSteps(workflowStep.Steps, allowActions, scope, seen)
}

func validateWorktreeEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.ValidateBlock(); err != nil {
		return err
	}
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("worktree block requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.WorkingDirectory != "" || workflowStep.Steps != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Attempt != nil || workflowStep.With != nil {
		return fmt.Errorf("worktree block cannot be combined with other step fields")
	}
	// The worktree is removed when the block ends, so nothing in it may outlive the block.
	if containsObserve(workflowStep.Worktree.Steps) {
		return fmt.Errorf("observe is not supported inside worktree blocks")
	}
	return validateSteps(workflowStep.Worktree.Steps, allowActions, scope, seen)
}

func validateConditionalBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if err := workflowStep.validateBlock(); err != nil {
		return err
	}
	if scope == scopeConcurrent {
		return fmt.Errorf("conditional blocks are not supported inside concurrent groups")
	}
	for _, child := range workflowStep.Steps {
		if child.IsConditionalBlock() {
			return fmt.Errorf("nested conditional blocks are not supported")
		}
	}
	return validateSteps(workflowStep.Steps, allowActions, scope, seen)
}

func validateConcurrentEntry(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsConditionalBlock() || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Attempt != nil || workflowStep.With != nil {
		return fmt.Errorf("concurrent cannot be combined with other step fields")
	}
	if scope == scopeConcurrent {
		return fmt.Errorf("nested concurrent groups are not supported")
	}
	if executorStepScope(scope) {
		return fmt.Errorf("concurrent groups are not supported inside executor blocks")
	}
	group := workflowStep.Concurrent
	if err := group.Validate(); err != nil {
		return err
	}
	return validateSteps(group.Steps, allowActions, scopeConcurrent, seen)
}

// validateCleanupServices rejects service steps inside cleanup. A scope stops and joins its
// services before running finally or defer, so a service started from cleanup could never be
// launched, and the failure would otherwise surface only at run time.
func validateCleanupServices(steps []Step, cleanup bool) error {
	for _, workflowStep := range steps {
		if cleanup && workflowStep.Type == "process" {
			return fmt.Errorf("step %q: process steps are not supported inside finally or defer, which join services rather than start them", workflowStep.ID)
		}
		for _, child := range workflowStep.ChildSequences() {
			nested := cleanup || child.Role == ChildFinally || child.Role == ChildDefer
			if err := validateCleanupServices(child.Steps, nested); err != nil {
				return err
			}
		}
	}
	return nil
}

func executorStepScope(scope stepScope) bool {
	return scope == scopeExecutor || scope == scopeExecutorControl || scope == scopeExecutorFinally || scope == scopeExecutorDefer
}

func validateControlEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	controlCount := 0
	for _, present := range []bool{workflowStep.Batch != nil, workflowStep.Foreach != nil, workflowStep.Matrix != nil} {
		if present {
			controlCount++
		}
	}
	if controlCount != 1 {
		return fmt.Errorf("must set exactly one of batch, foreach or matrix")
	}
	kind := "foreach"
	var children []Step
	var validationErr error
	if workflowStep.Batch != nil {
		kind = "batch"
		children = workflowStep.Batch.Steps
		validationErr = workflowStep.Batch.Validate()
	} else if workflowStep.Matrix != nil {
		kind = "matrix"
		children = workflowStep.Matrix.Steps
		validationErr = workflowStep.Matrix.Validate()
	} else {
		children = workflowStep.Foreach.Steps
		validationErr = workflowStep.Foreach.Validate()
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Attempt != nil || workflowStep.With != nil {
		return fmt.Errorf("%s cannot be combined with ordinary step fields", kind)
	}
	if scope != scopeTop && scope != scopeExecutor {
		return fmt.Errorf("nested %s controls are not supported", kind)
	}
	if validationErr != nil {
		return validationErr
	}
	childScope := scopeControl
	if scope == scopeExecutor {
		if maxConcurrency := controlMaxConcurrency(workflowStep); maxConcurrency != 1 {
			return fmt.Errorf("%s inside executor must use max_concurrency 1", kind)
		}
		childScope = scopeExecutorControl
	}
	return validateStepScope(children, allowActions, childScope, enclosing)
}

func validateLoopEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("loop requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.Attempt != nil || workflowStep.With != nil || workflowStep.If != "" {
		return fmt.Errorf("loop cannot be combined with ordinary step fields")
	}
	if scope != scopeTop && scope != scopeExecutor {
		return fmt.Errorf("nested loops are not supported")
	}
	if err := workflowStep.Loop.Validate(); err != nil {
		return err
	}
	if err := controlpkg.ValidateExpression(string(workflowStep.Loop.Until)); err != nil {
		return fmt.Errorf("loop until: %w", err)
	}
	childScope := scopeControl
	if scope == scopeExecutor {
		childScope = scopeExecutorControl
	}
	return validateStepScope(workflowStep.Loop.Steps, allowActions, childScope, enclosing)
}

func controlMaxConcurrency(workflowStep Step) int {
	if workflowStep.Batch != nil {
		return workflowStep.Batch.MaxConcurrency
	}
	if workflowStep.Foreach != nil {
		return workflowStep.Foreach.MaxConcurrency
	}
	return workflowStep.Matrix.MaxConcurrency
}

func validateExecutorBlock(workflowStep Step, scope stepScope, allowActions bool, seen map[string]struct{}) error {
	if scope != scopeTop {
		return fmt.Errorf("executor blocks are only supported in sequential workflow scopes")
	}
	if !allowActions {
		return fmt.Errorf("executor blocks are not supported in composite actions")
	}
	if workflowStep.Executor == nil || strings.TrimSpace(workflowStep.Executor.Type) == "" {
		return fmt.Errorf("executor type is required")
	}
	if len(workflowStep.Steps) == 0 {
		return fmt.Errorf("executor block must contain at least one step")
	}
	if workflowStep.ID != "" || workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Defer != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.If != "" || workflowStep.Attempt != nil || workflowStep.With != nil {
		return fmt.Errorf("executor block cannot be combined with other step fields")
	}
	if err := validateSteps(workflowStep.Steps, allowActions, scopeExecutor, seen); err != nil {
		return fmt.Errorf("executor steps: %w", err)
	}
	if err := validateSteps(workflowStep.Finally, allowActions, scopeExecutorFinally, seen); err != nil {
		return fmt.Errorf("executor finally: %w", err)
	}
	return nil
}
