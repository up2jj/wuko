package multiplexer

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
)

type herdrAdapter struct{ executor commandExecutor }

func (herdrAdapter) Provider() Provider { return ProviderHerdr }

func (herdrAdapter) Detect(environment map[string]string) (Target, bool) {
	id := strings.TrimSpace(environment["HERDR_PANE_ID"])
	if id == "" {
		return Target{}, false
	}
	return Target{Provider: ProviderHerdr, ID: id, Workspace: environment["HERDR_WORKSPACE_ID"]}, true
}

func (adapter herdrAdapter) Execute(ctx context.Context, target Target, request Request, environment map[string]string) error {
	var args []string
	switch request.Operation {
	case OperationTitle:
		args = []string{"pane", "rename", target.ID, "--", request.Title}
	case OperationClearTitle:
		args = []string{"pane", "rename", target.ID, "--clear"}
	case OperationZoom:
		args = []string{"pane", "zoom", target.ID, "--" + request.Mode}
	case OperationNotify:
		// herdr's notification show rejects the -- separator and misparses an
		// option that precedes its TITLE operand, so the title leads and the
		// body follows. The operand position already protects flag-shaped text.
		args = []string{"notification", "show", request.Title}
		if request.Body != "" {
			args = append(args, "--body", request.Body)
		}
	case OperationMetadata:
		args = metadataArgs(target, request)
	default:
		return &UnsupportedError{Provider: ProviderHerdr, Operation: request.Operation}
	}
	_, err := runCommand(ctx, adapter.executor, environment, "herdr", args...)
	return err
}

func metadataArgs(target Target, request Request) []string {
	args := []string{"pane", "report-metadata", target.ID, "--source", request.Source}
	if request.Title != "" {
		args = append(args, "--title", request.Title)
	}
	if request.ClearTitle {
		args = append(args, "--clear-title")
	}
	if request.DisplayAgent != "" {
		args = append(args, "--display-agent", request.DisplayAgent)
	}
	if request.ClearDisplayAgent {
		args = append(args, "--clear-display-agent")
	}
	for _, status := range slices.Sorted(maps.Keys(request.StateLabels)) {
		label := request.StateLabels[status]
		args = append(args, "--state-label", status+"="+label)
	}
	if request.ClearStateLabels {
		args = append(args, "--clear-state-labels")
	}
	for _, name := range slices.Sorted(maps.Keys(request.Tokens)) {
		value := request.Tokens[name]
		args = append(args, "--token", name+"="+value)
	}
	for _, name := range request.ClearTokens {
		args = append(args, "--clear-token", name)
	}
	if request.TTLMilliseconds > 0 {
		args = append(args, "--ttl-ms", strconv.Itoa(request.TTLMilliseconds))
	}
	return args
}
