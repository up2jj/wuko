package engine

import (
	"context"
	"errors"

	"github.com/up2jj/wuko/correlation"
	"github.com/up2jj/wuko/workflow"
)

func leafStepCount(steps []workflow.Step) int {
	total := 0
	for _, workflowStep := range steps {
		if children, transparent := transparentChildSequences(workflowStep); transparent {
			for _, child := range children {
				total += leafStepCount(child.Steps)
			}
			continue
		}
		if workflowStep.Return != nil {
			continue
		}
		if workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil {
			total++
			continue
		}
		total++
	}
	return total
}

func transparentChildSequences(workflowStep workflow.Step) ([]workflow.ChildSequence, bool) {
	if workflowStep.IsExecutorBlock() || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.IsConditionalBlock() || workflowStep.Concurrent != nil {
		return workflowStep.ChildSequences(), true
	}
	return nil, false
}

func rollupNestedMetrics(stats *RunStats, nested RunStats) {
	stats.Attempts += nested.Attempts
	stats.Retries += nested.Retries
	stats.RetryWait += nested.RetryWait
	stats.Polls += nested.Polls
	stats.PollWait += nested.PollWait
	stats.TimedOut += nested.TimedOut
}

func mergeRunStats(target *RunStats, source RunStats) {
	target.Steps = append(target.Steps, source.Steps...)
	target.Succeeded += source.Succeeded
	target.Failed += source.Failed
	target.Skipped += source.Skipped
	target.Canceled += source.Canceled
	target.TimedOut += source.TimedOut
	target.Attempts += source.Attempts
	target.Retries += source.Retries
	target.RetryWait += source.RetryWait
	target.Polls += source.Polls
	target.PollWait += source.PollWait
}

func reportStepFinished(options Options, workflowName, stepID, stepType string, index, total int, stats StepStats) {
	report(options, ProgressEvent{
		Kind: StepFinished, Status: stats.Status, Time: stats.StartedAt.Add(stats.Duration),
		WorkflowName: workflowName, Depth: options.depth, StepID: stepID, StepType: stepType,
		Index: index, Total: total, Attempt: len(stats.Attempts), Duration: stats.Duration,
		Polls: stats.Polls, PollWait: stats.PollWait, Error: stats.Error,
	})
}

func recordStep(stats *RunStats, stepStats StepStats) {
	stats.Steps = append(stats.Steps, stepStats)
	switch stepStats.Status {
	case StatusSucceeded:
		stats.Succeeded++
	case StatusSkipped:
		stats.Skipped++
	case StatusCanceled:
		stats.Canceled++
	default:
		stats.Failed++
	}
	stats.Attempts += len(stepStats.Attempts)
	stats.Retries += max(0, len(stepStats.Attempts)-1)
	stats.RetryWait += stepStats.RetryWait
	stats.Polls += stepStats.Polls
	stats.PollWait += stepStats.PollWait
	stats.TimedOut += stepTimeoutCount(stepStats)
}

func stepTimeoutCount(stepStats StepStats) int {
	timedOut := 0
	for _, attempt := range stepStats.Attempts {
		if attempt.Status == StatusTimedOut {
			timedOut++
		}
	}
	if stepStats.Status == StatusTimedOut && timedOut == 0 {
		return 1
	}
	return timedOut
}

func statusFromError(err error) ExecutionStatus {
	if err == nil {
		return StatusSucceeded
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return StatusCanceled
	}
	return StatusFailed
}

func withStepRun(options Options) Options {
	options.stepRunID = correlation.NewStepRunID()
	return options
}

func runStatsIdentity(options Options) RunStats {
	return RunStats{
		InvocationID: options.InvocationID, RunID: options.runID,
		ParentRunID: options.parentRunID, ParentStepRunID: options.parentStepRunID,
	}
}
