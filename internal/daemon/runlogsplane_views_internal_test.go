package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
)

// viewsTestPlane builds a run-logs plane over a temp engine home holding one
// capture file for run 7 with the given content.
func viewsTestPlane(t *testing.T, content string) runLogsPlane {
	t.Helper()
	home := t.TempDir()
	s := config.Settings{Socket: filepath.Join(home, "iris.sock")}
	logs := NewRunLogWriter(s)
	if err := os.MkdirAll(filepath.Dir(logs.Ref("7")), 0o700); err != nil {
		t.Fatalf("mk logs dir: %v", err)
	}
	if err := os.WriteFile(logs.Ref("7"), []byte(content), 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	return runLogsPlane{logs: logs}
}

// readAll drains the plane's reader for run 7 under opts.
func readAll(t *testing.T, p runLogsPlane, opts api.LogsOptions) (string, error) {
	t.Helper()
	rc, err := p.Logs(context.Background(), "7", opts)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read view: %v", err)
	}
	return string(b), nil
}

// framedFixture is a framed capture: header, log lines, both frame directions,
// and a close stamp.
const framedFixture = "#|{\"iris_log\":1,\"run\":\"7\"}\n" +
	"L|starting up\n" +
	">|{\"event\":\"go\",\"turn\":1}\n" +
	"<|{\"event\":\"done\",\"turn\":1}\n" +
	"L|all good\n" +
	"#|{\"ended\":\"x\",\"outcome\":\"succeeded\"}\n"

// TestRunLogsViews proves the framed-capture views: naturalized default,
// stream filters, and the raw tagged passthrough.
func TestRunLogsViews(t *testing.T) {
	p := viewsTestPlane(t, framedFixture)

	t.Run("naturalized-default", func(t *testing.T) {
		got, err := readAll(t, p, api.LogsOptions{})
		if err != nil {
			t.Fatalf("default view: %v", err)
		}
		want := "[iris] {\"iris_log\":1,\"run\":\"7\"}\n" +
			"starting up\n" +
			"[engine] {\"event\":\"go\",\"turn\":1}\n" +
			"[pipeline] {\"event\":\"done\",\"turn\":1}\n" +
			"all good\n" +
			"[iris] {\"ended\":\"x\",\"outcome\":\"succeeded\"}\n"
		if got != want {
			t.Errorf("naturalized view = %q, want %q", got, want)
		}
	})

	t.Run("stream-log", func(t *testing.T) {
		got, err := readAll(t, p, api.LogsOptions{Stream: "log"})
		if err != nil {
			t.Fatalf("log view: %v", err)
		}
		if want := "starting up\nall good\n"; got != want {
			t.Errorf("log view = %q, want %q", got, want)
		}
	})

	t.Run("stream-frames", func(t *testing.T) {
		got, err := readAll(t, p, api.LogsOptions{Stream: "frames"})
		if err != nil {
			t.Fatalf("frames view: %v", err)
		}
		want := "[engine] {\"event\":\"go\",\"turn\":1}\n[pipeline] {\"event\":\"done\",\"turn\":1}\n"
		if got != want {
			t.Errorf("frames view = %q, want %q", got, want)
		}
	})

	t.Run("format-tagged", func(t *testing.T) {
		got, err := readAll(t, p, api.LogsOptions{Format: "tagged"})
		if err != nil {
			t.Fatalf("tagged view: %v", err)
		}
		if got != framedFixture {
			t.Errorf("tagged view = %q, want the file verbatim", got)
		}
	})
}

// TestRunLogsLegacyRaw proves a legacy raw capture is served byte-for-byte and
// refuses stream/format views honestly.
func TestRunLogsLegacyRaw(t *testing.T) {
	raw := "plain stderr line\nanother\n"
	p := viewsTestPlane(t, raw)

	got, err := readAll(t, p, api.LogsOptions{})
	if err != nil {
		t.Fatalf("raw default: %v", err)
	}
	if got != raw {
		t.Errorf("raw capture = %q, want verbatim %q", got, raw)
	}

	if _, err := readAll(t, p, api.LogsOptions{Stream: "log"}); err == nil || !strings.Contains(err.Error(), "without framing") {
		t.Errorf("stream filter on raw capture: err = %v, want a without-framing refusal", err)
	}
	if _, err := readAll(t, p, api.LogsOptions{Format: "tagged"}); err == nil || !strings.Contains(err.Error(), "without framing") {
		t.Errorf("tagged view on raw capture: err = %v, want a without-framing refusal", err)
	}
}

// TestRunLogMeta proves the ps readout's per-run log metadata: size, framing
// flag, and the naturalized last line, with honest absence for a missing file.
func TestRunLogMeta(t *testing.T) {
	p := viewsTestPlane(t, framedFixture)

	meta := runLogMeta(p.logs, "7")
	if meta == nil {
		t.Fatal("meta = nil for a present capture")
	}
	if !meta.Framed {
		t.Error("framed capture reported unframed")
	}
	if meta.SizeBytes != int64(len(framedFixture)) {
		t.Errorf("size = %d, want %d", meta.SizeBytes, len(framedFixture))
	}
	if want := `[iris] {"ended":"x","outcome":"succeeded"}`; meta.LastLine != want {
		t.Errorf("last line = %q, want %q", meta.LastLine, want)
	}

	if got := runLogMeta(p.logs, "404"); got != nil {
		t.Errorf("meta for an absent capture = %+v, want nil", got)
	}
	if got := runLogMeta(nil, "7"); got != nil {
		t.Errorf("meta with capture off = %+v, want nil", got)
	}
}
