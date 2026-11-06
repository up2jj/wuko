package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/up2jj/wuko/diagnostic"
	"github.com/up2jj/wuko/step"
)

// commitChecks applies findings to the visible snapshot and records them as this state's own
// writes, which its owner later replays into the parent. Writes are kept separately because a
// recheck may deliberately repeat an inherited value; both slices hold one finding per ID.
func commitChecks(state *State, findings []step.CheckFinding) {
	for _, finding := range findings {
		state.Checks = upsertCheck(state.Checks, finding)
		state.checkWrites = upsertCheck(state.checkWrites, finding)
	}
}

// upsertCheck replaces the finding with the same ID in place, keeping first-seen order.
func upsertCheck(findings []step.CheckFinding, finding step.CheckFinding) []step.CheckFinding {
	if index := slices.IndexFunc(findings, func(previous step.CheckFinding) bool { return previous.ID == finding.ID }); index >= 0 {
		findings[index] = finding
		return findings
	}
	return append(findings, finding)
}

func checkFailure(findings []step.CheckFinding) error {
	failed := 0
	for _, finding := range findings {
		if finding.Status == "fail" {
			failed++
		}
	}
	if failed == 0 {
		return nil
	}
	return fmt.Errorf("%d unresolved check(s) failed", failed)
}

// ownChecks returns the latest findings this run wrote, in snapshot order. Inherited findings
// (an inline callback's captured parent snapshot) belong to, and are decided by, their owner.
func ownChecks(state *State) []step.CheckFinding {
	var findings []step.CheckFinding
	for _, finding := range state.Checks {
		if slices.ContainsFunc(state.checkWrites, func(written step.CheckFinding) bool { return written.ID == finding.ID }) {
			findings = append(findings, finding)
		}
	}
	return findings
}

func reportedChecks(findings []step.CheckFinding, options Options) []step.CheckFinding {
	result := slices.Clone(findings)
	var sensitive []string
	if options.runtime != nil {
		sensitive = options.runtime.sensitive.snapshot()
	}
	redact := func(value string) string {
		for _, item := range sensitive {
			value = strings.ReplaceAll(value, item, diagnostic.Redacted)
		}
		return options.secretSession.Redact(value)
	}
	for i := range result {
		result[i].ID = redact(result[i].ID)
		result[i].Message = redact(result[i].Message)
		result[i].Fix = redact(result[i].Fix)
	}
	return result
}
