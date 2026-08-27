package cmd

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	controlpkg "github.com/up2jj/wuko/control"
	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/workflow"
)

func newTreeCmd(deps dependencies) *cobra.Command {
	var variables, variableFiles, environment []string
	var workflowFile string
	command := &cobra.Command{
		Use:   "tree [NAME|URL|GITHUB] [TARGET]",
		Short: "Display a workflow as a tree",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			reporter := diagnosticsFor(command, deps, cwd)
			diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseInvocation, Status: diagnostic.StatusStarted, Message: "render workflow tree", Attributes: []diagnostic.Attribute{diagnostic.Attr("run_dir", cwd)}})
			if workflowFile != "" && len(args) > 1 {
				return fmt.Errorf("workflow selector and --file cannot be used together")
			}
			if workflowFile == "" && len(args) == 0 {
				return fmt.Errorf("workflow name or --file is required")
			}
			vars, err := parseVars(command.Context(), cwd, variableFiles, variables)
			if err != nil {
				return err
			}
			env, err := parseEnv(environment)
			if err != nil {
				return err
			}
			baseEnv, err := invocationEnvironment(command, deps, cwd)
			if err != nil {
				return err
			}

			loader := deps.loader
			if loader == nil {
				loader = workflow.NewLoader(nil)
			}
			options := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd, Diagnostics: reporter}
			if workflowFile != "" {
				if len(args) == 1 {
					options.Target = args[0]
				}
			} else if len(args) == 2 {
				options.Target = args[1]
			}
			var definition *workflow.Definition
			if workflowFile != "" {
				path, err := filepath.Abs(workflowFile)
				if err != nil {
					return fmt.Errorf("resolving workflow file %s: %w", workflowFile, err)
				}
				definition, err = loader.Load(command.Context(), path, options)
				if err != nil {
					return err
				}
			} else if workflow.IsRemoteLocator(args[0]) {
				var cleanup func()
				definition, cleanup, err = loader.LoadRemote(command.Context(), args[0], options)
				if err != nil {
					return err
				}
				defer cleanup()
			} else {
				discoveryStarted := time.Now()
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusStarted, Time: discoveryStarted, Message: args[0]})
				source, err := workflow.Find(cwd, home, config, args[0])
				if err != nil {
					diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusFailed, Duration: time.Since(discoveryStarted), Error: err})
					return err
				}
				diagnostic.Emit(reporter, diagnostic.Event{Phase: diagnostic.PhaseDiscovery, Status: diagnostic.StatusSucceeded, Duration: time.Since(discoveryStarted), Location: diagnostic.Location{Source: source.Path}, Message: source.Name})
				definition, err = loader.Load(command.Context(), source.Path, options)
				if err != nil {
					return err
				}
			}

			plan, err := resolveDependencyPlan(command.Context(), definition, loader, options, cwd, home, config)
			if err != nil {
				return err
			}
			return writeDependencyPlanTree(command.OutOrStdout(), plan)
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&variableFiles, "var-file", nil, "import workflow variables from a JSON or TOML file (repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().StringVar(&workflowFile, "file", "", "display a workflow from a file path")
	command.ValidArgsFunction = workflowCompletion(deps, false)
	return command
}

func writeDependencyPlanTree(writer io.Writer, plan *workflow.DependencyPlan) error {
	if plan == nil || plan.Root == nil {
		return fmt.Errorf("dependency plan root is required")
	}
	if _, err := fmt.Fprintln(writer, plan.Root.Definition.Name); err != nil {
		return err
	}
	return writeDependencyWorkflowContents(writer, plan.Root, "", make(map[*workflow.DependencyNode]bool))
}

func writeDependencyWorkflowContents(writer io.Writer, node *workflow.DependencyNode, prefix string, expanded map[*workflow.DependencyNode]bool) error {
	expanded[node] = true
	hasWorkflowPlan := len(node.Definition.Steps) > 0 || len(node.Definition.Finally) > 0
	if len(node.Dependencies) > 0 {
		branch := "├── "
		childPrefix := prefix + "│   "
		if !hasWorkflowPlan {
			branch = "└── "
			childPrefix = prefix + "    "
		}
		if _, err := fmt.Fprintf(writer, "%s%sdepends_on\n", prefix, branch); err != nil {
			return err
		}
		aliases := slices.Sorted(maps.Keys(node.Dependencies))
		for index, alias := range aliases {
			dependency := node.Dependencies[alias]
			last := index == len(aliases)-1
			if err := writeDependencyWorkflowNode(writer, dependency, alias, childPrefix, last, expanded); err != nil {
				return err
			}
		}
	}
	if hasWorkflowPlan {
		return writeTreePlan(writer, node.Definition.Steps, node.Definition.Finally, prefix)
	}
	return nil
}

