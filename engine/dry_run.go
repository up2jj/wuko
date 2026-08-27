package engine

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/workflow"
)

func writeDryRun(writer io.Writer, steps []workflow.Step, indent string, parent []int) error {
	for i, workflowStep := range steps {
		path := append(append([]int(nil), parent...), i+1)
		index := dryRunIndex(path)
		if workflowStep.IsCancelOn() {
			collect := ""
			if workflowStep.CancelOn.Collect != "" {
				collect = "; collect " + workflowStep.CancelOn.Collect
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s (cancel_on%s)%s\n", indent, index, workflowStep.ID, collect, dryRunCondition(workflowStep)); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(writer, indent+"   monitors:"); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.CancelOn.Monitors, indent+"      ", path); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(writer, indent+"   steps:"); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.CancelOn.Steps, indent+"      ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsExecutorBlock() {
			label := fmt.Sprintf("executor: %s", workflowStep.Executor.Type)
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (executor: %s)", workflowStep.ID, workflowStep.Executor.Type)
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s\n", indent, index, label); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Steps, indent+"   ", path); err != nil {
				return err
			}
			if len(workflowStep.Finally) > 0 {
				if _, err := fmt.Fprintf(writer, "%s   finally:\n", indent); err != nil {
					return err
				}
				if err := writeDryRun(writer, workflowStep.Finally, indent+"      ", path); err != nil {
					return err
				}
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			label := "working_directory: " + workflowStep.WorkingDirectory
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (working_directory: %s)", workflowStep.ID, workflowStep.WorkingDirectory)
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s\n", indent, index, label); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsWorktreeBlock() {
			publish := ""
			if workflowStep.Worktree.Publish != nil {
				publish = "; publish " + workflowStep.Worktree.Publish.Branch
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s (worktree %s%s)\n", indent, index, workflowStep.ID, workflowStep.Worktree.Revision, publish); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Worktree.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			label := "if: " + string(workflowStep.If)
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (if: %s)", workflowStep.ID, workflowStep.If)
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s\n", indent, index, label); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			label := "concurrent" + concurrentPolicySuffix(workflowStep.Concurrent)
			if workflowStep.ID != "" {
				label = workflowStep.ID + " (concurrent" + concurrentPolicySuffix(workflowStep.Concurrent) + ")"
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s\n", indent, index, label); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Concurrent.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Return != nil {
			names := strings.Join(slices.Sorted(maps.Keys(workflowStep.Return.Outputs)), ", ")
			if names == "" {
				names = "{}"
			}
			if _, err := fmt.Fprintf(writer, "%s%s return (outputs: %s)%s\n", indent, index, names, dryRunCondition(workflowStep)); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Batch != nil {
			if _, err := fmt.Fprintf(writer, "%s%s %s (batch %s by %s%s)%s%s\n", indent, index, workflowStep.ID, workflowStep.Batch.Items, batchSizeLabel(workflowStep.Batch.Size), fanoutCollectSuffix(workflowStep.Batch.Collect), fanoutPolicySuffix(workflowStep.Batch.MaxConcurrency, workflowStep.Batch.MaxIterations, workflowStep.Batch.Timeout, workflowStep.Batch.FailFast), dryRunCondition(workflowStep)); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Batch.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Foreach != nil {
			if _, err := fmt.Fprintf(writer, "%s%s %s (foreach %s%s)%s%s\n", indent, index, workflowStep.ID, workflowStep.Foreach.Items, fanoutCollectSuffix(workflowStep.Foreach.Collect), fanoutPolicySuffix(workflowStep.Foreach.MaxConcurrency, workflowStep.Foreach.MaxIterations, workflowStep.Foreach.Timeout, workflowStep.Foreach.FailFast), dryRunCondition(workflowStep)); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Foreach.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Matrix != nil {
			if _, err := fmt.Fprintf(writer, "%s%s %s (matrix %s%s)%s%s\n", indent, index, workflowStep.ID, matrixAxisNames(workflowStep.Matrix.Axes), fanoutCollectSuffix(workflowStep.Matrix.Collect), fanoutPolicySuffix(workflowStep.Matrix.MaxConcurrency, workflowStep.Matrix.MaxIterations, workflowStep.Matrix.Timeout, workflowStep.Matrix.FailFast), dryRunCondition(workflowStep)); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Matrix.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		kind := workflowStep.Type
		if workflowStep.Action != nil {
			kind = "uses " + workflowStep.Uses.Display()
		}
		condition := ""
		if workflowStep.If != "" {
			condition = " if: " + string(workflowStep.If)
		}
		if _, err := fmt.Fprintf(writer, "%s%s %s (%s)%s%s\n", indent, index, workflowStep.ID, kind, executionPolicySuffix(workflowStep), condition); err != nil {
			return err
		}
		if workflowStep.Action != nil {
			if err := writeDryRun(writer, workflowStep.Action.Steps, indent+"   ", path); err != nil {
				return err
			}
			if len(workflowStep.Action.Finally) > 0 {
				if _, err := fmt.Fprintf(writer, "%s   finally:\n", indent); err != nil {
					return err
				}
				if err := writeDryRun(writer, workflowStep.Action.Finally, indent+"      ", path); err != nil {
					return err
				}
			}
		}
		if len(workflowStep.Defer) > 0 {
			if _, err := fmt.Fprintf(writer, "%s   defer:\n", indent); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Defer, indent+"      ", path); err != nil {
				return err
			}
		}
	}
	return nil
}

func fanoutCollectSuffix(collect string) string {
	if collect == "" {
		return ""
	}
	return "; collect " + collect
}

func writeDryRunFinally(writer io.Writer, steps []workflow.Step) error {
	if len(steps) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(writer, "finally:"); err != nil {
		return err
	}
	return writeDryRun(writer, steps, "   ", nil)
}

func dryRunCondition(workflowStep workflow.Step) string {
	if workflowStep.If == "" {
		return ""
	}
	return " if: " + string(workflowStep.If)
}

func matrixAxisNames(axes workflow.MatrixAxes) string {
	names := make([]string, len(axes))
	for i, axis := range axes {
		names[i] = axis.Name
	}
	return strings.Join(names, " × ")
}

func batchSizeLabel(size workflow.BatchSize) string {
	if size.Literal != 0 {
		return strconv.Itoa(size.Literal)
	}
	return size.Expression
}

func fanoutPolicySuffix(maxConcurrency, maxIterations int, timeout *workflow.Duration, failFast bool) string {
	parts := []string{fmt.Sprintf("max %d", maxConcurrency), fmt.Sprintf("max %d iterations", effectiveMaxIterations(maxIterations))}
	if timeout != nil {
		parts = append(parts, "timeout "+timeout.String())
	}
	if failFast {
		parts = append(parts, "fail fast")
	} else {
		parts = append(parts, "wait for all")
	}
	return " [" + strings.Join(parts, ", ") + "]"
}

func dryRunIndex(path []int) string {
	parts := make([]string, len(path))
	for i, value := range path {
		parts[i] = strconv.Itoa(value)
	}
	index := strings.Join(parts, ".")
	if len(path) == 1 {
		index += "."
	}
	return index
}

func concurrentPolicySuffix(group *workflow.ConcurrentGroup) string {
	parts := []string{fmt.Sprintf("max %d", group.MaxConcurrency)}
	if group.Timeout != nil {
		parts = append(parts, "timeout "+group.Timeout.String())
	}
	if group.FailFast {
		parts = append(parts, "fail fast")
	} else {
		parts = append(parts, "wait for all")
	}
	return " [" + strings.Join(parts, ", ") + "]"
}
