package choice

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	Variable         string         `yaml:"variable"`
	Message          string         `yaml:"message"`
	Multiple         bool           `yaml:"multiple,omitempty"`
	SelectAll        bool           `yaml:"select_all,omitempty"`
	AutoSelectSingle bool           `yaml:"auto_select_single,omitempty"`
	Required         *bool          `yaml:"required,omitempty"`
	MinSelected      *int           `yaml:"min_selected,omitempty"`
	MaxSelected      *int           `yaml:"max_selected,omitempty"`
	Choices          []ChoiceConfig `yaml:"choices,omitempty"`
	From             string         `yaml:"from,omitempty"`
	LabelField       string         `yaml:"label_field,omitempty"`
	LabelExpr        string         `yaml:"label_expr,omitempty"`
	ValueField       string         `yaml:"value_field,omitempty"`
	ValueExpr        string         `yaml:"value_expr,omitempty"`
	DescriptionField string         `yaml:"description_field,omitempty"`
	DescriptionExpr  string         `yaml:"description_expr,omitempty"`
	DisabledField    string         `yaml:"disabled_field,omitempty"`
	DisabledExpr     string         `yaml:"disabled_expr,omitempty"`
	ReasonField      string         `yaml:"reason_field,omitempty"`
	ReasonExpr       string         `yaml:"reason_expr,omitempty"`
	DefaultField     string         `yaml:"default_field,omitempty"`
	DefaultExpr      string         `yaml:"default_expr,omitempty"`
}

type ChoiceConfig struct {
	Label       string `yaml:"label"`
	Description string `yaml:"description,omitempty"`
	Value       any    `yaml:"value"`
	Disabled    bool   `yaml:"disabled,omitempty"`
	Reason      string `yaml:"reason,omitempty"`
	Default     bool   `yaml:"default,omitempty"`
}

type resolvedChoice struct {
	tui.Option
	item    any
	hasItem bool
}

type expressionRoots struct {
	Inputs       map[string]any               `expr:"inputs"`
	Vars         map[string]any               `expr:"vars"`
	Env          map[string]string            `expr:"env"`
	Steps        map[string]any               `expr:"steps"`
	Dependencies map[string]map[string]any    `expr:"dependencies"`
	Batch        map[string]any               `expr:"batch"`
	Foreach      map[string]any               `expr:"foreach"`
	Matrix       map[string]any               `expr:"matrix"`
	Observe      map[string]any               `expr:"observe"`
	Finally      map[string]any               `expr:"finally"`
	Error        map[string]any               `expr:"error"`
	Workflow     step.WorkflowValue           `expr:"workflow"`
	Run          step.RunValue                `expr:"run"`
	Secret       func(string) (string, error) `expr:"secret"`
}

type itemExpressionEnvironment struct {
	expressionRoots
	Item any `expr:"item"`
}

type labelExpressionEnvironment struct {
	itemExpressionEnvironment
	Label string `expr:"label"`
}

type valueExpressionEnvironment struct {
	labelExpressionEnvironment
	Value any `expr:"value"`
}

type descriptionExpressionEnvironment struct {
	valueExpressionEnvironment
	Description string `expr:"description"`
}

type disabledExpressionEnvironment struct {
	descriptionExpressionEnvironment
	Disabled bool `expr:"disabled"`
}

type reasonExpressionEnvironment struct {
	disabledExpressionEnvironment
	Reason string `expr:"reason"`
}

type expressionPrograms struct {
	label       *vm.Program
	value       *vm.Program
	description *vm.Program
	disabled    *vm.Program
	reason      *vm.Program
	defaultItem *vm.Program
}

type Runner struct {
	config   Config
	programs expressionPrograms
}

func Register(registry *step.Registry) error { return registry.Register("tui_choice", New) }

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if config.Variable == "" || strings.TrimSpace(config.Message) == "" {
		return nil, fmt.Errorf("variable and message are required")
	}
	if (len(config.Choices) == 0) == (config.From == "") {
		return nil, fmt.Errorf("exactly one of choices or from is required")
	}
	if err := validateBounds(config); err != nil {
		return nil, err
	}
	programs, err := compileExpressions(raw, config)
	if err != nil {
		return nil, err
	}
	if config.LabelField == "" && config.LabelExpr == "" {
		config.LabelField = "label"
	}
	if config.ValueField == "" && config.ValueExpr == "" {
		config.ValueField = "value"
	}
	return &Runner{config: config, programs: programs}, nil
}

