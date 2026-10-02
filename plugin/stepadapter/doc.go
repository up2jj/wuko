// Package stepadapter exposes Wuko-native step runners through the executable
// plugin SDK. Unlike plugin/sdk, this package deliberately depends on Wuko's
// step contracts and their transitive dependencies.
//
// Register a managed service by supplying its native builder and declaring
// whether successful results require cleanup:
//
//	err := stepadapter.RegisterService(plugin, "server", stepadapter.ServiceRegistration{
//		Builder: server.New,
//		Cleanup: true,
//	}, sdk.WithHostCallbacks(sdk.HostTemplateRender))
package stepadapter
