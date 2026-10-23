package cmd

import (
	"fmt"
	"io"

	"github.com/up2jj/wuko/workflow"
)

const noValidWorkflowsHelp = "No valid workflows found. Run 'wuko validate' to inspect invalid workflows.\n"

func writeDiscoveryWarnings(writer io.Writer, warnings []workflow.DiscoveryWarning) error {
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(writer, "Warning: skipping invalid workflow %s: %v\n", warning.Source.Path, warning.Err); err != nil {
			return err
		}
	}
	return nil
}

func discoveryWarningNotice(warnings []workflow.DiscoveryWarning) string {
	if len(warnings) == 0 {
		return ""
	}
	if len(warnings) == 1 {
		return "Skipped 1 invalid workflow; run 'wuko validate' for details"
	}
	return fmt.Sprintf("Skipped %d invalid workflows; run 'wuko validate' for details", len(warnings))
}
