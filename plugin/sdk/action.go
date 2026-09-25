package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// InputType is the value kind accepted by a composite-action input.
type InputType string

const (
	StringInput  InputType = "string"
	BooleanInput InputType = "boolean"
	NumberInput  InputType = "number"
	ArrayInput   InputType = "array"
	ObjectInput  InputType = "object"
)

// OptionalValue distinguishes an omitted default from an explicit null default.
type OptionalValue struct {
	Value any
	Set   bool
}

// Some returns a declared action-input default. value may be nil.
func Some(value any) OptionalValue { return OptionalValue{Value: value, Set: true} }

// Action is a static composite action exported by a plugin.
type Action struct {
	Name        string
	Description string
	Inputs      map[string]ActionInput
	Outputs     map[string]ActionOutput
	Templates   map[string]string
	Steps       []ActionStep
	Finally     []ActionStep
}

type ActionInput struct {
	Type        InputType
	Description string
	Required    bool
	Default     OptionalValue
}

type ActionOutput struct {
	Description string `json:"description,omitempty"`
	Value       string `json:"value"`
}

// Duration is a Go duration string such as "500ms" or "2m".
type Duration string

// ActionStep mirrors the step and recursive control fields accepted inside a
// Wuko composite action. It intentionally has no uses, require, or sha256 field.
type ActionStep struct {
	ID               string            `json:"id,omitempty"`
	Needs            []string          `json:"needs,omitempty"`
	Type             string            `json:"type,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	Worktree         *WorktreeGroup    `json:"worktree,omitempty"`
	Steps            []ActionStep      `json:"steps,omitempty"`
	Finally          []ActionStep      `json:"finally,omitempty"`
	Defer            []ActionStep      `json:"defer,omitempty"`
	Concurrent       *ConcurrentGroup  `json:"concurrent,omitempty"`
	Batch            *BatchGroup       `json:"batch,omitempty"`
	Foreach          *ForeachGroup     `json:"foreach,omitempty"`
	Matrix           *MatrixGroup      `json:"matrix,omitempty"`
	Loop             *LoopGroup        `json:"loop,omitempty"`
	Once             *OnceGroup        `json:"once,omitempty"`
	Attempt          *AttemptControl   `json:"attempt,omitempty"`
	CancelOn         *CancelOnGroup    `json:"cancel_on,omitempty"`
	Try              *TryBlock         `json:"try,omitempty"`
	Catch            *CatchBlock       `json:"catch,omitempty"`
	Return           *ReturnControl    `json:"return,omitempty"`
	If               string            `json:"if,omitempty"`
	With             map[string]any    `json:"with,omitempty"`
}

type WorktreeGroup struct {
	Revision string           `json:"revision"`
	Path     string           `json:"path,omitempty"`
	Publish  *WorktreePublish `json:"publish,omitempty"`
	Steps    []ActionStep     `json:"steps"`
}

type WorktreePublish struct {
	Branch string `json:"branch"`
}

// FailFast is a pointer in fan-out groups so false can be distinguished from
// omission (whose Wuko default is true). Bool is a convenience constructor.
func Bool(value bool) *bool { return &value }

type ConcurrentGroup struct {
	Steps          []ActionStep `json:"steps"`
	MaxConcurrency int          `json:"max_concurrency,omitempty"`
	Timeout        Duration     `json:"timeout,omitempty"`
	FailFast       *bool        `json:"fail_fast,omitempty"`
}

type BatchGroup struct {
	Items          string       `json:"items"`
	Size           any          `json:"size"`
	Collect        string       `json:"collect,omitempty"`
	Steps          []ActionStep `json:"steps"`
	MaxConcurrency int          `json:"max_concurrency,omitempty"`
	MaxIterations  int          `json:"max_iterations,omitempty"`
	Timeout        Duration     `json:"timeout,omitempty"`
	FailFast       *bool        `json:"fail_fast,omitempty"`
}

type ForeachGroup struct {
	Items          string       `json:"items"`
	Collect        string       `json:"collect,omitempty"`
	Steps          []ActionStep `json:"steps"`
	MaxConcurrency int          `json:"max_concurrency,omitempty"`
	MaxIterations  int          `json:"max_iterations,omitempty"`
	Timeout        Duration     `json:"timeout,omitempty"`
	FailFast       *bool        `json:"fail_fast,omitempty"`
}

type MatrixGroup struct {
	Axes           MatrixAxes   `json:"axes"`
	Collect        string       `json:"collect,omitempty"`
	Steps          []ActionStep `json:"steps"`
	MaxConcurrency int          `json:"max_concurrency,omitempty"`
	MaxIterations  int          `json:"max_iterations,omitempty"`
	Timeout        Duration     `json:"timeout,omitempty"`
	FailFast       *bool        `json:"fail_fast,omitempty"`
}

type MatrixAxis struct {
	Name       string
	Values     []any
	Expression string
}

// MatrixAxes preserves the author's axis order in the canonical JSON object.
type MatrixAxes []MatrixAxis

func (axes MatrixAxes) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	seen := make(map[string]bool, len(axes))
	for index, axis := range axes {
		if axis.Name == "" || seen[axis.Name] {
			return nil, fmt.Errorf("matrix axis names must be non-empty and unique")
		}
		seen[axis.Name] = true
		if (axis.Values == nil) == (axis.Expression == "") {
			return nil, fmt.Errorf("matrix axis %q must set exactly one of Values or Expression", axis.Name)
		}
		if index > 0 {
			buffer.WriteByte(',')
		}
		name, _ := json.Marshal(axis.Name)
		buffer.Write(name)
		buffer.WriteByte(':')
		var value []byte
		var err error
		if axis.Values != nil {
			value, err = json.Marshal(axis.Values)
		} else {
			value, err = json.Marshal(axis.Expression)
		}
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

type LoopGroup struct {
	Until         string       `json:"until"`
	Delay         string       `json:"delay,omitempty"`
	Steps         []ActionStep `json:"steps"`
	MaxIterations int          `json:"max_iterations,omitempty"`
	Timeout       Duration     `json:"timeout,omitempty"`
}

type OnceGroup struct {
	Key    string       `json:"key"`
	Scope  string       `json:"scope"`
	OnBusy string       `json:"on_busy,omitempty"`
	Steps  []ActionStep `json:"steps"`
}

// AttemptControl scalar options accept their native wire forms: durations and
// expressions are strings, counts are integers or strings, and factors are
// numbers or strings.
type AttemptControl struct {
	Duration          string       `json:"duration,omitempty"`
	Steps             []ActionStep `json:"steps,omitempty"`
	Timeout           string       `json:"timeout,omitempty"`
	MaxAttempts       any          `json:"max_attempts,omitempty"`
	InitialDelay      string       `json:"initial_delay,omitempty"`
	BackoffMultiplier any          `json:"backoff_multiplier,omitempty"`
	MaxDelay          string       `json:"max_delay,omitempty"`
	Jitter            any          `json:"jitter,omitempty"`
	When              string       `json:"when,omitempty"`
	Methods           []string     `json:"methods,omitempty"`
	Statuses          []Status     `json:"statuses,omitempty"`
	Until             string       `json:"until,omitempty"`
	Interval          string       `json:"interval,omitempty"`
	MaxElapsedTime    string       `json:"max_elapsed_time,omitempty"`
	OperationID       string       `json:"operation_id,omitempty"`
}

// Status is either a status code (From == To) or an inclusive range.
type Status struct {
	From int
	To   int
}

func (status Status) MarshalJSON() ([]byte, error) {
	if status.To == 0 || status.To == status.From {
		return json.Marshal(status.From)
	}
	return json.Marshal(fmt.Sprintf("%d-%d", status.From, status.To))
}

type CancelOnGroup struct {
	Monitors []ActionStep `json:"monitors"`
	Steps    []ActionStep `json:"steps"`
	Collect  string       `json:"collect,omitempty"`
}

type TryBlock struct {
	Steps []ActionStep `json:"steps"`
}

type CatchBlock struct {
	Steps []ActionStep `json:"steps"`
}

// ReturnControl omits a destination because composite actions may only return
// to their caller.
type ReturnControl struct {
	Outputs map[string]string `json:"outputs"`
}

type actionWire struct {
	Version     int                     `json:"version"`
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Inputs      map[string]actionInput  `json:"inputs,omitempty"`
	Outputs     map[string]ActionOutput `json:"outputs,omitempty"`
	Templates   map[string]string       `json:"templates,omitempty"`
	Steps       []ActionStep            `json:"steps"`
	Finally     []ActionStep            `json:"finally,omitempty"`
}

type actionInput struct {
	Type        InputType `json:"type"`
	Description string    `json:"description,omitempty"`
	Required    bool      `json:"required,omitempty"`
	Default     any       `json:"default,omitempty"`
	hasDefault  bool
}

func (input actionInput) MarshalJSON() ([]byte, error) {
	type withoutDefault struct {
		Type        InputType `json:"type"`
		Description string    `json:"description,omitempty"`
		Required    bool      `json:"required,omitempty"`
	}
	base := withoutDefault{Type: input.Type, Description: input.Description, Required: input.Required}
	if !input.hasDefault {
		return json.Marshal(base)
	}
	return json.Marshal(struct {
		withoutDefault
		Default any `json:"default"`
	}{withoutDefault: base, Default: input.Default})
}

var actionIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func snapshotAction(exportedName string, action Action) (json.RawMessage, error) {
	if !actionIdentifier.MatchString(exportedName) {
		return nil, fmt.Errorf("invalid action name %q", exportedName)
	}
	if action.Name == "" {
		action.Name = exportedName
	}
	if strings.TrimSpace(action.Name) == "" {
		return nil, fmt.Errorf("action name is required")
	}
	if len(action.Steps) == 0 {
		return nil, fmt.Errorf("action %q must contain at least one step", exportedName)
	}
	inputs := make(map[string]actionInput, len(action.Inputs))
	for name, input := range action.Inputs {
		if !actionIdentifier.MatchString(name) {
			return nil, fmt.Errorf("invalid input name %q", name)
		}
		switch input.Type {
		case StringInput, BooleanInput, NumberInput, ArrayInput, ObjectInput:
		default:
			return nil, fmt.Errorf("input %q has unsupported type %q", name, input.Type)
		}
		if input.Required && input.Default.Set {
			return nil, fmt.Errorf("input %q cannot be required and have a default", name)
		}
		if input.Default.Set {
			normalized, err := normalizeJSONValue(input.Default.Value)
			if err != nil {
				return nil, fmt.Errorf("input %q default is not JSON-compatible: %w", name, err)
			}
			if !inputValueMatches(input.Type, normalized) {
				return nil, fmt.Errorf("input %q default does not match type %s", name, input.Type)
			}
		}
		inputs[name] = actionInput{Type: input.Type, Description: input.Description, Required: input.Required, Default: input.Default.Value, hasDefault: input.Default.Set}
	}
	for name, output := range action.Outputs {
		if !actionIdentifier.MatchString(name) {
			return nil, fmt.Errorf("invalid output name %q", name)
		}
		if strings.TrimSpace(output.Value) == "" {
			return nil, fmt.Errorf("output %q value is required", name)
		}
	}
	wire := actionWire{Version: 1, Name: action.Name, Description: action.Description, Inputs: inputs, Outputs: action.Outputs, Templates: action.Templates, Steps: action.Steps, Finally: action.Finally}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encoding action %q: %w", exportedName, err)
	}
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, encoded); err != nil {
		return nil, fmt.Errorf("canonicalizing action %q: %w", exportedName, err)
	}
	if canonical.Len() > 1<<20 {
		return nil, fmt.Errorf("action %q exceeds the 1 MiB manifest limit", exportedName)
	}
	return json.RawMessage(bytes.Clone(canonical.Bytes())), nil
}

func normalizeJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func inputValueMatches(kind InputType, value any) bool {
	switch kind {
	case StringInput:
		_, ok := value.(string)
		return ok
	case BooleanInput:
		_, ok := value.(bool)
		return ok
	case NumberInput:
		_, ok := value.(json.Number)
		return ok
	case ArrayInput:
		_, ok := value.([]any)
		return ok
	case ObjectInput:
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}
