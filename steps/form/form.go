// Package form implements a YAML-defined terminal form with named step outputs.
package form

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/types"
	"github.com/expr-lang/expr/vm"
	wukoexpr "github.com/up2jj/wuko/expression"
	"github.com/up2jj/wuko/step"
	"github.com/up2jj/wuko/steps/choice"
	datestep "github.com/up2jj/wuko/steps/date"
	luastep "github.com/up2jj/wuko/steps/lua"
	pathstep "github.com/up2jj/wuko/steps/path"
	"github.com/up2jj/wuko/tui"
	"github.com/up2jj/wuko/workflow"
)

type Config struct {
	Title       string  `yaml:"title"`
	Description string  `yaml:"description,omitempty"`
	Fields      []Field `yaml:"fields"`
	// NonInteractive answers each field without a value with its default when the run
	// cannot prompt.
	NonInteractive step.NonInteractive `yaml:"noninteractive,omitempty"`
}

// Field names one output and describes its widget and immutable input sources.
type Field struct {
	ID               string                `yaml:"id"`
	Type             string                `yaml:"type"`
	Label            string                `yaml:"label"`
	Description      string                `yaml:"description,omitempty"`
	Required         bool                  `yaml:"required,omitempty"`
	Value            any                   `yaml:"value,omitempty"`
	Default          any                   `yaml:"default,omitempty"`
	Validation       Validation            `yaml:"validation,omitempty"`
	Choices          []choice.ChoiceConfig `yaml:"choices,omitempty"`
	From             string                `yaml:"from,omitempty"`
	LabelField       string                `yaml:"label_field,omitempty"`
	ValueField       string                `yaml:"value_field,omitempty"`
	DescriptionField string                `yaml:"description_field,omitempty"`
	DisabledField    string                `yaml:"disabled_field,omitempty"`
	ReasonField      string                `yaml:"reason_field,omitempty"`
	MinSelected      *int                  `yaml:"min_selected,omitempty"`
	MaxSelected      *int                  `yaml:"max_selected,omitempty"`
	Root             string                `yaml:"root,omitempty"`
	Kind             string                `yaml:"kind,omitempty"`
	Patterns         []string              `yaml:"patterns,omitempty"`
	ShowHidden       bool                  `yaml:"show_hidden,omitempty"`
	Multiple         bool                  `yaml:"multiple,omitempty"`
	Format           string                `yaml:"format,omitempty"`
	Min              string                `yaml:"min,omitempty"`
	Max              string                `yaml:"max,omitempty"`
}

// Validation combines built-in constraints with one optional boolean predicate.
type Validation struct {
	Expr      string   `yaml:"expr,omitempty"`
	Lua       string   `yaml:"lua,omitempty"`
	MinLength int      `yaml:"min_length,omitempty"`
	MaxLength int      `yaml:"max_length,omitempty"`
	Pattern   string   `yaml:"pattern,omitempty"`
	Message   string   `yaml:"message,omitempty"`
	Min       *float64 `yaml:"min,omitempty"`
	Max       *float64 `yaml:"max,omitempty"`
	Integer   bool     `yaml:"integer,omitempty"`
}

type binding struct {
	value   any
	program *vm.Program
	present bool
}

type compiledField struct {
	config          Field
	value, fallback binding
	text            step.TextValidator
	predicate       *vm.Program
	luaPredicate    *luastep.Predicate
	choices         *choice.Runner
	path            *pathstep.Runner
	date            *datestep.Runner
}

type Runner struct {
	config Config
	fields []compiledField
	now    func() time.Time
	// read is injectable in package tests; production always uses one Bubble Tea program.
	read func(context.Context, step.Request, tui.FormConfig) (map[string]any, error)
}

var fieldID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func Register(registry *step.Registry) error {
	return registry.RegisterDefinition("tui_form", step.Registration{
		Builder: New, Outputs: step.OpenObject(), ConfigOutputs: outputSchema, References: references,
	})
}

func outputSchema(raw map[string]any) step.OutputSchema {
	outputs := map[string]step.OutputSchema{}
	fields, _ := raw["fields"].([]any)
	for _, item := range fields {
		field, _ := item.(map[string]any)
		id, _ := field["id"].(string)
		kind, _ := field["type"].(string)
		schema := step.Scalar()
		if kind == "multiselect" || kind == "path" && field["multiple"] == true {
			schema = step.Array(step.Scalar())
		}
		outputs[id] = schema
	}
	return step.ClosedObject(outputs)
}