func compileExpressions(raw map[string]any, config Config) (expressionPrograms, error) {
	type declaration struct {
		name        string
		field       string
		expression  string
		environment any
		target      **vm.Program
	}
	var programs expressionPrograms
	declarations := []declaration{
		{name: "label", field: config.LabelField, expression: config.LabelExpr, environment: itemExpressionEnvironment{}, target: &programs.label},
		{name: "value", field: config.ValueField, expression: config.ValueExpr, environment: labelExpressionEnvironment{}, target: &programs.value},
		{name: "description", field: config.DescriptionField, expression: config.DescriptionExpr, environment: valueExpressionEnvironment{}, target: &programs.description},
		{name: "disabled", field: config.DisabledField, expression: config.DisabledExpr, environment: descriptionExpressionEnvironment{}, target: &programs.disabled},
		{name: "reason", field: config.ReasonField, expression: config.ReasonExpr, environment: disabledExpressionEnvironment{}, target: &programs.reason},
		{name: "default", field: config.DefaultField, expression: config.DefaultExpr, environment: reasonExpressionEnvironment{}, target: &programs.defaultItem},
	}
	for _, declaration := range declarations {
		key := declaration.name + "_expr"
		if _, exists := raw[key]; !exists {
			continue
		}
		if strings.TrimSpace(declaration.expression) == "" {
			return expressionPrograms{}, fmt.Errorf("%s must be a non-empty expression", key)
		}
		if config.From == "" {
			return expressionPrograms{}, fmt.Errorf("%s requires from", key)
		}
		if declaration.field != "" {
			return expressionPrograms{}, fmt.Errorf("%s_field and %s are mutually exclusive", declaration.name, key)
		}
		program, err := wukoexpr.Compile(declaration.expression, expr.Env(declaration.environment))
		if err != nil {
			return expressionPrograms{}, fmt.Errorf("compiling %s: %w", key, err)
		}
		*declaration.target = program
	}
	return programs, nil
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	options, err := r.options(request)
	if err != nil {
		return step.Result{}, err
	}
	if err := ensureUnique(options); err != nil {
		return step.Result{}, err
	}
	if err := r.validateOptions(options); err != nil {
		return step.Result{}, err
	}

	if supplied, exists := request.Vars[r.config.Variable]; exists {
		return r.preSupplied(supplied, options)
	}
	if !request.Interactive {
		if r.minimum() == 0 {
			return r.selected(nil, options), nil
		}
		return step.Result{}, fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", r.config.Variable)
	}
	if r.config.AutoSelectSingle {
		if index := soleEnabledIndex(options); index >= 0 {
			return r.selected([]int{index}, options), nil
		}
	}
	indexes, err := tui.Choose(ctx, request.Stdin, request.Stdout, tui.ChoicePickerConfig{
		Message: r.config.Message, Options: tuiOptions(options), Multiple: r.config.Multiple, Required: r.required(),
		SelectAll:   r.config.SelectAll,
		MinSelected: r.config.MinSelected, MaxSelected: r.config.MaxSelected,
	})
	if err != nil {
		return step.Result{}, fmt.Errorf("choosing: %w", err)
	}
	return r.selected(indexes, options), nil
}

func (r *Runner) options(request step.Request) ([]resolvedChoice, error) {
	if len(r.config.Choices) > 0 {
		options := make([]resolvedChoice, len(r.config.Choices))
		for i, choice := range r.config.Choices {
			if choice.Label == "" {
				return nil, fmt.Errorf("choice %d has an empty label", i+1)
			}
			if !scalar(choice.Value) {
				return nil, fmt.Errorf("choice %d value must be a scalar", i+1)
			}
			if choice.Disabled && strings.TrimSpace(choice.Reason) == "" {
				return nil, fmt.Errorf("choice %d is disabled without a reason", i+1)
			}
			options[i] = resolvedChoice{Option: tui.Option{
				Label: choice.Label, Description: choice.Description, Value: choice.Value,
				Disabled: choice.Disabled, DisabledReason: choice.Reason, Default: choice.Default,
			}}
		}
		return options, nil
	}

	value, err := step.Lookup(request, r.config.From)
	if err != nil {
		return nil, fmt.Errorf("resolving choices: %w", err)
	}
	items, ok := asSlice(value)
	if !ok {
		return nil, fmt.Errorf("choice source %q is not a list", r.config.From)
	}
	options := make([]resolvedChoice, 0, len(items))
	for i, item := range items {
		option, err := r.resolveDynamicChoice(request, item, i+1)
		if err != nil {
			return nil, err
		}
		options = append(options, resolvedChoice{
			Option: option,
			item:   workflow.Clone(item), hasItem: !scalar(item),
		})
	}
	return options, nil
}

