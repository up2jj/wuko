package process

import (
	"errors"
	"maps"
	"reflect"
	"slices"
)

// ErrAllowedExitCodes reports an allowed_exit_codes value that is neither a list nor any.
var ErrAllowedExitCodes = errors.New("allowed_exit_codes must be a non-empty list of exit codes from 0 through 255 or any")

// ExitCodePolicy decides which process exit codes complete a step instead of failing it.
type ExitCodePolicy struct {
	allowAny bool
	codes    []int
}

// Allows reports whether an exit code passes the policy. The any form accepts every normal process
// status but not the -1 that os/exec reports for a signal-terminated child, so a killed or crashed
// command still fails the step.
func (policy ExitCodePolicy) Allows(code int) bool {
	if policy.allowAny {
		return code >= 0 && code <= 255
	}
	return slices.Contains(policy.codes, code)
}

// NormalizeAllowedExitCodes reports whether a step configured allowed_exit_codes as any. The any
// form is returned as a flag rather than a list because no list can express it, along with a copy
// of raw that omits the key so the remaining list form decodes into the step's own configuration.
func NormalizeAllowedExitCodes(raw map[string]any) (map[string]any, bool, error) {
	value, configured := raw["allowed_exit_codes"]
	if !configured {
		return raw, false, nil
	}
	if text, isText := value.(string); isText {
		if text != "any" {
			return nil, false, ErrAllowedExitCodes
		}
		decoded := maps.Clone(raw)
		delete(decoded, "allowed_exit_codes")
		return decoded, true, nil
	}
	if value == nil {
		return nil, false, ErrAllowedExitCodes
	}
	kind := reflect.TypeOf(value).Kind()
	if kind != reflect.Array && kind != reflect.Slice {
		return nil, false, ErrAllowedExitCodes
	}
	return raw, false, nil
}

// NewExitCodePolicy validates a step's decoded allowed_exit_codes list against the configured and
// any flags that NormalizeAllowedExitCodes and the raw configuration supply. An unconfigured step
// accepts only exit code 0.
func NewExitCodePolicy(codes []int, configured, allowAny bool) (ExitCodePolicy, error) {
	if !configured {
		return ExitCodePolicy{codes: []int{0}}, nil
	}
	if allowAny {
		return ExitCodePolicy{allowAny: true}, nil
	}
	if len(codes) == 0 {
		return ExitCodePolicy{}, ErrAllowedExitCodes
	}
	for _, code := range codes {
		if code < 0 || code > 255 {
			return ExitCodePolicy{}, errors.New("allowed_exit_codes must contain only exit codes from 0 through 255")
		}
	}
	return ExitCodePolicy{codes: codes}, nil
}
