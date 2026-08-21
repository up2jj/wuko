package engine

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/up2jj/wuko/workflow"
)

func writeDryRun(writer io.Writer, steps []workflow.Step, indent string, parent []int) error {
	for i, workflowStep := range steps {
		path := append(append([]int(nil), parent...), i+1)
		index := dryRunIndex(path)
		if workflowStep.Concurrent != nil {
			if _, err := fmt.Fprintf(writer, "%s%s concurrent%s\n", indent, index, concurrentPolicySuffix(workflowStep.Concurrent)); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Concurrent.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Foreach != nil {
			if _, err := fmt.Fprintf(writer, "%s%s %s (foreach %s)%s%s\n", indent, index, workflowStep.ID, workflowStep.Foreach.Items, fanoutPolicySuffix(workflowStep.Foreach.MaxConcurrency, workflowStep.Foreach.Timeout, workflowStep.Foreach.FailFast), dryRunCondition(workflowStep)); err != nil {
				return err
			}
			if err := writeDryRun(writer, workflowStep.Foreach.Steps, indent+"   ", path); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Matrix != nil {
			if _, err := fmt.Fprintf(writer, "%s%s %s (matrix %s)%s%s\n", indent, index, workflowStep.ID, matrixAxisNames(workflowStep.Matrix.Axes), fanoutPolicySuffix(workflowStep.Matrix.MaxConcurrency, workflowStep.Matrix.Timeout, workflowStep.Matrix.FailFast), dryRunCondition(workflowStep)); err != nil {
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
		}
	}
	return nil
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

func fanoutPolicySuffix(maxConcurrency int, timeout *workflow.Duration, failFast bool) string {
	parts := []string{fmt.Sprintf("max %d", maxConcurrency)}
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
