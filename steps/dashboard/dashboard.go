// Package dashboard implements an interactive, refreshable terminal dashboard step.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

const defaultMaxConcurrency = 4

type Config struct {
	Title          string  `yaml:"title,omitempty"`
	MaxConcurrency int     `yaml:"max_concurrency,omitempty"`
	Groups         []Group `yaml:"groups"`
}

type Group struct {
	ID      string    `yaml:"id"`
	Title   string    `yaml:"title,omitempty"`
	Columns int       `yaml:"columns,omitempty"`
	Refresh *Callback `yaml:"refresh,omitempty"`
	Widgets []Widget  `yaml:"widgets"`
}

type Callback struct {
	Every   *workflow.Duration `yaml:"every,omitempty"`
	Steps   []workflow.Step    `yaml:"steps"`
	Finally []workflow.Step    `yaml:"finally,omitempty"`
	Outputs map[string]string  `yaml:"outputs,omitempty"`
}

type Widget struct {
	ID       string   `yaml:"id"`
	Title    string   `yaml:"title,omitempty"`
	Template string   `yaml:"template"`
	Actions  []Action `yaml:"actions,omitempty"`
}

type Action struct {
	ID            string            `yaml:"id"`
	Label         string            `yaml:"label,omitempty"`
	Confirm       string            `yaml:"confirm,omitempty"`
	Steps         []workflow.Step   `yaml:"steps"`
	Finally       []workflow.Step   `yaml:"finally,omitempty"`
	Outputs       map[string]string `yaml:"outputs,omitempty"`
	RefreshGroups []string          `yaml:"refresh_groups,omitempty"`
}

type dashboardView func(context.Context, io.Reader, io.Writer, tui.DashboardConfig) (tui.DashboardResult, error)

type Runner struct {
	config Config
	show   dashboardView
}

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("dashboard", step.Registration{
		Builder: New,
		Config:  Config{},
		Outputs: step.ClosedObject(map[string]step.OutputSchema{"groups": step.OpenObject()}),
	})
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Title) == "" {
		config.Title = "Dashboard"
	}
	if step.ConfigKnown(raw, "max_concurrency") && config.MaxConcurrency == 0 {
		config.MaxConcurrency = defaultMaxConcurrency
	}
	if step.ConfigKnown(raw, "max_concurrency") && config.MaxConcurrency < 1 {
		return nil, fmt.Errorf("max_concurrency must be positive")
	}
	if step.ConfigKnown(raw, "groups") && len(config.Groups) == 0 {
		return nil, fmt.Errorf("at least one group is required")
	}
	groups := make(map[string]struct{}, len(config.Groups))
	for groupIndex := range config.Groups {
		group := &config.Groups[groupIndex]
		group.ID = strings.TrimSpace(group.ID)
		if err := validateIdentifier("group", groupIndex, group.ID, groups); err != nil {
			return nil, err
		}
		if strings.TrimSpace(group.Title) == "" {
			group.Title = group.ID
		}
		if group.Columns == 0 {
			group.Columns = 2
		}
		if group.Columns < 1 {
			return nil, fmt.Errorf("group %q columns must be positive", group.ID)
		}
		if len(group.Widgets) == 0 {
			return nil, fmt.Errorf("group %q requires at least one widget", group.ID)
		}
		if group.Refresh != nil {
			if len(group.Refresh.Steps) == 0 {
				return nil, fmt.Errorf("group %q refresh steps are required", group.ID)
			}
			if group.Refresh.Every != nil && group.Refresh.Every.Value() <= 0 {
				return nil, fmt.Errorf("group %q refresh every must be positive", group.ID)
			}
		}
		widgets := make(map[string]struct{}, len(group.Widgets))
		for widgetIndex := range group.Widgets {
			widget := &group.Widgets[widgetIndex]
			widget.ID = strings.TrimSpace(widget.ID)
			if err := validateIdentifier("widget", widgetIndex, widget.ID, widgets); err != nil {
				return nil, fmt.Errorf("group %q: %w", group.ID, err)
			}
			if strings.TrimSpace(widget.Title) == "" {
				widget.Title = widget.ID
			}
			if strings.TrimSpace(widget.Template) == "" {
				return nil, fmt.Errorf("group %q widget %q template is required", group.ID, widget.ID)
			}
			actions := make(map[string]struct{}, len(widget.Actions))
			for actionIndex := range widget.Actions {
				action := &widget.Actions[actionIndex]
				action.ID = strings.TrimSpace(action.ID)
				if err := validateIdentifier("action", actionIndex, action.ID, actions); err != nil {
					return nil, fmt.Errorf("group %q widget %q: %w", group.ID, widget.ID, err)
				}
				if strings.TrimSpace(action.Label) == "" {
					action.Label = action.ID
				}
				if len(action.Steps) == 0 {
					return nil, fmt.Errorf("group %q widget %q action %q steps are required", group.ID, widget.ID, action.ID)
				}
			}
		}
	}
	for _, group := range config.Groups {
		for _, widget := range group.Widgets {
			for _, action := range widget.Actions {
				for _, refreshGroup := range action.RefreshGroups {
					if _, exists := groups[refreshGroup]; !exists {
						return nil, fmt.Errorf("group %q widget %q action %q references unknown refresh group %q", group.ID, widget.ID, action.ID, refreshGroup)
					}
				}
			}
		}
	}
	return &Runner{config: config, show: tui.Dashboard}, nil
}