func (r *Runner) resolveDynamicChoice(request step.Request, item any, index int) (tui.Option, error) {
	itemEnvironment := itemExpressionEnvironment{expressionRoots: expressionRootsFor(request), Item: item}

	label := fmt.Sprint(item)
	if !scalar(item) {
		resolved, err := step.LookupValue(item, r.config.LabelField)
		if err != nil {
			return tui.Option{}, fmt.Errorf("choice source item %d label: %w", index, err)
		}
		label = fmt.Sprint(resolved)
	}
	if r.programs.label != nil {
		resolved, err := runExpression(r.programs.label, itemEnvironment, index, "label")
		if err != nil {
			return tui.Option{}, err
		}
		label = fmt.Sprint(resolved)
	}

	labelEnvironment := labelExpressionEnvironment{itemExpressionEnvironment: itemEnvironment, Label: label}
	value := item
	if !scalar(item) {
		resolved, err := step.LookupValue(item, r.config.ValueField)
		if err != nil {
			return tui.Option{}, fmt.Errorf("choice source item %d value: %w", index, err)
		}
		value = resolved
	}
	if r.programs.value != nil {
		resolved, err := runExpression(r.programs.value, labelEnvironment, index, "value")
		if err != nil {
			return tui.Option{}, err
		}
		value = resolved
	}
	if !scalar(value) {
		return tui.Option{}, fmt.Errorf("choice source item %d value must be a scalar", index)
	}

	valueEnvironment := valueExpressionEnvironment{labelExpressionEnvironment: labelEnvironment, Value: value}
	description := ""
	if !scalar(item) && r.config.DescriptionField != "" {
		resolved, err := step.LookupValue(item, r.config.DescriptionField)
		if err != nil {
			return tui.Option{}, fmt.Errorf("choice source item %d description: %w", index, err)
		}
		description = fmt.Sprint(resolved)
	}
	if r.programs.description != nil {
		resolved, err := runExpression(r.programs.description, valueEnvironment, index, "description")
		if err != nil {
			return tui.Option{}, err
		}
		description = fmt.Sprint(resolved)
	}

	descriptionEnvironment := descriptionExpressionEnvironment{valueExpressionEnvironment: valueEnvironment, Description: description}
	disabled := false
	if !scalar(item) {
		var err error
		disabled, err = boolField(item, r.config.DisabledField)
		if err != nil {
			return tui.Option{}, fmt.Errorf("choice source item %d disabled: %w", index, err)
		}
	}
	if r.programs.disabled != nil {
		resolved, err := runBoolExpression(r.programs.disabled, descriptionEnvironment, index, "disabled")
		if err != nil {
			return tui.Option{}, err
		}
		disabled = resolved
	}

	disabledEnvironment := disabledExpressionEnvironment{descriptionExpressionEnvironment: descriptionEnvironment, Disabled: disabled}
	reason := ""
	if disabled {
		if r.config.ReasonField == "" && r.programs.reason == nil {
			return tui.Option{}, fmt.Errorf("choice source item %d is disabled without a reason field or expression", index)
		}
		var resolved any
		var err error
		if r.programs.reason != nil {
			resolved, err = runExpression(r.programs.reason, disabledEnvironment, index, "reason")
		} else {
			resolved, err = step.LookupValue(item, r.config.ReasonField)
			if err != nil {
				err = fmt.Errorf("choice source item %d reason: %w", index, err)
			}
		}
		if err != nil {
			return tui.Option{}, err
		}
		var ok bool
		reason, ok = resolved.(string)
		if !ok || strings.TrimSpace(reason) == "" {
			return tui.Option{}, fmt.Errorf("choice source item %d reason must be a non-empty string", index)
		}
	}

	reasonEnvironment := reasonExpressionEnvironment{disabledExpressionEnvironment: disabledEnvironment, Reason: reason}
	defaultChoice := false
	if !scalar(item) {
		var err error
		defaultChoice, err = boolField(item, r.config.DefaultField)
		if err != nil {
			return tui.Option{}, fmt.Errorf("choice source item %d default: %w", index, err)
		}
	}
	if r.programs.defaultItem != nil {
		resolved, err := runBoolExpression(r.programs.defaultItem, reasonEnvironment, index, "default")
		if err != nil {
			return tui.Option{}, err
		}
		defaultChoice = resolved
	}

	return tui.Option{
		Label: label, Description: description, Value: value,
		Disabled: disabled, DisabledReason: reason, Default: defaultChoice,
	}, nil
}

