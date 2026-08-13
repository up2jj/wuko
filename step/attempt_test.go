package step

import "testing"

func TestApplyAttemptEnvironment(t *testing.T) {
	environment := map[string]string{AttemptEnv: "overridden"}
	environment = ApplyAttemptEnvironment(environment, Request{Attempt: 2, MaxAttempts: 4, OperationID: "operation-42"})
	if environment[AttemptEnv] != "2" || environment[MaxAttemptsEnv] != "4" || environment[OperationIDEnv] != "operation-42" {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestApplyAttemptEnvironmentUsesStandaloneDefaults(t *testing.T) {
	var environment map[string]string
	environment = ApplyAttemptEnvironment(environment, Request{})
	if environment[AttemptEnv] != "1" || environment[MaxAttemptsEnv] != "1" {
		t.Fatalf("environment = %#v", environment)
	}
}
