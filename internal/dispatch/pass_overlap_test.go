package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// signalWalk wraps a WalkReader and signals every read, so a test can wait for
// the reconcile that follows a watermark bump instead of sleeping for it.
type signalWalk struct {
	inner dispatch.WalkReader
	reads chan struct{}
}

func (w signalWalk) Walk(ctx context.Context) ([]dispatch.Lane, error) {
	lanes, err := w.inner.Walk(ctx)
	select {
	case w.reads <- struct{}{}:
	default:
	}
	return lanes, err
}

// TestLaneRenameNeverOverlapsMember proves the running-pass member guard: a
// pipeline's lane identity changes between walk reads (mid-apply, a pipeline is
// its own anonymous lane until the composer row lands) while its pass is still
// in flight, and the recomposed lane must NOT spawn until that pass ends — two
// concurrent passes over one pipeline would drive the same resident session and
// interleave the turn protocol (the quake_feed dead-letter).
func TestLaneRenameNeverOverlapsMember(t *testing.T) {
	t.Run("lane-rename-never-overlaps-member", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		events := dispatch.NewEvents()
		runner := newFakeRunner()
		runner.entered = make(chan string)
		runner.release["p1"] = make(chan struct{})
		inner := newFakeWalk(dispatch.Lane{Name: "p1", Pipelines: []string{"p1"}})
		reads := make(chan struct{}, 8)
		hook, reports := passReports(t)
		loop := dispatch.NewLoop(
			signalWalk{inner: inner, reads: reads},
			newFakeGate(), runner, nil,
			dispatch.WithOnPass(hook), dispatch.WithEvents(events),
		)
		done := make(chan struct{})
		go func() { defer close(done); _ = loop.Run(ctx) }()
		defer func() { cancel(); <-done }()

		// p1's anonymous-lane pass is in flight, blocked inside StartFresh.
		if got := nextStart(ctx, t, runner); got != "p1" {
			t.Fatalf("first start = %q, want p1", got)
		}

		// Mid-flight the walk recomposes: p1 now rides lane "healthy". Drain the
		// walk-read signals, land the cause, then wait for the reconcile that
		// re-reads the walk — the exact moment a memberless guard would spawn
		// "healthy" beside the still-running anonymous pass.
		for len(reads) > 0 {
			<-reads
		}
		inner.set(dispatch.Lane{Name: "healthy", Pipelines: []string{"p1", "p2"}})
		events.Bump()
		select {
		case <-reads:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for the post-bump reconcile: %v", ctx.Err())
		}

		// The guard must have skipped "healthy": no second start may enter while
		// p1's pass still holds its session. (Bounded negative check: an
		// overlapping pass would send its entered signal here.)
		select {
		case got := <-runner.entered:
			t.Fatalf("lane pass overlap: %q started while p1's pass was still in flight", got)
		case <-time.After(50 * time.Millisecond):
		}

		// The anonymous pass ends; at that boundary "healthy" spawns and walks
		// its members serially.
		runner.release["p1"] <- struct{}{}
		if r := nextReport(ctx, t, reports); len(r.Started) != 1 || r.Started[0] != "p1" {
			t.Fatalf("anonymous pass report started = %v, want [p1]", r.Started)
		}
		if got := nextStart(ctx, t, runner); got != "p1" {
			t.Fatalf("healthy pass first start = %q, want p1", got)
		}
		runner.release["p1"] <- struct{}{}
		if got := nextStart(ctx, t, runner); got != "p2" {
			t.Fatalf("healthy pass second start = %q, want p2", got)
		}
		if r := nextReport(ctx, t, reports); len(r.Started) != 2 {
			t.Fatalf("healthy pass report started = %v, want [p1 p2]", r.Started)
		}

		cancel()
		<-done
		if n := runner.count("p1"); n != 2 {
			t.Fatalf("p1 dispatched %d times, want exactly 2 (anonymous pass, then the recomposed lane after the boundary)", n)
		}
	})
}