func writeDependencyWorkflowNode(writer io.Writer, node *workflow.DependencyNode, alias, prefix string, last bool, expanded map[*workflow.DependencyNode]bool) error {
	branch := "├── "
	childPrefix := prefix + "│   "
	if last {
		branch = "└── "
		childPrefix = prefix + "    "
	}
	label := alias
	if alias != node.Definition.Name {
		label += " (" + node.Definition.Name + ")"
	}
	if expanded[node] {
		_, err := fmt.Fprintf(writer, "%s%s%s (shared; shown above)\n", prefix, branch, label)
		return err
	}
	if _, err := fmt.Fprintf(writer, "%s%s%s\n", prefix, branch, label); err != nil {
		return err
	}
	return writeDependencyWorkflowContents(writer, node, childPrefix, expanded)
}

func writeWorkflowTree(writer io.Writer, definition *workflow.Definition) error {
	if _, err := fmt.Fprintln(writer, definition.Name); err != nil {
		return err
	}
	return writeTreePlan(writer, definition.Steps, definition.Finally, "")
}

func writeTreePlan(writer io.Writer, steps, finally []workflow.Step, prefix string) error {
	return writeTreePlanWithFollowing(writer, steps, finally, prefix, false)
}

func writeTreePlanWithFollowing(writer io.Writer, steps, finally []workflow.Step, prefix string, hasFollowing bool) error {
	if len(finally) == 0 {
		return writeTreeStepsWithFollowing(writer, steps, prefix, hasFollowing)
	}
	if err := writeTreeStepsWithFollowing(writer, steps, prefix, true); err != nil {
		return err
	}
	branch := "└── "
	childPrefix := prefix + "    "
	if hasFollowing {
		branch = "├── "
		childPrefix = prefix + "│   "
	}
	if _, err := fmt.Fprintf(writer, "%s%sfinally\n", prefix, branch); err != nil {
		return err
	}
	return writeTreeSteps(writer, finally, childPrefix)
}

func writeTreeSteps(writer io.Writer, steps []workflow.Step, prefix string) error {
	return writeTreeStepsWithFollowing(writer, steps, prefix, false)
}

