package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/up2jj/wuko/localresource"
	"github.com/up2jj/wuko/process"
)

func runWithResourceLimits(command *cobra.Command, deps dependencies, args []string, config runWorkflowConfig) (bool, error) {
	if !command.Flags().Changed("cpus") && !command.Flags().Changed("memory") {
		return false, nil
	}
	if command.Flags().Changed("cpus") && config.cpus == "" || command.Flags().Changed("memory") && config.memory == "" {
		return true, fmt.Errorf("--cpus and --memory must be non-empty when supplied")
	}
	limits, err := localresource.Parse(config.cpus, config.memory)
	if err != nil {
		return true, err
	}
	if config.dryRun {
		return false, nil
	}
	executable, err := deps.executable()
	if err != nil {
		return true, fmt.Errorf("locating Wuko for resource-limited execution: %w", err)
	}
	cwd, err := deps.cwd()
	if err != nil {
		return true, fmt.Errorf("resolving resource-limited run directory: %w", err)
	}
	options := process.Options{
		Command: executable, Args: resourceWorkerArgs(command, args), Dir: cwd,
		Env:   processEnvironment(),
		Stdin: command.InOrStdin(), Stdout: command.OutOrStdout(), Stderr: command.ErrOrStderr(),
		StdoutPolicy: process.OutputInherit, StderrPolicy: process.OutputInherit,
	}
	// A PTY preserves interactive prompts and terminal input across the supervisor.
	// Machine-readable stdout must retain its separate stream.
	options.TTY = deps.isInteractive(options.Stdin) && config.reportJSON != "-" && config.workflowFile != "-"
	run := deps.resourceRunner
	if run == nil {
		run = localresource.Run
	}
	_, err = run(command.Context(), limits, options)
	return true, err
}

func resourceWorkerArgs(command *cobra.Command, args []string) []string {
	worker := []string{"run"}
	command.Flags().Visit(func(flag *pflag.Flag) {
		if flag.Name == "cpus" || flag.Name == "memory" {
			return
		}
		if flag.Value.Type() == "stringArray" {
			for _, value := range flag.Value.(pflag.SliceValue).GetSlice() {
				worker = append(worker, "--"+flag.Name+"="+value)
			}
			return
		}
		worker = append(worker, "--"+flag.Name+"="+flag.Value.String())
	})
	return append(append(worker, "--"), args...)
}
