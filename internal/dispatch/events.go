package dispatch

import "sync"

// This file is the leader's meta-change watermark: the in-process signal the lane
// loop parks on so an idle lane costs nothing between causes. Every meta mutation
// rides the single Dispatcher (dispatch.go), so one bump site covers every
// engine-visible cause -- apply, manual run, replay, drain, wipe, and the loop's
// own run transitions -- and the watermark needs no stored state, no polling
// query, and no clock. It is leader-term scoped exactly like the dispatcher that
// bumps it: a new term starts a fresh watermark, and a fresh leader's first pass
// runs unconditionally (no lane has passed yet), so nothing is missed across a
// failover.
//
// Bumps are LABELED with the pipelines the change touched. A lane cares about a
// set of names (its members and their upstreams); it wakes only when a cause
// touching one of those names lands, so an unrelated pipeline's run costs a
// parked lane nothing -- not a walk read, not a gate query. An UNLABELED bump is
// a global cause (graph change, operator surface): it makes every lane eligible,
// which over-wakes at worst one cheap pass and can never miss a change. The
// watermark still holds no consumer cursor: eligibility is a comparison of
// in-process counters, nothing stored.

// Events is a monotonic in-process change counter with per-name marks and a
// coalescing wake channel. Bump advances the sequence, stamps the named marks
// (or the global mark when unlabeled), and wakes any parked waiter; CareSeq
// reads the newest mark a care-set has seen; Wake exposes the wait channel. It
// is safe for concurrent use. The zero value is NOT ready; build it with
// NewEvents.
type Events struct {
	mu     sync.Mutex
	seq    uint64            // advances on every bump
	global uint64            // seq at the last UNLABELED bump (a cause for everyone)
	named  map[string]uint64 // seq at the last bump labeling each name
	wake   chan struct{}
}

// NewEvents builds a ready watermark at sequence zero.
func NewEvents() *Events {
	return &Events{named: map[string]uint64{}, wake: make(chan struct{}, 1)}
}

// Bump records a cause and wakes a parked waiter. Names label the pipelines the
// cause touched, so only lanes caring about one of them become eligible; no
// names is a global cause making every lane eligible. The wake send is
// non-blocking and coalescing: any number of bumps while no one waits leave one
// pending token, so a waiter never misses that something changed -- CareSeq
// carries what.
func (e *Events) Bump(names ...string) {
	e.mu.Lock()
	e.seq++
	if len(names) == 0 {
		e.global = e.seq
	} else {
		for _, n := range names {
			e.named[n] = e.seq
		}
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Seq returns the current sequence: the count of all bumps, labeled or not.
func (e *Events) Seq() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq
}

// CareSeq returns the newest mark visible to a care-set: the latest of the
// global mark and every named mark in cares. An empty care-set means "care
// about everything" and reads the full sequence, so a composition that never
// fills care-sets keeps today's wake-on-anything behavior. A lane whose last
// pass started at this value has seen every cause it can consume.
func (e *Events) CareSeq(cares []string) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(cares) == 0 {
		return e.seq
	}
	newest := e.global
	for _, n := range cares {
		if s := e.named[n]; s > newest {
			newest = s
		}
	}
	return newest
}

// Wake returns the coalescing wake channel. A receive consumes the pending
// token; the caller re-checks CareSeq after waking rather than counting
// receives.
func (e *Events) Wake() <-chan struct{} {
	return e.wake
}
