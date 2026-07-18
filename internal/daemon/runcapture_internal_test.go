package daemon

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// closableBuffer is an in-memory io.WriteCloser capturing what the runCapture
// writes.
type closableBuffer struct {
	bytes.Buffer
	closed bool
}

func (b *closableBuffer) Close() error { b.closed = true; return nil }

// fixedNow pins the capture clock so stamp lines are deterministic.
func fixedNow(c *runCapture) {
	c.now = func() time.Time { return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC) }
}

// TestRunCaptureUnframedPassthrough proves the default contract (no split, no
// stamp) is the legacy capture byte-for-byte: raw passthrough, no tags.
func TestRunCaptureUnframedPassthrough(t *testing.T) {
	buf := &closableBuffer{}
	c := newRunCapture(buf, "7", "quake_feed", false, false)

	in := "line one\npartial without newline"
	if _, err := c.Write([]byte(in)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := buf.String(); got != in {
		t.Errorf("unframed capture = %q, want the input verbatim %q", got, in)
	}
	if !buf.closed {
		t.Error("underlying file was not closed")
	}
}

// TestRunCaptureFramedLog proves split framing: stderr chunks are reassembled
// into L|-tagged lines, an unterminated tail flushes at close, and protocol
// frames land tagged by direction.
func TestRunCaptureFramedLog(t *testing.T) {
	buf := &closableBuffer{}
	c := newRunCapture(buf, "7", "quake_feed", true, false)

	// Chunked writes crossing line boundaries.
	for _, chunk := range []string{"first ", "line\nsec", "ond line\ntail"} {
		if _, err := c.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	c.EngineFrame(`{"event":"go","turn":1}`)
	c.PipelineFrame(`{"event":"done","turn":1}`)
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	want := []string{
		"", // the identity header, asserted by prefix below
		"L|first line",
		"L|second line",
		`>|{"event":"go","turn":1}`,
		`<|{"event":"done","turn":1}`,
		"L|tail", // the unterminated tail, flushed at close
	}
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("framed capture has %d lines %q, want %d", len(got), got, len(want))
	}
	if !strings.HasPrefix(got[0], `#|{"iris_log":1,"run":"7","pipeline":"quake_feed"`) {
		t.Errorf("header = %q, want the #| identity header", got[0])
	}
	for i := 1; i < len(want); i++ {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRunCaptureStamp proves the stamp contract: an open stamp with run
// identity and start, a close stamp with end and the recorded outcome, and the
// log lines framed between them.
func TestRunCaptureStamp(t *testing.T) {
	buf := &closableBuffer{}
	c := newRunCapture(buf, "42", "quake_report", false, true)
	fixedNow(c)

	if _, err := c.Write([]byte("working\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.SetOutcome("succeeded")
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stamped capture has %d lines %q, want 3", len(lines), lines)
	}
	// The open stamp is written before fixedNow pins the clock, so only its
	// non-time fields are asserted.
	if !strings.HasPrefix(lines[0], `#|{"iris_log":1,"run":"42","pipeline":"quake_report","started":`) {
		t.Errorf("open stamp = %q, want run identity and start", lines[0])
	}
	if lines[1] != "L|working" {
		t.Errorf("log line = %q, want L|working", lines[1])
	}
	if want := `#|{"ended":"2026-07-18T12:00:00Z","outcome":"succeeded"}`; lines[2] != want {
		t.Errorf("close stamp = %q, want %q", lines[2], want)
	}
}

// TestRunCaptureStampWithoutSplitDropsFrames proves stamp-only framing keeps
// protocol traffic out: frames are the split contract's, not the stamp's.
func TestRunCaptureStampWithoutSplitDropsFrames(t *testing.T) {
	buf := &closableBuffer{}
	c := newRunCapture(buf, "42", "p", false, true)
	c.EngineFrame(`{"event":"go","turn":1}`)
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if strings.Contains(buf.String(), "go") {
		t.Errorf("stamp-only capture recorded a frame: %q", buf.String())
	}
}

// TestRunCaptureNilSafety proves a nil capture (open failed; the uncaptured
// run) absorbs every call without panicking.
func TestRunCaptureNilSafety(t *testing.T) {
	var c *runCapture
	if _, err := c.Write([]byte("x")); err != nil {
		t.Errorf("nil write: %v", err)
	}
	c.EngineFrame("f")
	c.PipelineFrame("f")
	c.SetOutcome("succeeded")
	if err := c.Close(); err != nil {
		t.Errorf("nil close: %v", err)
	}
}

// TestTurnTranscriptFlush proves the loop-turn transcript buffers frames in
// order, flushes them through a split capture, and drops from the head past the
// cap with a truncation marker.
func TestTurnTranscriptFlush(t *testing.T) {
	t.Run("in-order", func(t *testing.T) {
		tr := &turnTranscript{}
		tr.EngineFrame(`{"event":"go","turn":9}`)
		tr.PipelineFrame(`{"event":"done","turn":9}`)

		buf := &closableBuffer{}
		c := newRunCapture(buf, "9", "p", true, false)
		tr.flushTo(c)
		_ = c.Close()

		want := ">|{\"event\":\"go\",\"turn\":9}\n<|{\"event\":\"done\",\"turn\":9}\n"
		if got := buf.String(); !strings.HasSuffix(got, want) || !strings.HasPrefix(got, "#|") {
			t.Errorf("flushed transcript = %q, want the #| header then %q", got, want)
		}
	})

	t.Run("truncates-head", func(t *testing.T) {
		tr := &turnTranscript{}
		line := strings.Repeat("x", 1024)
		for range 300 { // 300 KiB through a 256 KiB cap
			tr.EngineFrame(line)
		}
		buf := &closableBuffer{}
		c := newRunCapture(buf, "9", "p", true, false)
		tr.flushTo(c)
		_ = c.Close()

		if !strings.Contains(buf.String(), "transcript head truncated") {
			t.Error("dropped head is not marked in the flushed transcript")
		}
		if size := buf.Len(); size > turnTranscriptCap+4096 {
			t.Errorf("flushed transcript is %d bytes, want bounded near %d", size, turnTranscriptCap)
		}
	})

	t.Run("unsplit-capture-drops", func(t *testing.T) {
		tr := &turnTranscript{}
		tr.EngineFrame("frame")
		buf := &closableBuffer{}
		c := newRunCapture(buf, "9", "p", false, false)
		tr.flushTo(c)
		_ = c.Close()
		if strings.Contains(buf.String(), "frame") {
			t.Errorf("unsplit capture recorded transcript bytes: %q", buf.String())
		}
	})
}