func references(raw map[string]any) (step.References, error) {
	var result step.References
	fields, _ := raw["fields"].([]any)
	for index, item := range fields {
		field, _ := item.(map[string]any)
		prefix := fmt.Sprintf("fields[%d]", index)
		if source, _ := field["from"].(string); source != "" {
			result.Lookups = append(result.Lookups, step.LookupReference{Label: prefix + ".from", Path: source})
		}
		if validation, ok := field["validation"].(map[string]any); ok {
			if source, _ := validation["expr"].(string); source != "" {
				result.Expressions = append(result.Expressions, step.ExpressionReference{
					Label: prefix + ".validation.expr", Source: source, Locals: []string{"value", "fields"},
				})
			}
		}
		for _, key := range []string{"value", "default"} {
			if expression, ok := field[key].(map[string]any); ok {
				source, _ := expression["expr"].(string)
				result.Expressions = append(result.Expressions, step.ExpressionReference{Label: prefix + "." + key + ".expr", Source: source})
			}
		}
	}
	return result, nil
}

func New(raw map[string]any) (step.Runner, error) {
	var config Config
	if err := step.DecodeConfig(raw, &config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len(config.Fields) == 0 {
		return nil, fmt.Errorf("fields must not be empty")
	}
	rawFields, ok := raw["fields"].([]any)
	if !ok {
		return nil, fmt.Errorf("fields must be a list")
	}
	if err := config.NonInteractive.Validate(slices.ContainsFunc(config.Fields, func(field Field) bool { return field.Default != nil })); err != nil {
		return nil, err
	}
	runner := &Runner{config: config, now: time.Now, read: func(ctx context.Context, request step.Request, config tui.FormConfig) (map[string]any, error) {
		return tui.Form(ctx, request.Stdin, request.Stdout, config)
	}}
	fieldShape := make(types.Map, len(config.Fields))
	for i, field := range config.Fields {
		if !fieldID.MatchString(field.ID) {
			return nil, fmt.Errorf("field %d id must be a literal identifier", i+1)
		}
		if _, exists := fieldShape[field.ID]; exists {
			return nil, fmt.Errorf("duplicate field id %q", field.ID)
		}
		fieldShape[field.ID] = types.Any
	}
	for i, field := range config.Fields {
		rawField, ok := rawFields[i].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %d must be a mapping", i+1)
		}
		compiled, err := compileField(field, rawField, fieldShape)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", field.ID, err)
		}
		runner.fields = append(runner.fields, compiled)
	}
	return runner, nil
}

