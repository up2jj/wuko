package workflow

import (
	"fmt"
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// defaultPollInterval is the cadence a readiness loop uses when interval is omitted.
const defaultPollInterval = 5 * time.Second

// AttemptDuration is one duration option written either as a Go duration literal such as 2m or as
// an Expr expression resolved when execution reaches the control. The two are told apart the same
// way LoopDelay tells them apart: a scalar that parses as a duration is a literal, anything else
// is an expression.
type AttemptDuration struct {
	Literal    Duration
	Expression string
	declared   bool
}

func (value *AttemptDuration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
		return fmt.Errorf("attempt duration must be a duration such as 500ms or an expression string")
	}
	// Assign both fields: the decode target carries seeded defaults, so leaving the other one
	// alone would let a default masquerade as a declared value.
	parsed, err := time.ParseDuration(node.Value)
	if err == nil {
		value.Literal, value.Expression, value.declared = Duration(parsed), "", true
		return nil
	}
	value.Literal, value.Expression, value.declared = 0, node.Value, true
	return nil
}

// Set reports whether the option was declared at all. A declared zero counts: timeout: 0s must
// reach the numeric rule that rejects it rather than looking unset.
func (value AttemptDuration) Set() bool { return value.declared || value.Expression != "" }

// AttemptCount is one integer option written either as a literal or as an Expr expression.
type AttemptCount struct {
	Literal    int
	Expression string
	declared   bool
}

func (value *AttemptCount) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("attempt count must be an integer or expression string")
	}
	switch node.Tag {
	case "!!int":
		var literal int
		if err := node.Decode(&literal); err != nil {
			return fmt.Errorf("attempt count must be an integer or expression string")
		}
		value.Literal, value.Expression = literal, ""
	case "!!str":
		if strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("attempt count expression must not be empty")
		}
		value.Literal, value.Expression = 0, node.Value
	default:
		return fmt.Errorf("attempt count must be an integer or expression string")
	}
	value.declared = true
	return nil
}

func (value AttemptCount) Set() bool { return value.declared || value.Expression != "" }

// AttemptFactor is one fractional option written either as a literal or as an Expr expression.
type AttemptFactor struct {
	Literal    float64
	Expression string
	declared   bool
}

func (value *AttemptFactor) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("attempt factor must be a number or expression string")
	}
	switch node.Tag {
	case "!!int", "!!float":
		var literal float64
		if err := node.Decode(&literal); err != nil {
			return fmt.Errorf("attempt factor must be a number or expression string")
		}
		value.Literal, value.Expression = literal, ""
	case "!!str":
		if strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("attempt factor expression must not be empty")
		}
		value.Literal, value.Expression = 0, node.Value
	default:
		return fmt.Errorf("attempt factor must be a number or expression string")
	}
	value.declared = true
	return nil
}

func (value AttemptFactor) Set() bool { return value.declared || value.Expression != "" }

// LiteralDuration, LiteralCount, and LiteralFactor build declared option values for callers that
// construct a control programmatically instead of decoding YAML. Declaring matters: a plain
// struct literal leaves the option looking undeclared, so a zero would be read as "unset" and
// skip the numeric rule that rejects it.
func LiteralDuration(value Duration) AttemptDuration {
	return AttemptDuration{Literal: value, declared: true}
}

func LiteralCount(value int) AttemptCount {
	return AttemptCount{Literal: value, declared: true}
}

func LiteralFactor(value float64) AttemptFactor {
	return AttemptFactor{Literal: value, declared: true}
}

// AttemptControl bounds, repeats, and polls one sequential body. It is the single spelling for
// every execution policy: timeout bounds one pass, the retry fields repeat a failing pass, and
// until repeats a succeeding-but-not-ready pass.
//
// The two repeat reasons are disjoint. A pass that fails consumes an attempt and waits the
// backoff delay; a pass that succeeds while until is false costs no attempt and waits interval.
// So when only ever inspects a failure and until only ever inspects a success.
type AttemptControl struct {
	// Duration is the body-less fixed delay. It excludes every other field.
	Duration AttemptDuration `yaml:"duration,omitempty"`

	Steps []Step `yaml:"steps,omitempty"`

	// Timeout bounds one pass of the body, not the control as a whole.
	Timeout AttemptDuration `yaml:"timeout,omitempty"`

	MaxAttempts       AttemptCount    `yaml:"max_attempts,omitempty"`
	InitialDelay      AttemptDuration `yaml:"initial_delay,omitempty"`
	BackoffMultiplier AttemptFactor   `yaml:"backoff_multiplier,omitempty"`
	MaxDelay          AttemptDuration `yaml:"max_delay,omitempty"`
	Jitter            AttemptFactor   `yaml:"jitter,omitempty"`
	When              Condition       `yaml:"when,omitempty"`
	Methods           []string        `yaml:"methods,omitempty"`
	Statuses          []StatusRange   `yaml:"statuses,omitempty"`

	Until    Condition       `yaml:"until,omitempty"`
	Interval AttemptDuration `yaml:"interval,omitempty"`

	// MaxElapsedTime bounds every pass, backoff delay, and poll interval together.
	MaxElapsedTime AttemptDuration `yaml:"max_elapsed_time,omitempty"`
	OperationID    string          `yaml:"operation_id,omitempty"`

	hasDuration bool
	hasMethods  bool
	hasStatuses bool
}

