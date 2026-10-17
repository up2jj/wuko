// Package stepadapter exposes Wuko-native step runners and executors through the
// executable plugin SDK. Unlike plugin/sdk, this package deliberately depends on
// Wuko's step and executor contracts and their transitive dependencies, so code
// written for the built-in registries can be served by a plugin unchanged.
//
// Register exposes a step, RegisterService a managed service, and RegisterExecutor
// an executor whose sessions stay in the plugin. A step registered with Executor
// set receives the native session of the block it runs in, with every capability
// the session has:
//
//	err := stepadapter.RegisterExecutor(plugin, "shell", stepadapter.ExecutorRegistration{
//		Builder:    devenv.NewExecutor,
//		FileSystem: true, Stdin: true, TTY: true,
//	})
//	err = stepadapter.Register(plugin, "task", stepadapter.Registration{
//		Builder: devenv.NewTask, Outputs: &taskOutputs, Executor: true,
//	})
//
// A managed service is registered with its native builder, declaring whether
// successful results require cleanup:
//
//	err := stepadapter.RegisterService(plugin, "server", stepadapter.ServiceRegistration{
//		Builder: server.New,
//		Cleanup: true,
//	}, sdk.WithHostCallbacks(sdk.HostTemplateRender))
package stepadapter