func compileField(field Field, raw map[string]any, fieldShape types.Map) (compiledField, error) {
	result := compiledField{config: field}
	if strings.TrimSpace(field.Label) == "" {
		return result, fmt.Errorf("label is required")
	}
	allowed := map[string]bool{"id": true, "type": true, "label": true, "description": true, "required": true, "value": true, "default": true, "validation": true}
	allow := func(keys ...string) {
		for _, key := range keys {
			allowed[key] = true
		}
	}
	switch field.Type {
	case "text", "textarea", "password":
		allow("validation")
		if field.Validation.Min != nil || field.Validation.Max != nil || field.Validation.Integer {
			return result, fmt.Errorf("numeric validation requires number type")
		}
		var err error
		result.text, err = step.CompileTextValidator(field.Required, step.TextValidation{
			MinLength: field.Validation.MinLength, MaxLength: field.Validation.MaxLength, Pattern: field.Validation.Pattern, Message: field.Validation.Message,
		})
		if err != nil {
			return result, err
		}
	case "number":
		allow("validation")
		rules := field.Validation
		if rules.MinLength != 0 || rules.MaxLength != 0 || rules.Pattern != "" {
			return result, fmt.Errorf("text validation requires a text field")
		}
		if rules.Min != nil && !finite(*rules.Min) || rules.Max != nil && !finite(*rules.Max) {
			return result, fmt.Errorf("numeric bounds must be finite")
		}
		if rules.Min != nil && rules.Max != nil && *rules.Min > *rules.Max {
			return result, fmt.Errorf("min cannot exceed max")
		}
	case "checkbox":
	case "select", "multiselect":
		allow("choices", "from", "label_field", "value_field", "description_field", "disabled_field", "reason_field")
		if field.Type == "multiselect" {
			allow("min_selected", "max_selected")
		}
		for _, option := range field.Choices {
			if option.Section != nil || option.Separator != nil || option.Default {
				return result, fmt.Errorf("use field default instead of choice defaults, sections, or separators")
			}
		}
		if field.From == "" && (field.LabelField != "" || field.ValueField != "" || field.DescriptionField != "" || field.DisabledField != "" || field.ReasonField != "") {
			return result, fmt.Errorf("choice mappings require from")
		}
		built, err := choice.New(choiceRaw(field, raw))
		if err != nil {
			return result, err
		}
		result.choices = built.(*choice.Runner)
	case "date":
		allow("format", "min", "max")
		dateRaw := map[string]any{"variable": field.ID, "message": field.Label, "required": field.Required}
		for _, key := range []string{"format", "min", "max"} {
			if value, ok := raw[key]; ok {
				dateRaw[key] = value
			}
		}
		built, err := datestep.New(dateRaw)
		if err != nil {
			return result, err
		}
		result.date = built.(*datestep.Runner)
	case "path":
		allow("root", "kind", "patterns", "show_hidden", "multiple")
		built, err := pathstep.New(pathRaw(field, raw))
		if err != nil {
			return result, err
		}
		result.path = built.(*pathstep.Runner)
	default:
		return result, fmt.Errorf("unsupported type %q", field.Type)
	}
	for key := range raw {
		if !allowed[key] {
			return result, fmt.Errorf("%s is not supported for %s fields", key, field.Type)
		}
	}
	validation, _ := raw["validation"].(map[string]any)
	for key := range validation {
		valid := key == "message" || key == "expr" || key == "lua"
		if field.Type == "number" {
			valid = valid || key == "min" || key == "max" || key == "integer"
		} else if field.Type == "text" || field.Type == "textarea" || field.Type == "password" {
			valid = valid || key == "min_length" || key == "max_length" || key == "pattern"
		}
		if !valid {
			return result, fmt.Errorf("validation.%s is not supported for %s fields", key, field.Type)
		}
	}
	var err error
	_, hasExpr := validation["expr"]
	_, hasLua := validation["lua"]
	if hasExpr && hasLua {
		return result, fmt.Errorf("validation.expr and validation.lua are mutually exclusive")
	}
	if hasExpr {
		if strings.TrimSpace(field.Validation.Expr) == "" {
			return result, fmt.Errorf("validation.expr must not be empty")
		}
		shape := step.ExpressionEnvironmentShape(nil)
		environment := make(types.Map, len(shape)+1)
		for name, value := range shape {
			environment[name] = types.TypeOf(value)
		}
		environment["value"] = types.Any
		environment["fields"] = fieldShape
		result.predicate, err = wukoexpr.Compile(field.Validation.Expr,
			expr.Env(environment), expr.AllowUndefinedVariables(), expr.AsBool())
		if err != nil {
			return result, fmt.Errorf("validation.expr: %w", err)
		}
	}
	if hasLua {
		if strings.TrimSpace(field.Validation.Lua) == "" {
			return result, fmt.Errorf("validation.lua must not be empty")
		}
		result.luaPredicate, err = luastep.CompilePredicate(field.Validation.Lua)
		if err != nil {
			return result, fmt.Errorf("validation.lua: %w", err)
		}
	}
	result.value, err = compileBinding(raw, "value")
	if err != nil {
		return result, err
	}
	result.fallback, err = compileBinding(raw, "default")
	if err != nil {
		return result, err
	}
	return result, nil
}

func compileBinding(raw map[string]any, key string) (binding, error) {
	value, present := raw[key]
	result := binding{value: workflow.Clone(value), present: present}
	if mapping, ok := value.(map[string]any); ok {
		source, ok := mapping["expr"].(string)
		if !ok || len(mapping) != 1 || strings.TrimSpace(source) == "" {
			return result, fmt.Errorf("%s must be a value or a non-empty {expr: ...} binding", key)
		}
		program, err := wukoexpr.Compile(source, expr.Env(step.ExpressionEnvironmentShape(nil)), expr.AllowUndefinedVariables())
		if err != nil {
			return result, fmt.Errorf("%s.expr: %w", key, err)
		}
		result.program = program
	}
	return result, nil
}