func (control *AttemptControl) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("attempt must be an object")
	}
	if err := rejectUnknownFields(node, "attempt", map[string]bool{
		"duration": true, "steps": true, "timeout": true,
		"max_attempts": true, "initial_delay": true, "backoff_multiplier": true,
		"max_delay": true, "jitter": true, "when": true, "methods": true, "statuses": true,
		"until": true, "interval": true, "max_elapsed_time": true, "operation_id": true,
	}); err != nil {
		return err
	}
	if steps := mappingValue(node, "steps"); steps != nil && steps.Kind != yaml.SequenceNode {
		return fmt.Errorf("attempt steps must be a list")
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != "when" {
			continue
		}
		value := node.Content[i+1]
		if value.Kind != yaml.ScalarNode || (value.Tag != "!!str" && value.Tag != "!!bool") {
			return fmt.Errorf("attempt when must be a boolean expression")
		}
		if strings.TrimSpace(value.Value) == "" {
			return fmt.Errorf("attempt when must not be empty")
		}
	}
	type plainAttemptControl AttemptControl
	decoded := plainAttemptControl{
		// One, not three: attempt is now also the spelling for a bare timeout, so the presence
		// of the key cannot imply retrying the way the old retry: key did. Repeating is opt-in
		// through max_attempts.
		MaxAttempts:       AttemptCount{Literal: 1},
		InitialDelay:      AttemptDuration{Literal: Duration(time.Second)},
		BackoffMultiplier: AttemptFactor{Literal: 2},
		MaxDelay:          AttemptDuration{Literal: Duration(30 * time.Second)},
		Jitter:            AttemptFactor{Literal: 0.2},
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*control = AttemptControl(decoded)
	// Presence, not nil-ness, selects the form, so `duration: null` is a shape error rather
	// than a silent body form.
	control.hasDuration = hasMappingField(node, "duration")
	// The poll cadence default is applied only for a readiness loop. Seeding it unconditionally
	// would make "interval requires until" unenforceable.
	if control.Until != "" && !hasMappingField(node, "interval") {
		control.Interval = AttemptDuration{Literal: Duration(defaultPollInterval)}
	}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "methods":
			control.hasMethods = true
		case "statuses":
			control.hasStatuses = true
		}
	}
	return nil
}

// IsDelay reports whether the control is the body-less fixed delay form.
func (control AttemptControl) IsDelay() bool {
	return control.hasDuration || control.Duration.Set()
}

// Validate checks the attempt declaration's shape, and any option written as a literal. Options
// written as expressions are resolved when execution reaches the control, so their numeric rules
// are enforced by ResolvedAttempt.Validate instead -- the same split batch size already lives with.
func (control AttemptControl) Validate() error {
	if control.IsDelay() {
		return control.validateDelay()
	}
	if control.Duration.Set() {
		return fmt.Errorf("attempt duration must be a duration such as 500ms or 2m")
	}
	if len(control.Steps) == 0 {
		return fmt.Errorf("attempt must contain at least one step")
	}
	if control.Interval.Set() && control.Until == "" {
		return fmt.Errorf("attempt interval requires until")
	}
	// timeout now bounds a single pass, so a readiness loop needs its own bound. This is the
	// successor to the old "polling wait requires a top-level timeout" rule.
	if control.Until != "" && !control.MaxElapsedTime.Set() {
		return fmt.Errorf("attempt until requires max_elapsed_time")
	}
	if control.Until != "" && strings.TrimSpace(string(control.Until)) == "" {
		return fmt.Errorf("attempt until must not be empty")
	}
	if err := control.validateHTTPFilters(); err != nil {
		return err
	}
	if control.OperationID != "" && strings.TrimSpace(control.OperationID) == "" {
		return fmt.Errorf("attempt operation_id cannot be blank")
	}
	if control.When != "" && strings.TrimSpace(string(control.When)) == "" {
		return fmt.Errorf("attempt when must not be empty")
	}
	literal, err := control.literalValues()
	if err != nil {
		return err
	}
	return literal.validate(control, false)
}

