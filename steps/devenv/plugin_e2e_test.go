package devenv_test

// This file runs the end-to-end scenarios once more with the devenv executor and task served
// by a plugin, connected in process to the real plugin host, under the names a devenv plugin
// would give them. Passing here shows the package can move into a plugin without losing what
// the built-in registration provides; the assertions are the scenarios' own.

import (
	"testing"

	"github.com/up2jj/wuko/engine"
	"github.com/up2jj/wuko/executor"
	"github.com/up2jj/wuko/plugin/plugintest"
	"github.com/up2jj/wuko/plugin/sdk"
	"github.com/up2jj/wuko/plugin/stepadapter"
	"github.com/up2jj/wuko/step"
	assertstep "github.com/up2jj/wuko/steps/assert"
	devenvstep "github.com/up2jj/wuko/steps/devenv"
)

// throughPlugin switches devenvEngine and devenvDefinition to the plugin. The scenarios do not
// run in parallel, so the switch is safe to flip around them.
var throughPlugin bool

func TestDevenvScenariosThroughPlugin(t *testing.T) {
	throughPlugin = true
	t.Cleanup(func() { throughPlugin = false })
	for _, scenario := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"TaskDecodesTypedValueEndToEnd", TestDevenvTaskDecodesTypedValueEndToEnd},
		{"TaskTreatsEmptyStdoutAsNoTaskOutputs", TestDevenvTaskTreatsEmptyStdoutAsNoTaskOutputs},
		{"TaskSurvivesWrapperNoiseOnStdout", TestDevenvTaskSurvivesWrapperNoiseOnStdout},
		{"TaskDecodesTypedValueThroughSecretspecWrapper", TestDevenvTaskDecodesTypedValueThroughSecretspecWrapper},
		{"TaskReportsUndecodableOutputWithShowOutput", TestDevenvTaskReportsUndecodableOutputWithShowOutput},
		{"TaskRunsMultipleRootsUnderProfiles", TestDevenvTaskRunsMultipleRootsUnderProfiles},
		{"TaskReportsTruncatedStdout", TestDevenvTaskReportsTruncatedStdout},
		{"TaskReportsNonObjectStdout", TestDevenvTaskReportsNonObjectStdout},
		{"TaskPreservesOutputsWhenTheGraphFails", TestDevenvTaskPreservesOutputsWhenTheGraphFails},
		{"ExecutorStartsAndStopsOnlyTheProcessesItOwns", TestDevenvExecutorStartsAndStopsOnlyTheProcessesItOwns},
		{"ExecutorLeavesProcessesItDoesNotOwnRunning", TestDevenvExecutorLeavesProcessesItDoesNotOwnRunning},
	} {
		t.Run(scenario.name, scenario.run)
	}
}

// devenvTypes names the executor and task as the built-in registration or the plugin does.
func devenvTypes() (executorType, taskType string) {
	if throughPlugin {
		return "devenv.shell", "devenv.task"
	}
	return "devenv", "devenv_task"
}

func pluginDevenvEngine(t *testing.T) *engine.Engine {
	t.Helper()
	builtins := step.NewRegistry()
	if err := devenvstep.RegisterTask(builtins); err != nil {
		t.Fatal(err)
	}
	outputs := builtins.Definitions()["devenv_task"].Outputs
	plugin, err := sdk.New("devenv")
	if err != nil {
		t.Fatal(err)
	}
	if err := stepadapter.RegisterExecutor(plugin, "shell", stepadapter.ExecutorRegistration{
		Builder: devenvstep.NewExecutor, FileSystem: true, Stdin: true, TTY: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := stepadapter.Register(plugin, "task", stepadapter.Registration{
		Builder: devenvstep.NewTask, Outputs: &outputs, Executor: true,
	}); err != nil {
		t.Fatal(err)
	}
	steps := step.NewRegistry()
	if err := assertstep.Register(steps); err != nil {
		t.Fatal(err)
	}
	executors := executor.NewRegistry()
	plugintest.Install(plugintest.Connect(t, plugin), steps, executors)
	return engine.New(steps, engine.WithExecutors(executors))
}
