// Package sdktest exercises SDK plugins through their JSONL protocol.
//
// Start connects an sdk.Plugin to an in-memory host, performs the initialize
// handshake, and registers a clean shutdown with the test. Calls remain fully
// framed: step requests, events, cancellation notifications, callback requests,
// and responses all cross the same pipes they use in a plugin process.
//
// A service test typically starts a call, awaits readiness, interacts with the
// service, cancels the call, and then awaits its terminal response:
//
//	plugin := newPlugin()
//	host := sdktest.Start(t, plugin, sdktest.WithCallbacks(sdktest.Callbacks{
//		CallFunction: func(_ context.Context, name string, args []any) (any, error) {
//			return lookup(name, args)
//		},
//	}))
//	call := host.CallStep(sdktest.StepRequest{
//		Type:    "acme.server",
//		With:    map[string]any{"address": "127.0.0.1:0"},
//		Context: sdk.StepContext{StepID: "server"},
//	})
//	ready := call.AwaitEvent("ready")
//	var result sdk.Result
//	if err := ready.DecodeResult(&result); err != nil {
//		t.Fatal(err)
//	}
//	call.Cancel()
//	if response := call.Await(); response.Error != nil {
//		t.Fatal(response.Error)
//	}
package sdktest