func validateIdentifier(kind string, index int, id string, seen map[string]struct{}) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%s %d id is required", kind, index+1)
	}
	if _, exists := seen[id]; exists {
		return fmt.Errorf("duplicate %s id %q", kind, id)
	}
	seen[id] = struct{}{}
	return nil
}

func (*Runner) InlineWorkflowAware() {}

func (runner *Runner) Validate(ctx context.Context, request step.Request) error {
	if request.InlineWorkflows == nil {
		return fmt.Errorf("inline workflow execution is unavailable")
	}
	renderer, ok := request.TemplateRenderer.(step.DataTemplateRenderer)
	if !ok {
		return fmt.Errorf("dashboard template rendering is unavailable")
	}
	for _, group := range runner.config.Groups {
		for _, widget := range group.Widgets {
			if err := renderer.ValidateContent(widget.Template); err != nil {
				return fmt.Errorf("group %q widget %q template: %w", group.ID, widget.ID, err)
			}
			for _, action := range widget.Actions {
				if action.Confirm != "" {
					if err := renderer.ValidateContent(action.Confirm); err != nil {
						return fmt.Errorf("group %q widget %q action %q confirmation: %w", group.ID, widget.ID, action.ID, err)
					}
				}
				if err := validateCallbackSteps(action.Steps); err != nil {
					return fmt.Errorf("group %q widget %q action %q: %w", group.ID, widget.ID, action.ID, err)
				}
				if err := validateCallbackSteps(action.Finally); err != nil {
					return fmt.Errorf("group %q widget %q action %q finally: %w", group.ID, widget.ID, action.ID, err)
				}
				if err := request.InlineWorkflows.ValidateInlineWorkflow(ctx, actionCall(group, widget, action, outputPlaceholders(group))); err != nil {
					return fmt.Errorf("group %q widget %q action %q: %w", group.ID, widget.ID, action.ID, err)
				}
			}
		}
		if group.Refresh == nil {
			continue
		}
		if err := validateCallbackSteps(group.Refresh.Steps); err != nil {
			return fmt.Errorf("group %q refresh: %w", group.ID, err)
		}
		if err := validateCallbackSteps(group.Refresh.Finally); err != nil {
			return fmt.Errorf("group %q refresh finally: %w", group.ID, err)
		}
		if err := request.InlineWorkflows.ValidateInlineWorkflow(ctx, refreshCall(group)); err != nil {
			return fmt.Errorf("group %q refresh: %w", group.ID, err)
		}
	}
	return nil
}

