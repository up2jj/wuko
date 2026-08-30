package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackgroundSupervisorJoinsInRegistrationOrder(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	first := errors.New("first failed")
	second := errors.New("second failed")
	release := make(chan struct{})
	for _, item := range []struct {
		id  string
		err error
	}{{"one", first}, {"two", second}} {
		id, jobErr := item.id, item.err
		if err := supervisor.start(id, "test", func(context.Context) error {
			<-release
			return jobErr
		}); err != nil {
			t.Fatal(err)
		}
	}
	supervisor.seal()
	close(release)
	err := supervisor.wait()
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("joined error = %v", err)
	}
	if strings.Index(err.Error(), "one") > strings.Index(err.Error(), "two") {
		t.Fatalf("errors are not in registration order: %v", err)
	}
	if err := supervisor.start("late", "test", func(context.Context) error { return nil }); err == nil {
		t.Fatal("expected registration after seal to fail")
	}
}

func TestBackgroundSupervisorCancellationIsIdempotent(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	started := make(chan struct{})
	if err := supervisor.start("worker", "test", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	supervisor.stop(errBackgroundStopped)
	supervisor.stop(errBackgroundStopped)
	supervisor.seal()
	if err := supervisor.wait(); err != nil {
		t.Fatalf("lifecycle cancellation leaked as failure: %v", err)
	}
	if err := supervisor.wait(); err != nil {
		t.Fatalf("second wait = %v", err)
	}
}

func TestBackgroundSupervisorConcurrentRegistration(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	var workers sync.WaitGroup
	for index := range 32 {
		workers.Go(func() {
			_ = supervisor.start(string(rune('a'+index)), "test", func(context.Context) error { return nil })
		})
	}
	workers.Wait()
	supervisor.seal()
	if err := supervisor.wait(); err != nil {
		t.Fatal(err)
	}
	if len(supervisor.jobs) != 32 {
		t.Fatalf("jobs = %d, want 32", len(supervisor.jobs))
	}
}

func TestBackgroundSupervisorRegisterSealRace(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	start := make(chan struct{})
	results := make(chan error, 64)
	var workers sync.WaitGroup
	for index := range 64 {
		workers.Go(func() {
			<-start
			results <- supervisor.start(fmt.Sprintf("job-%02d", index), "test", func(context.Context) error { return nil })
		})
	}
	close(start)
	supervisor.seal()
	workers.Wait()
	close(results)
	registered := 0
	for err := range results {
		if err == nil {
			registered++
		}
	}
	if err := supervisor.wait(); err != nil {
		t.Fatal(err)
	}
	if len(supervisor.jobs) != registered {
		t.Fatalf("registered jobs = %d, successful calls = %d", len(supervisor.jobs), registered)
	}
}

func TestBackgroundSupervisorFirstFailureCancelsSiblings(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	fatal := errors.New("observer failed")
	release := make(chan struct{})
	if err := supervisor.start("fatal", "watch", func(context.Context) error {
		<-release
		return fatal
	}); err != nil {
		t.Fatal(err)
	}
	siblingCanceled := make(chan struct{})
	if err := supervisor.start("sibling", "watch", func(ctx context.Context) error {
		<-ctx.Done()
		close(siblingCanceled)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	supervisor.seal()
	close(release)
	<-siblingCanceled
	err := supervisor.wait()
	if !errors.Is(err, fatal) {
		t.Fatalf("joined error = %v", err)
	}
	if strings.Contains(err.Error(), "sibling") {
		t.Fatalf("expected sibling cancellation to be suppressed: %v", err)
	}
	if !errors.Is(context.Cause(supervisor.context()), fatal) {
		t.Fatalf("cancel cause = %v", context.Cause(supervisor.context()))
	}
}

func TestBackgroundSupervisorExternalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	supervisor := newBackgroundSupervisor(ctx)
	if err := supervisor.start("worker", "test", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	supervisor.seal()
	cancel()
	if err := supervisor.wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	if err := supervisor.start("late", "test", func(context.Context) error { return nil }); err == nil {
		t.Fatal("expected registration after cancellation to fail")
	}
}

func TestBackgroundSupervisorCountsOnlyRunningJobs(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	release := make(chan struct{})
	if err := supervisor.start("short", "test", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.start("long", "test", func(context.Context) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for supervisor.count() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("count = %d after the short job finished, want 1", supervisor.count())
		}
		time.Sleep(time.Millisecond)
	}
	supervisor.seal()
	close(release)
	if err := supervisor.wait(); err != nil {
		t.Fatal(err)
	}
	if count := supervisor.count(); count != 0 {
		t.Fatalf("count = %d after join, want 0", count)
	}
}
