package tui

// The rail footer's commit marks: the newest moment this view observed each
// pipeline write. Marks are DERIVED, never stored and never parsed from log
// text -- the poller reads the journal activity delta and stamps it. The stamp
// is when this view saw the write, client display like every rendered line;
// the engine still holds no clock.

import (
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// psCommitMark is one pipeline's newest observed commit: the stamp the rail
// footer paints and the sequence that orders marks across a midnight wrap.
type psCommitMark struct {
	Stamp string // observed-at, HH:MM:SS, client display
	Seq   int64  // poll ordinal; HH:MM:SS wraps, this does not
}

// deriveCommits folds one poll's activity delta into the commit marks,
// returning the updated map. A pipeline the delta does not name keeps the mark
// it had; a group the engine could not attribute is skipped rather than
// credited to a pipeline that did not write it.
func deriveCommits(acc map[string]psCommitMark, delta []api.JournalActivityGroup, stamp string, seq int64) map[string]psCommitMark {
	if acc == nil {
		acc = map[string]psCommitMark{}
	}
	for _, g := range delta {
		if g.Pipeline == "" || g.Rows == 0 {
			continue
		}
		if was, seen := acc[g.Pipeline]; seen && was.Seq >= seq {
			continue
		}
		acc[g.Pipeline] = psCommitMark{Stamp: stamp, Seq: seq}
	}
	return acc
}

// commitStamp renders the observed-at stamp for this poll.
func commitStamp(t time.Time) string { return t.Format("15:04:05") }
