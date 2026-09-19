package multiplexer

import (
	"context"
	"encoding/json"
	"fmt"
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

func (adapter herdrAdapter) Execute(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
	if request.Scope == ScopeTab {
		return adapter.executeTab(ctx, target, request, environment)
	}
	id := targetID(target, request.Target)
	var args []string
	switch request.Operation {
	case OperationTitle:
		if err := herdrOperandSafe(request.Title, herdrPaneRenameFlags); err != nil {
			return Outcome{}, err
		}
		args = []string{"pane", "rename", id, request.Title}
	case OperationClearTitle:
		args = []string{"pane", "rename", id, "--clear"}
	case OperationZoom:
		args = []string{"pane", "zoom", id, "--" + request.Mode}
	case OperationNotify:
		if request.Target != "" {
			return Outcome{}, &UnsupportedError{Provider: ProviderHerdr, Operation: request.Operation, Detail: "herdr notifications cannot target a pane"}
		}
		if err := herdrOperandSafe(request.Title, herdrNotificationShowFlags); err != nil {
			return Outcome{}, err
		}
		args = []string{"notification", "show", request.Title}
		if request.Body != "" {
			args = append(args, "--body", request.Body)
		}
	case OperationMetadata:
		target.ID = id
		args = metadataArgs(target, request)
	case OperationRead:
		args = []string{"pane", "read", id, "--source", "visible", "--format", "text"}
		if request.Scrollback {
			args = []string{"pane", "read", id, "--source", "recent", "--lines", strconv.Itoa(request.Lines), "--format", "text"}
		}
		result, err := runReadCommand(ctx, adapter.executor, environment, "herdr", args...)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Text: limitedLines(result.Stdout, request)}, nil
	case OperationSendText:
		args = []string{"pane", "send-text", id, request.Text}
	case OperationSendKeys:
		keys, err := herdrKeys(request.Keys)
		if err != nil {
			return Outcome{}, err
		}
		args = append([]string{"pane", "send-keys", id}, keys...)
	case OperationSplit:
		result, err := runCommand(ctx, adapter.executor, environment, "herdr", "pane", "split", id, "--direction", request.Direction, "--focus")
		if err != nil {
			return Outcome{}, err
		}
		created, err := herdrField(result.Stdout, "pane", "pane_id")
		if err != nil {
			return Outcome{}, err
		}
		if created == "" {
			return Outcome{}, fmt.Errorf("herdr split returned no pane id")
		}
		return Outcome{CreatedTarget: created, CreatedPane: created}, nil
	case OperationResize:
		args = []string{"pane", "resize", "--direction", request.Direction, "--pane", id}
		if request.Amount != nil {
			args = append(args, "--amount", amountArgument(request.Amount))
		}
	case OperationFocusDirection:
		args = []string{"pane", "focus", "--direction", request.Direction, "--pane", id}
	case OperationClosePane:
		args = []string{"pane", "close", id}
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderHerdr, Operation: request.Operation}
	}
	_, err := runCommand(ctx, adapter.executor, environment, "herdr", args...)
	return Outcome{}, err
}

// executeTab labels the tab owning the detected pane. herdr's tab rename has no
// --clear counterpart, so the previous label is reported back and a later step
// restores it with an ordinary title operation.
func (adapter herdrAdapter) executeTab(ctx context.Context, target Target, request Request, environment map[string]string) (Outcome, error) {
	switch request.Operation {
	case OperationTitle, OperationClearTitle:
	default:
		return Outcome{}, &UnsupportedError{Provider: ProviderHerdr, Operation: request.Operation, Detail: "herdr supports only title and clear_title for tab scope"}
	}
	title := request.Title
	if request.Operation == OperationClearTitle {
		title = ""
	}
	if err := herdrOperandSafe(title, herdrTabRenameFlags); err != nil {
		return Outcome{}, err
	}
	target.ID = targetID(target, request.Target)
	tab, err := adapter.tabID(ctx, target, environment, request.Target == "")
	if err != nil {
		return Outcome{}, err
	}
	previous, err := adapter.tabLabel(ctx, tab, environment)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := runCommand(ctx, adapter.executor, environment, "herdr", "tab", "rename", tab, title); err != nil {
		return Outcome{}, err
	}
	return Outcome{PreviousTitle: previous}, nil
}

func herdrKeys(values []string) ([]string, error) {
	keys := make([]string, 0, len(values))
	for _, value := range values {
		key, err := NormalizeKey(value)
		if err != nil {
			return nil, err
		}
		if key == "escape" {
			key = "esc"
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (adapter herdrAdapter) tabID(ctx context.Context, target Target, environment map[string]string, allowEnvironment bool) (string, error) {
	if id := strings.TrimSpace(environment["HERDR_TAB_ID"]); allowEnvironment && id != "" {
		return id, nil
	}
	result, err := runCommand(ctx, adapter.executor, environment, "herdr", "pane", "get", target.ID)
	if err != nil {
		return "", err
	}
	id, err := herdrField(result.Stdout, "pane", "tab_id")
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("herdr pane %s reported no tab", target.ID)
	}
	return id, nil
}

func (adapter herdrAdapter) tabLabel(ctx context.Context, tab string, environment map[string]string) (string, error) {
	result, err := runCommand(ctx, adapter.executor, environment, "herdr", "tab", "get", tab)
	if err != nil {
		return "", err
	}
	return herdrField(result.Stdout, "tab", "label")
}

// herdrField reads one string field out of a herdr CLI envelope of the shape
// {"result": {"<object>": {"<field>": "..."}}}. A missing field is not an
// error: an unlabeled tab simply has none.
func herdrField(payload, object, field string) (string, error) {
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return "", fmt.Errorf("parsing herdr response: %w", err)
	}
	// The result envelope mixes the requested object with scalar metadata such
	// as "type", so reach for the object explicitly rather than assuming shape.
	nested, ok := envelope.Result[object].(map[string]any)
	if !ok {
		return "", fmt.Errorf("herdr response has no %s object", object)
	}
	value, ok := nested[field]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("herdr %s.%s is not a string", object, field)
	}
	return text, nil
}

// herdr does not honor the -- end-of-options separator: `pane rename` folds it
// into its variadic LABEL, and `notification show` reports it as an unknown
// option. Display text therefore has to travel as a bare operand. herdr only
// intercepts text that exactly matches one of the subcommand's own flags -
// other dash-leading text such as "-x rebuild" is kept verbatim - so reject
// just those tokens rather than silently performing a different operation.
var (
	herdrPaneRenameFlags       = []string{"--clear"}
	herdrTabRenameFlags        = []string{}
	herdrNotificationShowFlags = []string{"--body", "--position", "--sound"}
)

func herdrOperandSafe(text string, flags []string) error {
	if slices.Contains(flags, text) {
		return fmt.Errorf("herdr cannot display the text %q because it collides with a herdr option", text)
	}
	return nil
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
