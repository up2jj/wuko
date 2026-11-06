package step

// CheckFinding is the latest observation of one named workflow check.
type CheckFinding struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// CheckValues exposes findings as ordinary workflow data, including an empty array.
func CheckValues(findings []CheckFinding) []any {
	values := make([]any, len(findings))
	for i, finding := range findings {
		values[i] = map[string]any{"id": finding.ID, "status": finding.Status, "message": finding.Message, "fix": finding.Fix}
	}
	return values
}

// CheckSchema describes one finding in both step outputs and the checks root.
func CheckSchema() OutputSchema {
	return Array(ClosedOutputs("id", "status", "message", "fix"))
}
