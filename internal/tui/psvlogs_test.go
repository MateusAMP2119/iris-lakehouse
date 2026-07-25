package tui

import (
	"reflect"
	"testing"
)

// TestHumanizeCapture proves the logs pane's frame humanizer: protocol frames
// render compact, consecutive row frames fold per origin and table with an
// occurred_at range, bare log lines and unparseable payloads ride verbatim.
func TestHumanizeCapture(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "row-burst-folds-with-time-range",
			in: []string{
				`[engine] {"event":"go","turn":160}`,
				`[engine] {"event":"row","table":"demo.quakes","row":{"id":"a1","occurred_at":"2026-07-24T11:51:28.65+01:00"}}`,
				`[engine] {"event":"row","table":"demo.quakes","row":{"id":"a2","occurred_at":"2026-07-24T10:44:35.843+01:00"}}`,
				`[engine] {"event":"run"}`,
			},
			want: []string{
				"[engine] go · turn 160",
				"[engine] 2 rows → demo.quakes (11:51:28 … 10:44:35)",
				"[engine] run",
			},
		},
		{
			name: "fold-breaks-on-origin-and-table",
			in: []string{
				`[engine] {"event":"row","table":"demo.quakes","row":{"id":"a1"}}`,
				`[pipeline] {"event":"row","table":"demo.report","row":{"metric":"m","value":"1"}}`,
				`[pipeline] {"event":"row","table":"demo.report","row":{"metric":"n","value":"2"}}`,
				`[pipeline] {"event":"done","turn":160}`,
			},
			want: []string{
				"[engine] 1 row → demo.quakes",
				"[pipeline] 2 rows → demo.report",
				"[pipeline] done · turn 160",
			},
		},
		{
			name: "log-lines-break-a-fold-and-ride-bare",
			in: []string{
				`[engine] {"event":"row","table":"t.a","row":{}}`,
				`plain pipeline log line`,
				`[engine] {"event":"row","table":"t.a","row":{}}`,
			},
			want: []string{
				"[engine] 1 row → t.a",
				"plain pipeline log line",
				"[engine] 1 row → t.a",
			},
		},
		{
			name: "stamps-render-open-and-close",
			in: []string{
				`[iris] {"iris_log":1,"run":"321","pipeline":"quake_report","started":"2026-07-25T09:40:12Z"}`,
				`[iris] {"ended":"2026-07-25T09:43:19Z","outcome":"succeeded"}`,
			},
			want: []string{
				"[iris] run 321 · quake_report · started 09:40:12",
				"[iris] ended 09:43:19 · succeeded",
			},
		},
		{
			name: "calls-error-and-res-render-compact",
			in: []string{
				`[pipeline] {"event":"call","call":1,"verb":"mail.send","args":{"to":"x@y.z"}}`,
				`[engine] {"event":"res","call":1,"ok":true,"result":{}}`,
				`[engine] {"event":"res","call":2,"ok":false,"error":"timeout"}`,
				`[pipeline] {"event":"error","turn":9,"reason":"bad input"}`,
			},
			want: []string{
				"[pipeline] call 1 · mail.send",
				"[engine] res call 1 · ok",
				"[engine] res call 2 · error: timeout",
				"[pipeline] error · turn 9 · bad input",
			},
		},
		{
			name: "unparseable-and-unknown-payloads-ride-verbatim",
			in: []string{
				`[engine] not json at all`,
				`[engine] {"iris":"transcript head truncated"}`,
				`[pipeline] {"event":"mystery"}`,
			},
			want: []string{
				`[engine] not json at all`,
				`[engine] {"iris":"transcript head truncated"}`,
				`[pipeline] {"event":"mystery"}`,
			},
		},
		{
			name: "trailing-fold-flushes",
			in: []string{
				`[engine] {"event":"row","table":"t.a","row":{"occurred_at":"2026-07-24T11:51:28Z"}}`,
			},
			want: []string{
				"[engine] 1 row → t.a (11:51:28)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := humanizeCapture(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("humanizeCapture:\n got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}
