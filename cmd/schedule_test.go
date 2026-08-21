package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/up2jj/wuko/workflow"
)

func scheduledDefinition(cron string) *workflow.Definition {
	return &workflow.Definition{Version: 1, Name: "scheduled", Cron: cron, Timezone: "UTC", Steps: []workflow.Step{{ID: "run", Type: "test"}}}
}

func TestScheduledRunnerRepeatsAfterAttemptFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 0, 500_000_000, time.UTC)
	loads, attempts := 0, 0
	var stderr bytes.Buffer
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(_ context.Context, instant time.Time) error {
			now = instant
			return nil
		},
		load: func(context.Context) (*workflow.Definition, func(), error) {
			loads++
			return scheduledDefinition("* * * * * *"), func() {}, nil
		},
		execute: func(context.Context, *workflow.Definition) error {
			attempts++
			if attempts == 1 {
				return errors.New("boom")
			}
			cancel()
			return nil
		},
		stderr: &stderr,
	}
	if err := runner.run(ctx, scheduledDefinition("* * * * * *"), func() {}); err != nil {
		t.Fatal(err)
	}
	if loads != 1 || attempts != 2 {
		t.Fatalf("loads = %d, attempts = %d, want 1 and 2", loads, attempts)
	}
	if !strings.Contains(stderr.String(), "scheduled attempt failed: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduledRunnerRetriesAfterReloadFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 1, 500_000_000, time.UTC)
	loads := 0
	var stderr bytes.Buffer
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(_ context.Context, instant time.Time) error {
			now = instant
			return nil
		},
		load: func(context.Context) (*workflow.Definition, func(), error) {
			loads++
			if loads == 1 {
				return nil, func() {}, errors.New("unavailable")
			}
			return scheduledDefinition("*/2 * * * * *"), func() {}, nil
		},
		execute: func(context.Context, *workflow.Definition) error {
			cancel()
			return nil
		},
		stderr: &stderr,
	}
	if err := runner.run(ctx, scheduledDefinition("*/2 * * * * *"), func() {}); err != nil {
		t.Fatal(err)
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2", loads)
	}
	if !strings.Contains(stderr.String(), "scheduled reload failed: unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestScheduledRunnerAdoptsChangedScheduleAndSkipsOldOccurrence(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 0, 500_000_000, time.UTC)
	var waits []time.Time
	loads, attempts := 0, 0
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(_ context.Context, instant time.Time) error {
			waits = append(waits, instant)
			now = instant
			return nil
		},
		load: func(context.Context) (*workflow.Definition, func(), error) {
			loads++
			return scheduledDefinition("*/2 * * * * *"), func() {}, nil
		},
		execute: func(context.Context, *workflow.Definition) error {
			attempts++
			cancel()
			return nil
		},
		stderr: new(bytes.Buffer),
	}
	if err := runner.run(ctx, scheduledDefinition("1-59/2 * * * * *"), func() {}); err != nil {
		t.Fatal(err)
	}
	if loads != 2 || attempts != 1 {
		t.Fatalf("loads = %d, attempts = %d, want 2 and 1", loads, attempts)
	}
	if len(waits) != 2 || waits[0].Second() != 1 || waits[1].Second() != 2 {
		t.Fatalf("waits = %v, want seconds 1 and 2", waits)
	}
}

func TestScheduledRunnerSkipsOccurrencesMissedDuringAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	var waitedFor time.Time
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(_ context.Context, instant time.Time) error {
			waitedFor = instant
			cancel()
			return context.Canceled
		},
		load: func(context.Context) (*workflow.Definition, func(), error) {
			t.Fatal("unexpected reload")
			return nil, nil, nil
		},
		execute: func(context.Context, *workflow.Definition) error {
			now = now.Add(5 * time.Second)
			return nil
		},
		stderr: new(bytes.Buffer),
	}
	if err := runner.run(ctx, scheduledDefinition("* * * * * *"), func() {}); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.August, 21, 10, 0, 6, 0, time.UTC)
	if !waitedFor.Equal(want) {
		t.Fatalf("waited for %s, want %s", waitedFor, want)
	}
}

func TestScheduledRunnerKeepsLastScheduleWhenCronIsRemoved(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	now := time.Date(2026, time.August, 21, 10, 0, 1, 500_000_000, time.UTC)
	loads := 0
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(_ context.Context, instant time.Time) error {
			now = instant
			return nil
		},
		load: func(context.Context) (*workflow.Definition, func(), error) {
			loads++
			if loads == 1 {
				return scheduledDefinition(""), func() {}, nil
			}
			return scheduledDefinition("*/2 * * * * *"), func() {}, nil
		},
		execute: func(context.Context, *workflow.Definition) error {
			cancel()
			return nil
		},
		stderr: new(bytes.Buffer),
	}
	if err := runner.run(ctx, scheduledDefinition("*/2 * * * * *"), func() {}); err != nil {
		t.Fatal(err)
	}
	if loads != 2 {
		t.Fatalf("loads = %d, want 2", loads)
	}
}

func TestScheduledRunnerCancellationWhileWaitingIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	now := time.Date(2026, time.August, 21, 10, 0, 1, 500_000_000, time.UTC)
	cleanups := 0
	runner := scheduledRunner{
		now: func() time.Time { return now },
		wait: func(context.Context, time.Time) error {
			cancel()
			return context.Canceled
		},
		stderr: new(bytes.Buffer),
	}
	if err := runner.run(ctx, scheduledDefinition("*/2 * * * * *"), func() { cleanups++ }); err != nil {
		t.Fatal(err)
	}
	if cleanups != 1 {
		t.Fatalf("cleanups = %d, want 1", cleanups)
	}
}
