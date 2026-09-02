package control

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRaceDeterministicWinner(t *testing.T) {
	release := make(chan struct{})
	started := make(chan int, 2)
	done := make(chan struct{})
	var winner int
	var raceErr error
	go func() {
		winner, raceErr = Race(t.Context(), 2, func(ctx context.Context, index int) bool {
			started <- index
			if index == 0 {
				<-release
				return true
			}
			<-ctx.Done()
			return true
		})
		close(done)
	}()
	<-started
	<-started
	close(release)
	<-done
	if raceErr != nil {
		t.Fatal(raceErr)
	}
	if winner != 0 {
		t.Fatalf("winner = %d, want 0", winner)
	}
}

func TestRaceParentCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	winner, err := Race(ctx, 1, func(context.Context, int) bool { return true })
	if winner != -1 || err != context.Canceled {
		t.Fatalf("Race() = (%d, %v), want (-1, context canceled)", winner, err)
	}
}

func TestRaceContainsParticipantPanic(t *testing.T) {
	observed := make(chan struct{})
	winner, err := Race(t.Context(), 2, func(ctx context.Context, index int) bool {
		if index == 0 {
			panic("participant exploded")
		}
		<-ctx.Done()
		close(observed)
		return false
	})
	<-observed
	if winner != -1 {
		t.Fatalf("winner = %d, want -1", winner)
	}
	if err == nil || !strings.Contains(err.Error(), "participant exploded") {
		t.Fatalf("error = %v, want the recovered panic", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("a panic must not be classified as a cancellation")
	}
}

func TestRacePanicDoesNotOutrankParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	winner, err := Race(ctx, 1, func(context.Context, int) bool {
		cancel()
		panic("participant exploded")
	})
	if winner != -1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("Race() = (%d, %v), want (-1, context canceled)", winner, err)
	}
}