func validateCallbackSteps(steps []workflow.Step) error {
	for _, declaration := range steps {
		if declaration.Return != nil {
			return fmt.Errorf("return steps are not allowed in dashboard callbacks")
		}
		if declaration.Type == "dashboard" {
			return fmt.Errorf("nested dashboard steps are not allowed")
		}
		for _, sequence := range declaration.ChildSequences() {
			if err := validateCallbackSteps(sequence.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

func (runner *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if !request.Interactive {
		return step.Result{}, fmt.Errorf("dashboard requires an interactive terminal")
	}
	if request.InlineWorkflows == nil {
		return step.Result{}, fmt.Errorf("inline workflow execution is unavailable")
	}
	renderer, ok := request.TemplateRenderer.(step.DataTemplateRenderer)
	if !ok {
		return step.Result{}, fmt.Errorf("dashboard template rendering is unavailable")
	}
	if request.Stdin == nil || request.Stdout == nil {
		return step.Result{}, fmt.Errorf("dashboard terminal input and output are required")
	}
	snapshot := renderer.Snapshot()
	config := runner.tuiConfig(snapshot, request.InlineWorkflows)
	result, err := runner.show(ctx, request.Stdin, request.Stdout, config)
	if err != nil {
		return step.Result{}, fmt.Errorf("showing dashboard: %w", err)
	}
	return step.Result{Outputs: map[string]any{"groups": resultValue(result)}}, nil
}

func (runner *Runner) tuiConfig(renderer step.DataTemplateRenderer, inline step.InlineWorkflowRunner) tui.DashboardConfig {
	config := tui.DashboardConfig{Title: runner.config.Title, MaxConcurrency: runner.config.MaxConcurrency}
	config.Groups = make([]tui.DashboardGroup, len(runner.config.Groups))
	for groupIndex, group := range runner.config.Groups {
		item := tui.DashboardGroup{ID: group.ID, Title: group.Title, Columns: group.Columns, Refresh: group.Refresh != nil}
		if group.Refresh != nil && group.Refresh.Every != nil {
			item.Every = group.Refresh.Every.Value()
		}
		item.Widgets = make([]tui.DashboardWidget, len(group.Widgets))
		for widgetIndex, widget := range group.Widgets {
			view := tui.DashboardWidget{ID: widget.ID, Title: widget.Title, Actions: make([]tui.DashboardAction, len(widget.Actions))}
			for actionIndex, action := range widget.Actions {
				view.Actions[actionIndex] = tui.DashboardAction{ID: action.ID, Label: action.Label, Confirm: strings.TrimSpace(action.Confirm) != "", RefreshGroups: slices.Clone(action.RefreshGroups)}
			}
			item.Widgets[widgetIndex] = view
		}
		config.Groups[groupIndex] = item
	}
	config.Refresh = func(ctx context.Context, groupID string) (map[string]any, error) {
		group, _, ok := runner.findGroup(groupID)
		if !ok || group.Refresh == nil {
			return nil, fmt.Errorf("unknown refresh group %q", groupID)
		}
		return inline.RunInlineWorkflow(ctx, refreshCall(group))
	}
	config.Action = func(ctx context.Context, groupID, widgetID, actionID string, data map[string]any) (map[string]any, error) {
		group, widget, action, ok := runner.findAction(groupID, widgetID, actionID)
		if !ok {
			return nil, fmt.Errorf("unknown action %q.%q.%q", groupID, widgetID, actionID)
		}
		return inline.RunInlineWorkflow(ctx, actionCall(group, widget, action, data))
	}
	config.Render = func(groupID, widgetID string, dimensions tui.DashboardWidgetDimensions, data map[string]any, refresh tui.DashboardState, actions map[string]tui.DashboardState) (string, error) {
		_, widget, ok := runner.findWidget(groupID, widgetID)
		if !ok {
			return "", fmt.Errorf("unknown widget %q.%q", groupID, widgetID)
		}
		return renderer.RenderContentWith(widget.Template, dashboardTemplateData(data, refresh, actions, dimensions))
	}
	config.Confirmation = func(groupID, widgetID, actionID string, dimensions tui.DashboardWidgetDimensions, data map[string]any, refresh tui.DashboardState, actions map[string]tui.DashboardState) (string, error) {
		_, _, action, ok := runner.findAction(groupID, widgetID, actionID)
		if !ok {
			return "", fmt.Errorf("unknown action %q.%q.%q", groupID, widgetID, actionID)
		}
		return renderer.RenderContentWith(action.Confirm, dashboardTemplateData(data, refresh, actions, dimensions))
	}
	return config
}

func refreshCall(group Group) step.InlineWorkflowCall {
	return step.InlineWorkflowCall{Name: "dashboard/" + group.ID + "/refresh", Steps: group.Refresh.Steps, Finally: group.Refresh.Finally, Outputs: cloneOutputs(group.Refresh.Outputs)}
}

func actionCall(group Group, widget Widget, action Action, data map[string]any) step.InlineWorkflowCall {
	return step.InlineWorkflowCall{
		Name:  "dashboard/" + group.ID + "/" + widget.ID + "/" + action.ID,
		Steps: action.Steps, Finally: action.Finally, Outputs: cloneOutputs(action.Outputs),
		Bindings: map[string]any{"data": workflow.CloneMap(data)},
	}
}

func cloneOutputs(outputs map[string]string) map[string]string {
	if outputs == nil {
		return map[string]string{}
	}
	return maps.Clone(outputs)
}

func outputPlaceholders(group Group) map[string]any {
	values := make(map[string]any)
	if group.Refresh != nil {
		for name := range group.Refresh.Outputs {
			values[name] = ""
		}
	}
	return values
}

func dashboardTemplateData(data map[string]any, refresh tui.DashboardState, actions map[string]tui.DashboardState, dimensions tui.DashboardWidgetDimensions) map[string]any {
	actionValues := make(map[string]any, len(actions))
	for id, state := range actions {
		actionValues[id] = stateValue(state)
	}
	return map[string]any{
		"data": workflow.CloneMap(data), "refresh": stateValue(refresh), "actions": actionValues,
		"widget": map[string]any{"width": dimensions.Width, "height": dimensions.Height},
	}
}

func stateValue(state tui.DashboardState) map[string]any {
	updatedAt := ""
	if !state.UpdatedAt.IsZero() {
		updatedAt = state.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"status": state.Status, "data": workflow.CloneMap(state.Data), "error": state.Error,
		"updated_at": updatedAt, "duration": state.Duration.String(),
	}
}

func resultValue(result tui.DashboardResult) map[string]any {
	groups := make(map[string]any, len(result.Groups))
	for _, group := range result.Groups {
		widgets := make(map[string]any, len(group.Widgets))
		for _, widget := range group.Widgets {
			actions := make(map[string]any, len(widget.Actions))
			for id, state := range widget.Actions {
				actions[id] = stateValue(state)
			}
			widgets[widget.ID] = map[string]any{"actions": actions}
		}
		value := stateValue(group.State)
		value["widgets"] = widgets
		groups[group.ID] = value
	}
	return groups
}

func (runner *Runner) findGroup(id string) (Group, int, bool) {
	for index, group := range runner.config.Groups {
		if group.ID == id {
			return group, index, true
		}
	}
	return Group{}, -1, false
}

func (runner *Runner) findWidget(groupID, widgetID string) (Group, Widget, bool) {
	group, _, ok := runner.findGroup(groupID)
	if !ok {
		return Group{}, Widget{}, false
	}
	for _, widget := range group.Widgets {
		if widget.ID == widgetID {
			return group, widget, true
		}
	}
	return Group{}, Widget{}, false
}

func (runner *Runner) findAction(groupID, widgetID, actionID string) (Group, Widget, Action, bool) {
	group, widget, ok := runner.findWidget(groupID, widgetID)
	if !ok {
		return Group{}, Widget{}, Action{}, false
	}
	for _, action := range widget.Actions {
		if action.ID == actionID {
			return group, widget, action, true
		}
	}
	return Group{}, Widget{}, Action{}, false
}

var _ step.Runner = (*Runner)(nil)
var _ step.Validator = (*Runner)(nil)
var _ step.InlineWorkflowAware = (*Runner)(nil)