func (control AttemptControl) validateDelay() error {
	// Length, not nil-ness: require expansion writes an empty non-nil slice back through
	// transformChildSequences, so a body-less control still reaches here with Steps allocated.
	if len(control.Steps) > 0 || control.Timeout.Set() || control.MaxAttempts.Set() ||
		control.InitialDelay.Set() || control.BackoffMultiplier.Set() || control.MaxDelay.Set() ||
		control.Jitter.Set() || control.Until != "" || control.Interval.Set() || control.When != "" ||
		control.hasMethods || control.hasStatuses || len(control.Methods) > 0 || len(control.Statuses) > 0 ||
		control.MaxElapsedTime.Set() || control.OperationID != "" {
		return fmt.Errorf("attempt duration cannot be combined with other attempt fields")
	}
	if control.Duration.Expression != "" {
		return nil
	}
	if control.Duration.Literal.Value() <= 0 {
		return fmt.Errorf("attempt duration must be greater than zero")
	}
	return nil
}

func (control AttemptControl) validateHTTPFilters() error {
	if control.When != "" && (control.hasMethods || control.hasStatuses || len(control.Methods) > 0 || len(control.Statuses) > 0) {
		return fmt.Errorf("attempt when cannot be combined with methods or statuses")
	}
	if control.hasMethods && len(control.Methods) == 0 {
		return fmt.Errorf("attempt methods must contain at least one HTTP method")
	}
	if control.hasStatuses && len(control.Statuses) == 0 {
		return fmt.Errorf("attempt statuses must contain at least one HTTP status or range")
	}
	seenMethods := make(map[string]bool, len(control.Methods))
	for i, method := range control.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || !httpMethodPattern.MatchString(method) {
			return fmt.Errorf("attempt method %q is not a valid HTTP method", control.Methods[i])
		}
		if seenMethods[method] {
			return fmt.Errorf("attempt method %q is duplicated", method)
		}
		seenMethods[method] = true
		control.Methods[i] = method
	}
	for i, status := range control.Statuses {
		if status.From < 100 || status.From > 599 || status.To < 100 || status.To > 599 || status.From > status.To {
			return fmt.Errorf("attempt status range %d-%d must be ascending and between 100 and 599", status.From, status.To)
		}
		for _, previous := range control.Statuses[:i] {
			if status.From <= previous.To && previous.From <= status.To {
				return fmt.Errorf("attempt status ranges %d-%d and %d-%d overlap", previous.From, previous.To, status.From, status.To)
			}
		}
	}
	return nil
}

// ResolvedAttempt holds the concrete option values for one execution of the control.
type ResolvedAttempt struct {
	Duration          time.Duration
	Timeout           time.Duration
	MaxAttempts       int
	InitialDelay      time.Duration
	BackoffMultiplier float64
	MaxDelay          time.Duration
	Jitter            float64
	Interval          time.Duration
	MaxElapsedTime    time.Duration
	// HasTimeout distinguishes an unset timeout from one that resolved to zero.
	HasTimeout bool
}

// literalValues collects the options declared as literals, leaving expression-backed ones zero.
func (control AttemptControl) literalValues() (ResolvedAttempt, error) {
	return ResolvedAttempt{
		Duration:          control.Duration.Literal.Value(),
		Timeout:           control.Timeout.Literal.Value(),
		HasTimeout:        control.Timeout.Set(),
		MaxAttempts:       control.MaxAttempts.Literal,
		InitialDelay:      control.InitialDelay.Literal.Value(),
		BackoffMultiplier: control.BackoffMultiplier.Literal,
		MaxDelay:          control.MaxDelay.Literal.Value(),
		Jitter:            control.Jitter.Literal,
		Interval:          control.Interval.Literal.Value(),
		MaxElapsedTime:    control.MaxElapsedTime.Literal.Value(),
	}, nil
}

