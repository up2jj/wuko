package step

import "fmt"

// PromptAnswers carries invocation-wide answers for prompt steps, set by run flags such as
// --yes and --defaults. A variable supplied with --var or declared in vars still wins.
type PromptAnswers struct {
	// Yes answers every confirmation prompt with true.
	Yes bool
	// Defaults answers every prompt that declares a default with that default.
	Defaults bool
}

// NonInteractive selects how a prompt answers when it cannot read from a terminal.
type NonInteractive string

const (
	// NonInteractiveFail rejects a prompt without a pre-supplied variable. It is the default.
	NonInteractiveFail NonInteractive = "fail"
	// NonInteractiveDefault answers the prompt with its declared default.
	NonInteractiveDefault NonInteractive = "default"
)

// Validate rejects an unknown policy and a default policy for a prompt that declares no
// default. hasDefault is true when the default is only known at run time.
func (policy NonInteractive) Validate(hasDefault bool) error {
	switch policy {
	case "", NonInteractiveFail:
		return nil
	case NonInteractiveDefault:
		if !hasDefault {
			return fmt.Errorf("noninteractive: default requires a default answer")
		}
		return nil
	}
	return fmt.Errorf("noninteractive must be %q or %q, got %q", NonInteractiveDefault, NonInteractiveFail, policy)
}

// UseDefault reports whether a prompt with a declared default should answer with it instead of
// asking: --defaults was given, or the run cannot prompt and the step opted into its default.
func (request Request) UseDefault(policy NonInteractive) bool {
	return request.Prompts.Defaults || !request.Interactive && policy == NonInteractiveDefault
}

// MissingAnswerError reports a prompt that cannot ask and has no pre-supplied variable.
// canDefault adds a hint when the prompt declares a default it could answer with.
func MissingAnswerError(variable string, canDefault bool) error {
	if canDefault {
		return fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var, or answer with the default using noninteractive: default or --defaults", variable)
	}
	return fmt.Errorf("variable %q is required when stdin is non-interactive; supply it with --var", variable)
}
