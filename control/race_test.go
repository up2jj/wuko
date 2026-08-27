package control

import (
	"context"
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