// Resolve turns every option into a concrete value, calling evaluate for the expression-backed
// ones. It is called once when execution reaches the control, so a policy never changes shape
// between passes.
func (control AttemptControl) Resolve(evaluate func(expression string) (any, error)) (ResolvedAttempt, error) {
	resolved, _ := control.literalValues()
	durations := []struct {
		name   string
		option AttemptDuration
		target *time.Duration
	}{
		{"duration", control.Duration, &resolved.Duration},
		{"timeout", control.Timeout, &resolved.Timeout},
		{"initial_delay", control.InitialDelay, &resolved.InitialDelay},
		{"max_delay", control.MaxDelay, &resolved.MaxDelay},
		{"interval", control.Interval, &resolved.Interval},
		{"max_elapsed_time", control.MaxElapsedTime, &resolved.MaxElapsedTime},
	}
	for _, entry := range durations {
		if entry.option.Expression == "" {
			continue
		}
		value, err := resolveDurationExpression(entry.name, entry.option.Expression, evaluate)
		if err != nil {
			return ResolvedAttempt{}, err
		}
		*entry.target = value
		if entry.name == "timeout" {
			resolved.HasTimeout = true
		}
	}
	if control.MaxAttempts.Expression != "" {
		value, err := resolveIntExpression("max_attempts", control.MaxAttempts.Expression, evaluate)
		if err != nil {
			return ResolvedAttempt{}, err
		}
		resolved.MaxAttempts = value
	}
	factors := []struct {
		name   string
		option AttemptFactor
		target *float64
	}{
		{"backoff_multiplier", control.BackoffMultiplier, &resolved.BackoffMultiplier},
		{"jitter", control.Jitter, &resolved.Jitter},
	}
	for _, entry := range factors {
		if entry.option.Expression == "" {
			continue
		}
		value, err := resolveFloatExpression(entry.name, entry.option.Expression, evaluate)
		if err != nil {
			return ResolvedAttempt{}, err
		}
		*entry.target = value
	}
	if err := resolved.validate(control, true); err != nil {
		return ResolvedAttempt{}, err
	}
	return resolved, nil
}

func resolveDurationExpression(name, expression string, evaluate func(string) (any, error)) (time.Duration, error) {
	value, err := evaluate(expression)
	if err != nil {
		return 0, fmt.Errorf("evaluating attempt %s: %w", name, err)
	}
	switch typed := value.(type) {
	case string:
		parsed, parseErr := time.ParseDuration(typed)
		if parseErr != nil {
			return 0, fmt.Errorf("attempt %s expression returned %q, want a duration string", name, typed)
		}
		return parsed, nil
	case time.Duration:
		return typed, nil
	default:
		return 0, fmt.Errorf("attempt %s expression returned %T, want a duration string", name, value)
	}
}

func resolveIntExpression(name, expression string, evaluate func(string) (any, error)) (int, error) {
	value, err := evaluate(expression)
	if err != nil {
		return 0, fmt.Errorf("evaluating attempt %s: %w", name, err)
	}
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != math.Trunc(typed) {
			return 0, fmt.Errorf("attempt %s expression returned %v, want a whole number", name, typed)
		}
		return int(typed), nil
	default:
		return 0, fmt.Errorf("attempt %s expression returned %T, want an integer", name, value)
	}
}

func resolveFloatExpression(name, expression string, evaluate func(string) (any, error)) (float64, error) {
	value, err := evaluate(expression)
	if err != nil {
		return 0, fmt.Errorf("evaluating attempt %s: %w", name, err)
	}
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case int:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	default:
		return 0, fmt.Errorf("attempt %s expression returned %T, want a number", name, value)
	}
}

