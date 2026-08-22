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
		if workflowStep.IsExecutorBlock() {
			if _, err := fmt.Fprintf(writer, "%s%s executor: %s\n", indent, index, workflowStep.Executor.Type); err != nil {
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
			if _, err := fmt.Fprintf(writer, "%s%s working_directory: %s\n", indent, index, workflowStep.WorkingDirectory); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			if _, err := fmt.Fprintf(writer, "%s%s if: %s\n", indent, index, workflowStep.If); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			if _, err := fmt.Fprintf(writer, "%s%s concurrent%s\n", indent, index, concurrentPolicySuffix(workflowStep.Concurrent)); err != nil {
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