func expressionRootsFor(request step.Request) expressionRoots {
	return expressionRoots{
		Inputs:       request.Inputs,
		Vars:         request.Vars,
		Env:          request.Env,
		Steps:        request.Steps,
		Dependencies: request.Dependencies,
		Batch:        bindingRoot(request.Bindings, "batch"),
		Foreach:      bindingRoot(request.Bindings, "foreach"),
		Matrix:       bindingRoot(request.Bindings, "matrix"),
		Observe:      bindingRoot(request.Bindings, "observe"),
		Finally:      bindingRoot(request.Bindings, "finally"),
		Error:        bindingRoot(request.Bindings, "error"),
		Workflow:     request.WorkflowValue(),
		Run:          request.RunValue(),
		Secret:       request.ResolveSecret,
	}
}

func bindingRoot(bindings map[string]any, name string) map[string]any {
	value, _ := bindings[name].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func runExpression(program *vm.Program, environment any, index int, property string) (any, error) {
	value, err := expr.Run(program, environment)
	if err != nil {
		return nil, fmt.Errorf("choice source item %d %s expression: %w", index, property, err)
	}
	return value, nil
}

func runBoolExpression(program *vm.Program, environment any, index int, property string) (bool, error) {
	value, err := runExpression(program, environment, index, property)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("choice source item %d %s expression must return a boolean, got %T", index, property, value)
	}
	return result, nil
}