func (b binding) resolve(request step.Request) (any, error) {
	if b.program != nil {
		return expr.Run(b.program, request.ExpressionEnvironment(nil))
	}
	return workflow.Clone(b.value), nil
}

func choiceRaw(field Field, raw map[string]any) map[string]any {
	result := map[string]any{"variable": field.ID, "message": field.Label, "required": field.Required, "multiple": field.Type == "multiselect"}
	for _, key := range []string{"choices", "from", "label_field", "value_field", "description_field", "disabled_field", "reason_field", "min_selected", "max_selected"} {
		if value, ok := raw[key]; ok {
			result[key] = value
		}
	}
	if field.Type == "multiselect" && field.Required && (field.MinSelected == nil || *field.MinSelected == 0) {
		result["min_selected"] = 1
	}
	return result
}

func pathRaw(field Field, raw map[string]any) map[string]any {
	result := map[string]any{"variable": field.ID, "message": field.Label, "required": field.Required}
	for _, key := range []string{"root", "kind", "patterns", "show_hidden", "multiple"} {
		if value, ok := raw[key]; ok {
			result[key] = value
		}
	}
	return result
}

func (r *Runner) Run(ctx context.Context, request step.Request) (step.Result, error) {
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	// A form answers with its defaults only when every field has a value or a default, so
	// --defaults still shows a form that asks for something new.
	var missing, undefaulted []string
	for _, field := range r.fields {
		if !field.value.present {
			missing = append(missing, field.config.ID)
			if !field.fallback.present {
				undefaulted = append(undefaulted, field.config.ID)
			}
		}
	}
	useDefaults := request.UseDefault(r.config.NonInteractive) && len(undefaulted) == 0
	if !request.Interactive && !useDefaults {
		if request.UseDefault(r.config.NonInteractive) {
			missing = undefaulted
		}
		if len(missing) > 0 {
			return step.Result{}, fmt.Errorf("non-interactive form requires explicit value for fields: %s", strings.Join(missing, ", "))
		}
	}
	config := tui.FormConfig{Title: r.config.Title, Description: r.config.Description}
	// Date fields share one reference instant and one timezone lookup.
	reference, location := r.now(), time.Local
	for _, field := range r.fields {
		if field.date != nil {
			var err error
			if location, err = datestep.Location(request.WorkflowTimezone); err != nil {
				return step.Result{}, err
			}
			break
		}
	}
	for _, field := range r.fields {
		resolved, err := field.resolve(ctx, request, request.Interactive || useDefaults, reference, location)
		if err != nil {
			return step.Result{}, fmt.Errorf("field %q: %w", field.config.ID, err)
		}
		config.Fields = append(config.Fields, resolved)
	}
	values := make(map[string]any, len(config.Fields))
	if request.Interactive && !useDefaults {
		submitted, err := r.read(ctx, request, config)
		if err != nil {
			return step.Result{}, fmt.Errorf("reading form: %w", err)
		}
		values = submitted
	} else {
		for _, field := range config.Fields {
			values[field.ID] = field.Value
		}
	}
	if err := ctx.Err(); err != nil {
		return step.Result{}, err
	}
	// Recheck every output against the complete submitted draft, using the same
	// frozen choices and path root.
	outputs := make(map[string]any, len(config.Fields))
	draft := tui.FormDraft(config.Fields, values)
	for _, field := range config.Fields {
		value, exists := values[field.ID]
		if !exists {
			return step.Result{}, fmt.Errorf("form did not submit field %q", field.ID)
		}
		normalized, err := field.Validate(value, workflow.CloneMap(draft))
		if err != nil {
			return step.Result{}, fmt.Errorf("field %q: %w", field.ID, err)
		}
		outputs[field.ID] = workflow.Clone(normalized)
	}
	return step.Result{Outputs: outputs}, nil
}

