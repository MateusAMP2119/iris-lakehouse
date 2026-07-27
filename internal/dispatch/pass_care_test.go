package dispatch_test

import (
	"context"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// TestEventsCareSeq pins the labeled watermark's read semantics: a named bump
// moves only its carers' view, an unlabeled bump moves everyone's, and an empty
// care-set reads the full sequence (care about everything).
func TestEventsCareSeq(t *testing.T) {
	e := dispatch.NewEvents()
	if got := e.CareSeq([]string{"a"}); got != 0 {
		t.Fatalf("fresh CareSeq = %d, want 0", got)
	}

	e.Bump("a")
	if got := e.CareSeq([]string{"a"}); got == 0 {
		t.Fatal("carer of a did not see Bump(a)")
	}
	if got := e.CareSeq([]string{"b"}); got != 0 {
		t.Fatalf("carer of b saw Bump(a): CareSeq = %d, want 0", got)
	}
	if got := e.CareSeq(nil); got != e.Seq() {
		t.Fatalf("empty care-set reads %d, want full sequence %d", got, e.Seq())
	}

	before := e.CareSeq([]string{"b"})
	e.Bump() // unlabeled: a global cause
	if got := e.CareSeq([]string{"b"}); got == before {
		t.Fatal("carer of b did not see the unlabeled bump")
	}
}

// TestLabeledCauseWakesOnlyCaringLane proves the scoped park: two parked lanes,
// a bump labeled for one -- only that lane re-passes; the other never leaves its
// park, paying nothing for the unrelated cause. An unlabeled bump then wakes
// both, the over-waking-is-safe default.
func TestLabeledCauseWakesOnlyCaringLane(t *testing.T) {
	t.Run("labeled-cause-wakes-only-caring-lane", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		events := dispatch.NewEvents()
		runner := newFakeRunner()
		hook, reports := passReports(t)
		loop := dispatch.NewLoop(
			newFakeWalk(
				dispatch.Lane{Name: "la", Pipelines: []string{"a"}, Cares: []string{"a"}},
				dispatch.Lane{Name: "lb", Pipelines: []string{"b"}, Cares: []string{"b"}},
			),
			newFakeGate(), runner, nil,
			dispatch.WithOnPass(hook), dispatch.WithEvents(events),
		)
		done := make(chan struct{})
		go func() { defer close(done); _ = loop.Run(ctx) }()
		defer func() { cancel(); <-done }()

		// First passes: both lanes run unconditionally (never passed this term).
		first := map[string]bool{}
		for range 2 {
			first[nextReport(ctx, t, reports).Lane] = true
		}
		if !first["la"] || !first["lb"] {
			t.Fatalf("first passes covered %v, want both la and lb", first)
		}

		// A cause labeled for a: exactly lane la re-passes.
		events.Bump("a")
		if r := nextReport(ctx, t, reports); r.Lane != "la" {
			t.Fatalf("labeled cause woke lane %q, want la", r.Lane)
		}

		// An unlabeled (global) cause: both lanes re-pass.
		events.Bump()
		woken := map[string]bool{}
		for range 2 {
			woken[nextReport(ctx, t, reports).Lane] = true
		}
		if !woken["la"] || !woken["lb"] {
			t.Fatalf("global cause woke %v, want both la and lb", woken)
		}

		cancel()
		<-done
		// b ran its first pass and the global-cause pass only: the labeled cause
		// for a never touched it.
		if n := runner.count("b"); n != 2 {
			t.Fatalf("b dispatched %d times, want exactly 2 (first pass + global cause; a's cause must not wake lb)", n)
		}
		if n := runner.count("a"); n != 3 {
			t.Fatalf("a dispatched %d times, want exactly 3 (first pass + labeled cause + global cause)", n)
		}
	})
}

// TestCareSetSpansUpstream proves a cross-lane dependent wakes to its upstream's
// labeled cause: lane lb's care-set names upstream a (as the walk reader builds
// it from the dependency edges), so a cause labeled a re-passes lb even though a
// is not a member.
func TestCareSetSpansUpstream(t *testing.T) {
	t.Run("care-set-spans-upstream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		events := dispatch.NewEvents()
		runner := newFakeRunner()
		hook, reports := passReports(t)
		loop := dispatch.NewLoop(
			newFakeWalk(dispatch.Lane{Name: "lb", Pipelines: []string{"b"}, Cares: []string{"b", "a"}}),
			newFakeGate(), runner, nil,
			dispatch.WithOnPass(hook), dispatch.WithEvents(events),
		)
		done := make(chan struct{})
		go func() { defer close(done); _ = loop.Run(ctx) }()
		defer func() { cancel(); <-done }()

		nextReport(ctx, t, reports) // unconditional first pass

		// Upstream a's cause (its run record, its fresh source bytes) wakes the
		// dependent's lane.
		events.Bump("a")
		if r := nextReport(ctx, t, reports); r.Lane != "lb" {
			t.Fatalf("upstream cause woke lane %q, want lb", r.Lane)
		}
	})
}
