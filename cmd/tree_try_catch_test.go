package cmd

import (
	"bytes"
	"testing"

	"github.com/up2jj/wuko/workflow"
)

func TestTreeShowsTryAndCatchPhases(t *testing.T) {
	definition := &workflow.Definition{Version: 1, Name: "recovery", Steps: []workflow.Step{{
		ID:    "deployment",
		Try:   &workflow.TryBlock{Steps: []workflow.Step{{ID: "deploy", Type: "shell"}}},
		Catch: &workflow.CatchBlock{Steps: []workflow.Step{{ID: "rollback", Type: "shell"}, {ID: "report", Type: "http"}}},
	}}}
	var output bytes.Buffer
	if err := writeWorkflowTree(&output, definition); err != nil {
		t.Fatal(err)
	}
	want := "recovery\n└── deployment (try)\n    ├── try\n    │   └── deploy (shell)\n    └── catch\n        ├── rollback (shell)\n        └── report (http)\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
