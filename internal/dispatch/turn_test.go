package dispatch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The turn protocol model is pure, so these tests drive the codec and the
// collector's frame discipline with literal wire lines: every violation class
// the epic names (unparseable line, row outside declared writes, frame after
// terminal, wrong turn echo) and the two terminal shapes.

// testWrites is the declared-writes surface the collector tests enforce against.
func testWrites() WriteSet {
	return WriteSet{"marts.daily": {"day": true, "sum": true}}
}

func TestEncodeFrames(t *testing.T) {
	if got := EncodeGoFrame(841); got != `{"event":"go","turn":841}` {
		t.Fatalf("go frame: %s", got)
	}
	if got := EncodeRunFrame(); got != `{"event":"run"}` {
		t.Fatalf("run frame: %s", got)
	}
	row, err := EncodeRowFrame("raw.orders", json.RawMessage(`{"id":9,"total":40}`))
	if err != nil {
		t.Fatalf("encode row frame: %v", err)
	}
	if row != `{"event":"row","table":"raw.orders","row":{"id":9,"total":40}}` {
		t.Fatalf("row frame: %s", row)
	}
	// Every engine frame must be one line: the protocol is JSON Lines.
	for _, f := range []string{EncodeGoFrame(1), EncodeRunFrame(), row} {
		if strings.ContainsRune(f, '\n') {
			t.Fatalf("frame is not one line: %q", f)
		}
	}
}

func TestTurnCollectorDone(t *testing.T) {
	c := NewTurnCollector(841, testWrites())
	if _, terminal, err := c.Feed(`{"event":"row","table":"marts.daily","row":{"day":"2026-07-17","sum":52}}`); err != nil || terminal {
		t.Fatalf("row feed: terminal=%v err=%v", terminal, err)
	}
	end, terminal, err := c.Feed(`{"event":"done","turn":841}`)
	if err != nil || !terminal || end.Errored {
		t.Fatalf("done feed: end=%+v terminal=%v err=%v", end, terminal, err)
	}
	rows := c.Rows()
	if len(rows) != 1 || rows[0].Table != "marts.daily" || string(rows[0].Row) != `{"day":"2026-07-17","sum":52}` {
		t.Fatalf("collected rows: %+v", rows)
	}
}

func TestTurnCollectorErrorTerminal(t *testing.T) {
	c := NewTurnCollector(7, testWrites())
	end, terminal, err := c.Feed(`{"event":"error","turn":7,"reason":"upstream gone","detail":{"code":3}}`)
	if err != nil || !terminal {
		t.Fatalf("error feed: terminal=%v err=%v", terminal, err)
	}
	if !end.Errored || end.Reason != "upstream gone" || string(end.Detail) != `{"code":3}` {
		t.Fatalf("error end: %+v", end)
	}
}

func TestTurnCollectorErrorReasonDefaults(t *testing.T) {
	c := NewTurnCollector(7, testWrites())
	end, terminal, err := c.Feed(`{"event":"error","turn":7}`)
	if err != nil || !terminal || !end.Errored || end.Reason == "" {
		t.Fatalf("bare error feed: end=%+v terminal=%v err=%v", end, terminal, err)
	}
}

