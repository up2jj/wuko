package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/up2jj/wuko/step"
)

func TestBackgroundSupervisorSealsOnlyAutoStopServices(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	autoStopped := make(chan struct{})
	keeperStopped := make(chan struct{})
	if err := supervisor.StartService("auto", "process", step.ServiceOptions{}, func(ctx context.Context) error {
		<-ctx.Done()
		close(autoStopped)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.StartService("keeper", "process", step.ServiceOptions{KeepAlive: true}, func(ctx context.Context) error {
		<-ctx.Done()
		close(keeperStopped)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	supervisor.seal()
	select {
	case <-autoStopped:
	case <-time.After(time.Second):
		t.Fatal("auto-stop service survived seal")
	}
	select {
	case <-keeperStopped:
		t.Fatal("keep-alive service was canceled by seal")
	default:
	}
	supervisor.stop(errBackgroundStopped)
	if err := supervisor.wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-keeperStopped:
	default:
		t.Fatal("keep-alive service survived scope stop")
	}
}

func TestBackgroundSupervisorDeferredFailuresDoNotCancelIndependentWork(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	failure := errors.New("deferred failure")
	failed := make(chan struct{})
	if err := supervisor.StartService("failed", "process", step.ServiceOptions{KeepAlive: true}, func(context.Context) error {
		close(failed)
		return failure
	}); err != nil {
		t.Fatal(err)
	}
	siblingStopped := make(chan struct{})
	if err := supervisor.StartService("sibling", "process", step.ServiceOptions{KeepAlive: true}, func(ctx context.Context) error {
		<-ctx.Done()
		close(siblingStopped)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-failed
	select {
	case <-supervisor.context().Done():
		t.Fatal("deferred failure canceled the scope")
	default:
	}
	select {
	case <-siblingStopped:
		t.Fatal("deferred failure canceled an independent service")
	default:
	}
	supervisor.seal()
	supervisor.stop(errBackgroundStopped)
	err := supervisor.wait()
	if !errors.Is(err, failure) {
		t.Fatalf("joined error = %v", err)
	}
}

func TestBackgroundSupervisorConcurrentMixedPolicyStress(t *testing.T) {
	const rounds = 50
	const jobs = 48
	for round := range rounds {
		supervisor := newBackgroundSupervisor(t.Context())
		gate := make(chan struct{})
		results := make(chan error, jobs)
		var starters sync.WaitGroup
		starters.Add(jobs)
		for index := range jobs {
			go func() {
				defer starters.Done()
				options := step.ServiceOptions{KeepAlive: index%3 == 0, FailFast: false}
				results <- supervisor.StartService(fmt.Sprintf("%d-%d", round, index), "process", options, func(ctx context.Context) error {
					select {
					case <-gate:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
		}
		supervisor.seal()
		starters.Wait()
		close(results)
		close(gate)
		supervisor.stop(errBackgroundStopped)
		if err := supervisor.wait(); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		registered := 0
		for err := range results {
			if err == nil {
				registered++
			}
		}
		if registered != len(supervisor.jobs) {
			t.Fatalf("round %d registered=%d jobs=%d", round, registered, len(supervisor.jobs))
		}
	}
}

func TestBackgroundSupervisorLifecycleShutdownDoesNotEndTheScope(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	if err := supervisor.StartService("service", "process", step.ServiceOptions{ExitOnEnd: true}, func(ctx context.Context) error {
		<-ctx.Done()
		// A lifecycle-stopped service reports a clean shutdown, not a terminal exit.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	supervisor.seal()
	if err := supervisor.wait(); err != nil {
		t.Fatal(err)
	}
	if supervisor.endedScope() {
		t.Fatal("lifecycle shutdown was reported as a service ending its scope")
	}
}

func TestBackgroundSupervisorIgnoresServicesThatNeverStarted(t *testing.T) {
	supervisor := newBackgroundSupervisor(t.Context())
	options := step.ServiceOptions{FailFast: true, ExitOnEnd: true}
	if err := supervisor.StartService("service", "process", options, func(context.Context) error {
		return fmt.Errorf("%w: readiness never matched", step.ErrServiceAborted)
	}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.wait(); err != nil {
		t.Fatalf("aborted service was reported by the scope: %v", err)
	}
	if supervisor.endedScope() {
		t.Fatal("aborted service ended its scope")
	}
	if err := supervisor.context().Err(); err != nil {
		t.Fatalf("aborted service canceled its scope: %v", err)
	}
}