func writeTreeStepsWithFollowing(writer io.Writer, steps []workflow.Step, prefix string, hasFollowing bool) error {
	for index, workflowStep := range steps {
		last := index == len(steps)-1 && !hasFollowing
		branch := "├── "
		childPrefix := prefix + "│   "
		if last {
			branch = "└── "
			childPrefix = prefix + "    "
		}
		if workflowStep.IsCancelOn() {
			collect := ""
			if workflowStep.CancelOn.Collect != "" {
				collect = " collect " + workflowStep.CancelOn.Collect
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s (cancel_on%s)%s\n", prefix, branch, workflowStep.ID, collect, treeCondition(workflowStep)); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "%s├── monitors\n", childPrefix); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.CancelOn.Monitors, childPrefix+"│   "); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "%s└── steps\n", childPrefix); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.CancelOn.Steps, childPrefix+"    "); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsExecutorBlock() {
			label := fmt.Sprintf("executor: %s", workflowStep.Executor.Type)
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (executor: %s)", workflowStep.ID, workflowStep.Executor.Type)
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s\n", prefix, branch, label); err != nil {
				return err
			}
			if len(workflowStep.Finally) == 0 {
				if err := writeTreeSteps(writer, workflowStep.Steps, childPrefix); err != nil {
					return err
				}
				continue
			}
			if err := writeTreeStepsWithFollowing(writer, workflowStep.Steps, childPrefix, true); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(writer, "%s└── finally\n", childPrefix); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Finally, childPrefix+"    "); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsWorkingDirectoryBlock() {
			label := "working_directory: " + workflowStep.WorkingDirectory
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (working_directory: %s)", workflowStep.ID, workflowStep.WorkingDirectory)
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s\n", prefix, branch, label); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsWorktreeBlock() {
			publish := ""
			if workflowStep.Worktree.Publish != nil {
				publish = " publish " + workflowStep.Worktree.Publish.Branch
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s (worktree %s%s)\n", prefix, branch, workflowStep.ID, workflowStep.Worktree.Revision, publish); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Worktree.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.IsConditionalBlock() {
			label := "if: " + string(workflowStep.If)
			if workflowStep.ID != "" {
				label = fmt.Sprintf("%s (if: %s)", workflowStep.ID, workflowStep.If)
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s\n", prefix, branch, label); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Concurrent != nil {
			label := "concurrent" + treeConcurrentPolicy(workflowStep.Concurrent)
			if workflowStep.ID != "" {
				label = workflowStep.ID + " (concurrent" + treeConcurrentPolicy(workflowStep.Concurrent) + ")"
			}
			if _, err := fmt.Fprintf(writer, "%s%s%s\n", prefix, branch, label); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Concurrent.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Return != nil {
			names := strings.Join(slices.Sorted(maps.Keys(workflowStep.Return.Outputs)), ", ")
			if names == "" {
				names = "{}"
			}
			if _, err := fmt.Fprintf(writer, "%s%sreturn (outputs: %s)%s\n", prefix, branch, names, treeCondition(workflowStep)); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Batch != nil {
			condition := treeCondition(workflowStep)
			if _, err := fmt.Fprintf(writer, "%s%s%s (batch %s by %s%s)%s%s\n", prefix, branch, workflowStep.ID, workflowStep.Batch.Items, treeBatchSize(workflowStep.Batch.Size), treeFanoutCollectSuffix(workflowStep.Batch.Collect), treeFanoutPolicy(workflowStep.Batch.MaxConcurrency, workflowStep.Batch.MaxIterations, workflowStep.Batch.Timeout, workflowStep.Batch.FailFast), condition); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Batch.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Foreach != nil {
			condition := treeCondition(workflowStep)
			if _, err := fmt.Fprintf(writer, "%s%s%s (foreach %s%s)%s%s\n", prefix, branch, workflowStep.ID, workflowStep.Foreach.Items, treeFanoutCollectSuffix(workflowStep.Foreach.Collect), treeFanoutPolicy(workflowStep.Foreach.MaxConcurrency, workflowStep.Foreach.MaxIterations, workflowStep.Foreach.Timeout, workflowStep.Foreach.FailFast), condition); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Foreach.Steps, childPrefix); err != nil {
				return err
			}
			continue
		}
		if workflowStep.Matrix != nil {
			condition := treeCondition(workflowStep)
			if _, err := fmt.Fprintf(writer, "%s%s%s (matrix %s%s)%s%s\n", prefix, branch, workflowStep.ID, treeMatrixAxes(workflowStep.Matrix.Axes), treeFanoutCollectSuffix(workflowStep.Matrix.Collect), treeFanoutPolicy(workflowStep.Matrix.MaxConcurrency, workflowStep.Matrix.MaxIterations, workflowStep.Matrix.Timeout, workflowStep.Matrix.FailFast), condition); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Matrix.Steps, childPrefix); err != nil {
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
		if _, err := fmt.Fprintf(writer, "%s%s%s (%s)%s%s\n", prefix, branch, workflowStep.ID, kind, treeExecutionPolicy(workflowStep), condition); err != nil {
			return err
		}
		if workflowStep.Action != nil {
			if err := writeTreePlanWithFollowing(writer, workflowStep.Action.Steps, workflowStep.Action.Finally, childPrefix, len(workflowStep.Defer) > 0); err != nil {
				return err
			}
		}
		if len(workflowStep.Defer) > 0 {
			if _, err := fmt.Fprintf(writer, "%s└── defer\n", childPrefix); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Defer, childPrefix+"    "); err != nil {
				return err
			}
		}
	}
	return nil
}

func treeFanoutCollectSuffix(collect string) string {
	if collect == "" {
		return ""
	}
	return "; collect " + collect
}

func treeCondition(workflowStep workflow.Step) string {
	if workflowStep.If == "" {
		return ""
	}
	return " if: " + string(workflowStep.If)
}

func treeMatrixAxes(axes workflow.MatrixAxes) string {
	names := make([]string, len(axes))
	for i, axis := range axes {
		names[i] = axis.Name
	}
	return strings.Join(names, " × ")
}

func treeBatchSize(size workflow.BatchSize) string {
	if size.Literal != 0 {
		return fmt.Sprint(size.Literal)
	}
	return size.Expression
}

func treeFanoutPolicy(maxConcurrency, maxIterations int, timeout *workflow.Duration, failFast bool) string {
	if maxIterations == 0 {
		maxIterations = controlpkg.DefaultMaxIterations
	}
	parts := []string{fmt.Sprintf("max %d", maxConcurrency), fmt.Sprintf("max %d iterations", maxIterations)}
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

func treeConcurrentPolicy(group *workflow.ConcurrentGroup) string {
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

func treeExecutionPolicy(workflowStep workflow.Step) string {
	var parts []string
	if workflowStep.Timeout != nil {
		parts = append(parts, "timeout "+workflowStep.Timeout.String())
	}
	if workflowStep.Retry != nil {
		retry := fmt.Sprintf("%d attempts", workflowStep.Retry.MaxAttempts)
		if workflowStep.Retry.MaxElapsedTime.Value() > 0 {
			retry += " within " + workflowStep.Retry.MaxElapsedTime.String()
		}
		parts = append(parts, retry)
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, ", ") + "]"
}