func TestTurnCollectorViolations(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"unparseable", `done 0`},
		{"unknown event", `{"event":"bark"}`},
		{"undeclared table", `{"event":"row","table":"raw.other","row":{"day":"x"}}`},
		{"undeclared field", `{"event":"row","table":"marts.daily","row":{"day":"x","hacked":1}}`},
		{"row not an object", `{"event":"row","table":"marts.daily","row":[1,2]}`},
		{"empty row object", `{"event":"row","table":"marts.daily","row":{}}`},
		{"row without table", `{"event":"row","row":{"day":"x"}}`},
		{"wrong turn echo", `{"event":"done","turn":840}`},
		{"terminal without turn", `{"event":"done"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewTurnCollector(841, testWrites())
			_, terminal, err := c.Feed(tc.line)
			if terminal {
				t.Fatalf("violation reported terminal")
			}
			var fe *FrameError
			if !errors.As(err, &fe) {
				t.Fatalf("want *FrameError, got %v", err)
			}
			if fe.Line != tc.line {
				t.Fatalf("violation does not quote the offending line: %q", fe.Line)
			}
		})
	}
}

func TestTurnCollectorCall(t *testing.T) {
	c := NewTurnCollector(5, testWrites())
	if _, terminal, err := c.Feed(`{"event":"call","id":"c1","verb":"mail.send","args":{"to":"ops@example.com"}}`); err != nil || terminal {
		t.Fatalf("call feed: terminal=%v err=%v", terminal, err)
	}
	call, ok := c.TakeCall()
	if !ok {
		t.Fatal("TakeCall found no pending call after a call frame")
	}
	if call.ID != "c1" || call.Verb != "mail.send" || string(call.Args) != `{"to":"ops@example.com"}` {
		t.Fatalf("call = %+v", call)
	}
	if _, ok := c.TakeCall(); ok {
		t.Fatal("TakeCall did not clear the pending call")
	}
	// The turn proceeds normally after the call.
	if _, terminal, err := c.Feed(`{"event":"done","turn":5}`); err != nil || !terminal {
		t.Fatalf("done after call: terminal=%v err=%v", terminal, err)
	}
}

func TestTurnCollectorCallViolations(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"no id", `{"event":"call","verb":"mail.send","args":{}}`},
		{"no verb", `{"event":"call","id":"c1"}`},
		{"unqualified verb", `{"event":"call","id":"c1","verb":"send"}`},
		{"empty alias", `{"event":"call","id":"c1","verb":".send"}`},
		{"empty verb half", `{"event":"call","id":"c1","verb":"mail."}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewTurnCollector(5, testWrites())
			var fe *FrameError
			if _, _, err := c.Feed(tc.line); !errors.As(err, &fe) {
				t.Fatalf("want *FrameError, got %v", err)
			}
		})
	}

	t.Run("call while outstanding", func(t *testing.T) {
		c := NewTurnCollector(5, testWrites())
		if _, _, err := c.Feed(`{"event":"call","id":"c1","verb":"mail.send"}`); err != nil {
			t.Fatalf("first call: %v", err)
		}
		var fe *FrameError
		if _, _, err := c.Feed(`{"event":"call","id":"c2","verb":"mail.send"}`); !errors.As(err, &fe) {
			t.Fatalf("second call while outstanding: want *FrameError, got %v", err)
		}
	})
}

func TestEncodeResFrames(t *testing.T) {
	ok, err := EncodeResOKFrame("c1", json.RawMessage(`{"message_id":"m9"}`))
	if err != nil {
		t.Fatalf("encode ok frame: %v", err)
	}
	if ok != `{"event":"res","id":"c1","ok":{"message_id":"m9"}}` {
		t.Fatalf("ok frame: %s", ok)
	}
	empty, err := EncodeResOKFrame("c1", nil)
	if err != nil {
		t.Fatalf("encode empty ok frame: %v", err)
	}
	if empty != `{"event":"res","id":"c1","ok":{}}` {
		t.Fatalf("empty ok frame: %s", empty)
	}
	if got := EncodeResErrFrame("c1", `plugin "mail" timed out`); got != `{"event":"res","id":"c1","err":"plugin \"mail\" timed out"}` {
		t.Fatalf("err frame: %s", got)
	}
	for _, f := range []string{ok, empty, EncodeResErrFrame("c1", "x\ny")} {
		if strings.ContainsRune(f, '\n') {
			t.Fatalf("res frame is not one line: %q", f)
		}
	}
}

func TestTurnCollectorFrameAfterTerminal(t *testing.T) {
	c := NewTurnCollector(3, testWrites())
	if _, terminal, err := c.Feed(`{"event":"done","turn":3}`); err != nil || !terminal {
		t.Fatalf("done feed: terminal=%v err=%v", terminal, err)
	}
	var fe *FrameError
	if _, _, err := c.Feed(`{"event":"done","turn":3}`); !errors.As(err, &fe) {
		t.Fatalf("frame after terminal: want *FrameError, got %v", err)
	}
}