func (r *Runner) preSupplied(value any, options []resolvedChoice) (step.Result, error) {
	if !r.config.Multiple {
		index := optionIndex(options, value)
		if index < 0 {
			if !r.required() && value == nil {
				return r.selected(nil, options), nil
			}
			return step.Result{}, fmt.Errorf("pre-supplied variable %q is not an available choice", r.config.Variable)
		}
		if options[index].Disabled {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q is a disabled choice: %s", r.config.Variable, options[index].DisabledReason)
		}
		return r.selected([]int{index}, options), nil
	}
	values, ok := asSlice(value)
	if !ok {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must be a list", r.config.Variable)
	}
	indexes := make([]int, 0, len(values))
	seen := make(map[int]struct{})
	for _, selected := range values {
		index := optionIndex(options, selected)
		if index < 0 {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q contains an unavailable choice %v", r.config.Variable, selected)
		}
		if options[index].Disabled {
			return step.Result{}, fmt.Errorf("pre-supplied variable %q contains disabled choice %v: %s", r.config.Variable, selected, options[index].DisabledReason)
		}
		if _, duplicate := seen[index]; duplicate {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	if len(indexes) < r.minimum() {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must contain at least %d values", r.config.Variable, r.minimum())
	}
	if r.config.MaxSelected != nil && len(indexes) > *r.config.MaxSelected {
		return step.Result{}, fmt.Errorf("pre-supplied variable %q must contain at most %d values", r.config.Variable, *r.config.MaxSelected)
	}
	return r.selected(indexes, options), nil
}

func (r *Runner) selected(indexes []int, options []resolvedChoice) step.Result {
	objectBacked := hasObjectItems(options)
	if !r.config.Multiple {
		if len(indexes) == 0 {
			outputs := map[string]any{"value": nil, "label": "", "selected": false}
			if objectBacked {
				outputs["item"] = nil
			}
			return step.Result{
				Outputs:   outputs,
				Variables: map[string]any{r.config.Variable: nil},
			}
		}
		option := options[indexes[0]]
		outputs := map[string]any{"value": option.Value, "label": option.Label, "selected": true}
		if objectBacked {
			outputs["item"] = nil
			if option.hasItem {
				outputs["item"] = workflow.Clone(option.item)
			}
		}
		return step.Result{
			Outputs:   outputs,
			Variables: map[string]any{r.config.Variable: option.Value},
		}
	}
	values := make([]any, 0, len(indexes))
	labels := make([]any, 0, len(indexes))
	items := make([]any, 0, len(indexes))
	for _, index := range indexes {
		option := options[index]
		values = append(values, option.Value)
		labels = append(labels, option.Label)
		if objectBacked {
			var item any
			if option.hasItem {
				item = workflow.Clone(option.item)
			}
			items = append(items, item)
		}
	}
	outputs := map[string]any{"values": values, "labels": labels, "count": len(values)}
	if objectBacked {
		outputs["items"] = items
	}
	return step.Result{
		Outputs:   outputs,
		Variables: map[string]any{r.config.Variable: values},
	}
}

func (r *Runner) required() bool { return r.config.Required == nil || *r.config.Required }

func (r *Runner) minimum() int {
	if !r.config.Multiple {
		if r.required() {
			return 1
		}
		return 0
	}
	if r.config.MinSelected != nil || r.config.MaxSelected != nil {
		if r.config.MinSelected != nil {
			return *r.config.MinSelected
		}
		return 0
	}
	if r.required() {
		return 1
	}
	return 0
}

func (r *Runner) validateOptions(options []resolvedChoice) error {
	enabled := 0
	defaults := 0
	for index, option := range options {
		if option.Disabled {
			if option.Default {
				return fmt.Errorf("choice %d cannot be both disabled and default", index+1)
			}
		} else {
			enabled++
		}
		if option.Default {
			defaults++
		}
	}
	if !r.config.Multiple && defaults > 1 {
		return fmt.Errorf("single choice mode allows at most one default")
	}
	if r.minimum() > enabled {
		return fmt.Errorf("minimum selected %d exceeds %d enabled choices", r.minimum(), enabled)
	}
	if r.config.SelectAll && r.config.MaxSelected != nil && enabled > *r.config.MaxSelected {
		return fmt.Errorf("select all would select %d choices, exceeding maximum selected %d", enabled, *r.config.MaxSelected)
	}
	if r.config.MaxSelected != nil && defaults > *r.config.MaxSelected {
		return fmt.Errorf("%d default choices exceed maximum selected %d", defaults, *r.config.MaxSelected)
	}
	return nil
}

func validateBounds(config Config) error {
	if config.AutoSelectSingle && config.Multiple {
		return fmt.Errorf("auto_select_single requires multiple: false")
	}
	if config.SelectAll && !config.Multiple {
		return fmt.Errorf("select_all requires multiple: true")
	}
	if (config.MinSelected != nil || config.MaxSelected != nil) && !config.Multiple {
		return fmt.Errorf("min_selected and max_selected require multiple: true")
	}
	if config.MinSelected != nil && *config.MinSelected < 0 {
		return fmt.Errorf("min_selected cannot be negative")
	}
	if config.MaxSelected != nil && *config.MaxSelected < 0 {
		return fmt.Errorf("max_selected cannot be negative")
	}
	if config.MinSelected != nil && config.MaxSelected != nil && *config.MinSelected > *config.MaxSelected {
		return fmt.Errorf("min_selected cannot exceed max_selected")
	}
	return nil
}

func soleEnabledIndex(options []resolvedChoice) int {
	selected := -1
	for index, option := range options {
		if option.Disabled {
			continue
		}
		if selected >= 0 {
			return -1
		}
		selected = index
	}
	return selected
}

func boolField(item any, path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	value, err := step.LookupValue(item, path)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("field %q must be a boolean", path)
	}
	return result, nil
}

func ensureUnique(options []resolvedChoice) error {
	seen := make(map[string]struct{}, len(options))
	for _, option := range options {
		encoded, err := json.Marshal(option.Value)
		if err != nil {
			return fmt.Errorf("encoding choice value: %w", err)
		}
		key := string(encoded)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate choice value %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func optionIndex(options []resolvedChoice, value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return -1
	}
	for i, option := range options {
		candidate, err := json.Marshal(option.Value)
		if err == nil && string(candidate) == string(encoded) {
			return i
		}
	}
	return -1
}

func tuiOptions(options []resolvedChoice) []tui.Option {
	result := make([]tui.Option, len(options))
	for i, option := range options {
		result[i] = option.Option
	}
	return result
}

func hasObjectItems(options []resolvedChoice) bool {
	for _, option := range options {
		if option.hasItem {
			return true
		}
	}
	return false
}

func scalar(value any) bool {
	switch value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func asSlice(value any) ([]any, bool) {
	if values, ok := value.([]any); ok {
		return values, true
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, false
	}
	values := make([]any, rv.Len())
	for i := range rv.Len() {
		values[i] = rv.Index(i).Interface()
	}
	return values, true
}