// resolve builds one field; fallback selects its default when it has no explicit value.
func (field compiledField) resolve(ctx context.Context, request step.Request, fallback bool, reference time.Time, location *time.Location) (tui.FormField, error) {
	f := field.config
	result := tui.FormField{ID: f.ID, Type: f.Type, Label: f.Label, Description: f.Description, Required: f.Required}
	selected := field.value
	if !selected.present && fallback {
		selected = field.fallback
	}
	value, err := selected.resolve(request)
	if err != nil {
		return result, fmt.Errorf("resolving initial value: %w", err)
	}
	// A missing value and an expression that resolves to nil share the zero value.
	if value == nil {
		switch f.Type {
		case "checkbox":
			value = false
		case "multiselect":
			value = []any{}
		case "path":
			if f.Multiple {
				value = []any{}
			} else {
				value = ""
			}
		case "text", "textarea", "password", "date":
			value = ""
		}
	}
	result.Value = value
	var builtIn func(any) (any, error)
	switch f.Type {
	case "select", "multiselect":
		result.Options, err = field.choices.ResolveOptions(request)
	case "date":
		var config tui.DatePickerConfig
		config, builtIn, err = field.date.PreparePicker(reference, location)
		result.Date = &config
		if err == nil {
			if normalized, validateErr := datestep.Normalize(value, config.Format, config.Today); validateErr == nil {
				result.Value = normalized
			}
		}
	case "path":
		var config tui.PathPickerConfig
		config, builtIn, err = field.path.PreparePicker(request.RunDir)
		result.Path = &config
	}
	if err != nil {
		return result, err
	}
	if builtIn == nil {
		builtIn = func(value any) (any, error) { return field.validate(value, result.Options) }
	}
	result.Normalize = func(value any) (any, error) {
		if f.Type == "date" {
			return datestep.Normalize(value, result.Date.Format, result.Date.Today)
		}
		if f.Type == "number" {
			if value == nil || value == "" {
				return nil, nil
			}
			normalized, _, err := number(value)
			return normalized, err
		}
		if normalized, err := builtIn(value); err == nil {
			return normalized, nil
		}
		// Text, selections, and paths remain inspectable while a content rule fails.
		return value, nil
	}
	result.Validate = func(value any, _ map[string]any) (any, error) { return builtIn(value) }
	if field.predicate != nil || field.luaPredicate != nil {
		// Bind helper calls to the lifetime of the form, including cancellation.
		request.HelperContext = ctx
		environment := request.ExpressionEnvironment(nil)
		result.Validate = func(value any, fields map[string]any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			normalized, err := builtIn(value)
			if err != nil {
				return nil, err
			}
			matched := false
			if field.predicate != nil {
				locals := make(map[string]any, len(environment)+1)
				for key, value := range environment {
					locals[key] = value
				}
				locals["value"] = workflow.Clone(normalized)
				locals["fields"] = workflow.CloneMap(fields)
				checked, evalErr := expr.Run(field.predicate, locals)
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if evalErr != nil {
					return nil, fmt.Errorf("expression validator failed to execute")
				}
				var ok bool
				matched, ok = checked.(bool)
				if !ok {
					return nil, fmt.Errorf("expression validator must return a boolean")
				}
			} else {
				validationContext, cancel := context.WithTimeout(ctx, time.Second)
				validationRequest := request
				validationRequest.Bindings = maps.Clone(request.Bindings)
				if validationRequest.Bindings == nil {
					validationRequest.Bindings = map[string]any{}
				}
				validationRequest.Bindings["fields"] = workflow.CloneMap(fields)
				matched, err = field.luaPredicate.Evaluate(validationContext, validationRequest, normalized)
				cancel()
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					// Runtime diagnostics can contain the entered value, especially Lua error(value).
					return nil, fmt.Errorf("Lua validator failed; it must finish within one second and return a boolean")
				}
			}
			if !matched {
				message := field.config.Validation.Message
				if message == "" {
					message = "value does not satisfy validation"
				}
				return nil, fmt.Errorf("%s", message)
			}
			return normalized, nil
		}
	}
	// Values with the wrong type cannot be faithfully displayed; reject them before opening.
	// Content validation (required, bounds, membership) stays editable in interactive mode.
	if err := validateInitialType(f, value); err != nil {
		return result, err
	}
	return result, nil
}

