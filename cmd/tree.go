package cmd

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/up2jj/wuko/workflow"
)

func newTreeCmd(deps dependencies) *cobra.Command {
	var variables, environment []string
	var workflowFile string
	command := &cobra.Command{
		Use:   "tree [NAME|URL|GITHUB]",
		Short: "Display a workflow as a tree",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			cwd, home, config, err := directories(deps)
			if err != nil {
				return err
			}
			if workflowFile != "" && len(args) > 0 {
				return fmt.Errorf("workflow selector and --file cannot be used together")
			}
			if workflowFile == "" && len(args) == 0 {
				return fmt.Errorf("workflow name or --file is required")
			}
			vars, err := parseVars(variables)
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
			options := workflow.LoadOptions{Vars: vars, Env: env, BaseEnv: baseEnv, RunDir: cwd}
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
				source, err := workflow.Find(cwd, home, config, args[0])
				if err != nil {
					return err
				}
				definition, err = loader.Load(command.Context(), source.Path, options)
				if err != nil {
					return err
				}
			}

			return writeWorkflowTree(command.OutOrStdout(), definition)
		},
	}
	command.Flags().StringArrayVar(&variables, "var", nil, "set a workflow variable (key=value; repeatable)")
	command.Flags().StringArrayVar(&environment, "env", nil, "override an environment variable (KEY=value; repeatable)")
	command.Flags().StringVar(&workflowFile, "file", "", "display a workflow from a file path")
	command.ValidArgsFunction = workflowCompletion(deps)
	return command
}

func writeWorkflowTree(writer io.Writer, definition *workflow.Definition) error {
	if _, err := fmt.Fprintln(writer, definition.Name); err != nil {
		return err
	}
	return writeTreeSteps(writer, definition.Steps, "")
}

func writeTreeSteps(writer io.Writer, steps []workflow.Step, prefix string) error {
	for index, workflowStep := range steps {
		last := index == len(steps)-1
		branch := "├── "
		childPrefix := prefix + "│   "
		if last {
			branch = "└── "
			childPrefix = prefix + "    "
		}
		if workflowStep.Concurrent != nil {
			if _, err := fmt.Fprintf(writer, "%s%sconcurrent%s\n", prefix, branch, treeConcurrentPolicy(workflowStep.Concurrent)); err != nil {
				return err
			}
			if err := writeTreeSteps(writer, workflowStep.Concurrent.Steps, childPrefix); err != nil {
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
			if err := writeTreeSteps(writer, workflowStep.Action.Steps, childPrefix); err != nil {
				return err
			}
		}
	}
	return nil
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
