package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/up2jj/wuko/diagnostic"
	workflowschedule "github.com/up2jj/wuko/schedule"
	"github.com/up2jj/wuko/workflow"
)

type scheduledRunner struct {
	load        func(context.Context) (*workflow.Definition, func(), error)
	execute     func(context.Context, *workflow.Definition) error
	now         func() time.Time
	wait        func(context.Context, time.Time) error
	stderr      io.Writer
	diagnostics diagnostic.Reporter
}

func (runner scheduledRunner) run(ctx context.Context, initial *workflow.Definition, cleanup func()) error {
	active, err := workflowschedule.Parse(initial.Cron, initial.Timezone)
	if err != nil {
		cleanup()
		return err
	}
	now := runner.now()
	next := active.Next(now)
	if !next.After(now) {
		runErr := runner.execute(ctx, initial)
		cleanup()
		if ctx.Err() != nil {
			return nil
		}
		runner.reportAttemptError(initial, runErr)
		next = active.NextAfter(runner.now())
	} else {
		cleanup()
	}

	for {
		runner.reportWait(initial.Name, active, next)
		if err := runner.wait(ctx, next); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("waiting for scheduled workflow %s: %w", initial.Name, err)
		}

		definition, release, err := runner.load(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			runner.reportReloadError(initial.Name, initial.Location, err)
			next = active.NextAfter(runner.now())
			continue
		}

		candidate, parseErr := parseReloadedSchedule(definition)
		if parseErr != nil {
			release()
			runner.reportReloadError(initial.Name, definition.Location, parseErr)
			next = active.NextAfter(runner.now())
			continue
		}
		if !candidate.Matches(next) {
			active = candidate
			initial = definition
			release()
			diagnostic.Emit(runner.diagnostics, diagnostic.Event{
				Phase: diagnostic.PhaseSchedule, Status: diagnostic.StatusSkipped,
				WorkflowName: definition.Name, Location: definition.Location,
				Message: "schedule changed before occurrence; skipping attempt",
			})
			next = active.NextAfter(runner.now())
			continue
		}

		active = candidate
		initial = definition
		runErr := runner.execute(ctx, definition)
		release()
		if ctx.Err() != nil {
			return nil
		}
		runner.reportAttemptError(definition, runErr)
		next = active.NextAfter(runner.now())
	}
}

func parseReloadedSchedule(definition *workflow.Definition) (*workflowschedule.Schedule, error) {
	if definition.Cron == "" {
		return nil, fmt.Errorf("reloaded workflow no longer declares cron")
	}
	return workflowschedule.Parse(definition.Cron, definition.Timezone)
}

func (runner scheduledRunner) reportWait(name string, schedule *workflowschedule.Schedule, next time.Time) {
	local := next.In(schedule.Location())
	fmt.Fprintf(runner.stderr, "Waiting for workflow %s until %s (%s)\n", name, local.Format(time.RFC3339), schedule.Location())
	diagnostic.Emit(runner.diagnostics, diagnostic.Event{
		Phase: diagnostic.PhaseSchedule, Status: diagnostic.StatusDetail,
		WorkflowName: name, Message: "waiting for scheduled workflow",
		Attributes: []diagnostic.Attribute{
			diagnostic.Attr("cron", schedule.Expression()),
			diagnostic.Attr("timezone", schedule.Location().String()),
			diagnostic.Attr("next", local.Format(time.RFC3339)),
		},
	})
}

func (runner scheduledRunner) reportReloadError(name string, location diagnostic.Location, err error) {
	fmt.Fprintf(runner.stderr, "Workflow %s scheduled reload failed: %v\n", name, err)
	diagnostic.Emit(runner.diagnostics, diagnostic.Event{
		Phase: diagnostic.PhaseSchedule, Status: diagnostic.StatusFailed,
		WorkflowName: name, Location: location, Message: "scheduled reload failed", Error: err,
	})
}

func (runner scheduledRunner) reportAttemptError(definition *workflow.Definition, err error) {
	status := diagnostic.StatusSucceeded
	if err != nil {
		status = diagnostic.StatusFailed
		fmt.Fprintf(runner.stderr, "Workflow %s scheduled attempt failed: %v\n", definition.Name, err)
	}
	diagnostic.Emit(runner.diagnostics, diagnostic.Event{
		Phase: diagnostic.PhaseSchedule, Status: status,
		WorkflowName: definition.Name, Location: definition.Location,
		Message: "scheduled attempt finished", Error: err,
	})
}