func validateInitialType(field Field, value any) error {
	if value == nil && (field.Type == "select" || field.Type == "number") {
		return nil
	}
	valid := false
	switch field.Type {
	case "text", "textarea", "password", "date":
		_, valid = value.(string)
	case "checkbox":
		_, valid = value.(bool)
	case "number":
		_, _, err := number(value)
		valid = err == nil
	case "path":
		if !field.Multiple {
			_, valid = value.(string)
		} else {
			_, valid = valuesSlice(value)
		}
	case "multiselect":
		_, valid = valuesSlice(value)
	case "select":
		valid = true
	}
	if !valid {
		return fmt.Errorf("initial value has an invalid type for %s", field.Type)
	}
	return nil
}

func (field compiledField) validate(value any, options []tui.Option) (any, error) {
	f := field.config
	switch f.Type {
	case "text", "textarea", "password":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("must be a string")
		}
		if err := field.text(text); err != nil {
			return nil, err
		}
		return text, nil
	case "checkbox":
		checked, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("must be a boolean")
		}
		if f.Required && !checked {
			return nil, fmt.Errorf("must be checked")
		}
		return checked, nil
	case "number":
		if value == nil || value == "" {
			if f.Required {
				return nil, fmt.Errorf("a value is required")
			}
			return nil, nil
		}
		normalized, n, err := number(value)
		if err != nil {
			return nil, err
		}
		rules := f.Validation
		message := ""
		switch {
		case rules.Integer && math.Trunc(n) != n:
			message = "must be a whole number"
		case rules.Min != nil && n < *rules.Min:
			message = fmt.Sprintf("must be at least %g", *rules.Min)
		case rules.Max != nil && n > *rules.Max:
			message = fmt.Sprintf("must be at most %g", *rules.Max)
		}
		if message != "" {
			if rules.Message != "" {
				message = rules.Message
			}
			return nil, fmt.Errorf("%s", message)
		}
		return normalized, nil
	case "select":
		if value == nil && !f.Required {
			return nil, nil
		}
		if !validOption(value, options) {
			return nil, fmt.Errorf("select an available choice")
		}
		return value, nil
	case "multiselect":
		values, ok := valuesSlice(value)
		if !ok {
			return nil, fmt.Errorf("must be a list")
		}
		minimum := 0
		if f.Required {
			minimum = 1
		}
		if f.MinSelected != nil {
			minimum = max(minimum, *f.MinSelected)
		}
		if len(values) < minimum {
			return nil, fmt.Errorf("select at least %d values", minimum)
		}
		if f.MaxSelected != nil && len(values) > *f.MaxSelected {
			return nil, fmt.Errorf("select at most %d values", *f.MaxSelected)
		}
		for i, value := range values {
			if !validOption(value, options) {
				return nil, fmt.Errorf("select available choices")
			}
			for _, previous := range values[:i] {
				if tui.FormValuesEqual(value, previous) {
					return nil, fmt.Errorf("duplicate selections are not allowed")
				}
			}
		}
		return values, nil
	}
	return nil, fmt.Errorf("unsupported type")
}

func valuesSlice(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil, false
	}
	result := make([]any, reflected.Len())
	for i := range result {
		result[i] = reflected.Index(i).Interface()
	}
	return result, true
}

func validOption(value any, options []tui.Option) bool {
	for _, option := range options {
		if !option.Disabled && tui.FormValuesEqual(value, option.Value) {
			return true
		}
	}
	return false
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// number returns the workflow value and its float64 form for bounds checks.
func number(value any) (any, float64, error) {
	text, ok := value.(string)
	if !ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, 0, fmt.Errorf("must be a finite number")
		}
		text = string(encoded)
	}
	text = strings.TrimSpace(text)
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, 0, fmt.Errorf("must be a finite number")
	}
	numeric, ok := decoded.(json.Number)
	if !ok || string(numeric) != text {
		return nil, 0, fmt.Errorf("must be a finite number")
	}
	n, err := numeric.Float64()
	if err != nil || !finite(n) {
		return nil, 0, fmt.Errorf("must be a finite number")
	}
	return workflow.RuntimeValue(numeric), n, nil
}