// validate enforces the numeric rules. resolved reports whether every expression has been
// evaluated: before that, an option backed by an expression is still zero and must be skipped.
func (values ResolvedAttempt) validate(control AttemptControl, resolved bool) error {
	// An option backed by an expression is still zero until Resolve has run, so its numeric
	// rule can only be enforced once every expression has been evaluated.
	known := func(expression string) bool { return resolved || expression == "" }
	if control.IsDelay() {
		if known(control.Duration.Expression) && values.Duration <= 0 {
			return fmt.Errorf("attempt duration must be greater than zero")
		}
		return nil
	}
	if known(control.Timeout.Expression) && values.HasTimeout && values.Timeout <= 0 {
		return fmt.Errorf("attempt timeout must be greater than zero")
	}
	if control.Interval.Set() && known(control.Interval.Expression) && values.Interval <= 0 {
		return fmt.Errorf("attempt interval must be greater than zero")
	}
	if known(control.MaxAttempts.Expression) && (values.MaxAttempts < 1 || values.MaxAttempts > 100) {
		return fmt.Errorf("attempt max_attempts must be between 1 and 100")
	}
	if known(control.InitialDelay.Expression) && values.InitialDelay < 0 {
		return fmt.Errorf("attempt initial_delay cannot be negative")
	}
	if known(control.BackoffMultiplier.Expression) &&
		(math.IsNaN(values.BackoffMultiplier) || math.IsInf(values.BackoffMultiplier, 0) || values.BackoffMultiplier < 1) {
		return fmt.Errorf("attempt backoff_multiplier must be at least 1")
	}
	if known(control.MaxDelay.Expression) && known(control.InitialDelay.Expression) &&
		values.MaxDelay < values.InitialDelay {
		return fmt.Errorf("attempt max_delay cannot be less than initial_delay")
	}
	if known(control.Jitter.Expression) &&
		(math.IsNaN(values.Jitter) || math.IsInf(values.Jitter, 0) || values.Jitter < 0 || values.Jitter > 1) {
		return fmt.Errorf("attempt jitter must be between 0 and 1")
	}
	if control.Until != "" && known(control.MaxElapsedTime.Expression) && values.MaxElapsedTime <= 0 {
		return fmt.Errorf("attempt max_elapsed_time must be greater than zero when until is set")
	}
	if known(control.MaxElapsedTime.Expression) && values.MaxElapsedTime < 0 {
		return fmt.Errorf("attempt max_elapsed_time cannot be negative")
	}
	return nil
}

// attemptContainsDefer rejects a defer anywhere in an attempt body. A body defer registers on the
// enclosing scope, is re-registered by every pass, and finally runs against state that was
// discarded. Cleanup belongs on the attempt step itself, where it runs once after the control
// commits.
func attemptContainsDefer(steps []Step) error {
	for _, step := range steps {
		if step.Defer != nil {
			return fmt.Errorf("defer is not supported inside attempt")
		}
		for _, child := range step.ChildSequences() {
			if err := attemptContainsDefer(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

// attemptContainsReturn rejects a return anywhere in an attempt body. Every pass runs on a state
// clone whose returning flag is never propagated, so a return would be swallowed on a failing
// pass and honored on a successful one. Cleanup sequences are left to validateSteps, which
// reports them against their own scope.
func attemptContainsReturn(steps []Step) error {
	for _, step := range steps {
		if step.Return != nil {
			return fmt.Errorf("return is not supported inside attempt")
		}
		for _, child := range step.ChildSequences() {
			if child.Role == ChildDefer || child.Role == ChildFinally {
				continue
			}
			if err := attemptContainsReturn(child.Steps); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAttemptEntry(workflowStep Step, scope stepScope, allowActions bool, enclosing map[string]struct{}) error {
	if workflowStep.ID == "" || !identifierPattern.MatchString(workflowStep.ID) {
		return fmt.Errorf("attempt requires a valid id")
	}
	if workflowStep.Type != "" || !workflowStep.Uses.Empty() || workflowStep.Require != nil || workflowStep.Worktree != nil || workflowStep.Executor != nil || workflowStep.Finally != nil || workflowStep.IsEnvironmentBlock() || workflowStep.IsWorkingDirectoryBlock() || workflowStep.Concurrent != nil || workflowStep.Batch != nil || workflowStep.Foreach != nil || workflowStep.Matrix != nil || workflowStep.Loop != nil || workflowStep.Once != nil || workflowStep.CancelOn != nil || workflowStep.Observe != nil || workflowStep.Return != nil || workflowStep.SHA256 != "" || workflowStep.With != nil || workflowStep.Steps != nil {
		return fmt.Errorf("attempt cannot be combined with ordinary step fields")
	}
	if err := workflowStep.Attempt.Validate(); err != nil {
		return err
	}
	if workflowStep.Attempt.IsDelay() {
		return nil
	}
	if err := attemptContainsDefer(workflowStep.Attempt.Steps); err != nil {
		return err
	}
	if err := attemptContainsReturn(workflowStep.Attempt.Steps); err != nil {
		return err
	}
	// The body publishes only through the control's own output, so it gets its own id scope --
	// but it keeps the restrictions of the scope the attempt itself sits in, exactly as once,
	// try/catch, and cancel_on bodies do.
	return validateStepScope(workflowStep.Attempt.Steps, allowActions, scope, enclosing)
}
