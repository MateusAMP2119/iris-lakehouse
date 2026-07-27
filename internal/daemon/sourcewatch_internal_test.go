package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeSourceLine parses a fetch answer's protocol line for assertions.
func decodeSourceLine(t *testing.T, line string) (status int, changed bool, body string) {
	t.Helper()
	var f struct {
		Event   string `json:"event"`
		Status  int    `json:"status"`
		Changed bool   `json:"changed"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal([]byte(line), &f); err != nil {
		t.Fatalf("source line does not parse: %v (%q)", err, line)
	}
	if f.Event != "source" {
		t.Fatalf("answer event = %q, want source", f.Event)
	}
	return f.Status, f.Changed, f.Body
}

// TestSourceFetchOnDemand pins the fetch-frame contract: the engine fetches
// only when asked, a changed body rides whole, an unchanged answer (304 via
// validators, or an identical body) rides as changed false with no body, and a
// failing origin answers its status instead of silence. No pacing state exists
// anywhere: every ask fetches now.
func TestSourceFetchOnDemand(t *testing.T) {
	t.Run("changed-then-unchanged-then-changed", func(t *testing.T) {
		body := `v1`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") == `"tag-`+body+`"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"tag-`+body+`"`)
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		ctx := t.Context()

		status, changed, got := decodeSourceLine(t, f.fetch(ctx, "p", srv.URL).line)
		if status != 200 || !changed || got != "v1" {
			t.Fatalf("first fetch = (%d, %v, %q), want (200, true, v1)", status, changed, got)
		}
		// Same body behind the validator: a 304, answered unchanged and empty.
		status, changed, got = decodeSourceLine(t, f.fetch(ctx, "p", srv.URL).line)
		if status != 304 || changed || got != "" {
			t.Fatalf("unchanged fetch = (%d, %v, %q), want (304, false, empty)", status, changed, got)
		}
		// The origin publishes fresh bytes: the next ask carries them.
		body = `v2-longer`
		status, changed, got = decodeSourceLine(t, f.fetch(ctx, "p", srv.URL).line)
		if status != 200 || !changed || got != "v2-longer" {
			t.Fatalf("changed fetch = (%d, %v, %q), want (200, true, v2-longer)", status, changed, got)
		}
	})

	t.Run("identical-body-without-validators-answers-unchanged", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("same"))
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		if _, changed, _ := decodeSourceLine(t, f.fetch(t.Context(), "p", srv.URL).line); !changed {
			t.Fatal("first body must answer changed")
		}
		if _, changed, _ := decodeSourceLine(t, f.fetch(t.Context(), "p", srv.URL).line); changed {
			t.Fatal("identical re-fetched body must answer unchanged")
		}
	})

	t.Run("failure-answers-status-and-tracks-health", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		status, changed, got := decodeSourceLine(t, f.fetch(t.Context(), "p", srv.URL).line)
		if status != 503 || changed || got != "" {
			t.Fatalf("failed fetch = (%d, %v, %q), want (503, false, empty)", status, changed, got)
		}
		h := f.health()
		if len(h) != 1 || h[0].ConsecutiveFails != 1 || h[0].Status != 503 {
			t.Fatalf("health = %+v, want one entry with status 503 and 1 consecutive fail", h)
		}
	})

	t.Run("url-change-resets-state", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("same"))
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		_ = f.fetch(t.Context(), "p", srv.URL)
		// A different declared URL restarts the remembered state: the identical
		// body still answers changed (it is the first fetch of the new URL).
		if _, changed, _ := decodeSourceLine(t, f.fetch(t.Context(), "p", srv.URL+"/other").line); !changed {
			t.Fatal("first fetch of a changed URL must answer changed")
		}
	})

	t.Run("forget-drops-state", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("same"))
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		_ = f.fetch(t.Context(), "p", srv.URL)
		f.forget("p")
		if len(f.health()) != 0 {
			t.Fatal("forget must drop the pipeline's health entry")
		}
		if _, changed, _ := decodeSourceLine(t, f.fetch(t.Context(), "p", srv.URL).line); !changed {
			t.Fatal("a forgotten pipeline's first fetch must answer changed")
		}
	})

	t.Run("capture-summary-carries-digest-not-body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("secret-body"))
		}))
		defer srv.Close()

		f := newSourceWatcher(nil)
		frame := f.fetch(t.Context(), "p", srv.URL)
		if strings.Contains(frame.summary, "secret-body") {
			t.Fatalf("summary must carry the digest, never the body: %q", frame.summary)
		}
		if !strings.Contains(frame.summary, `"sha256"`) {
			t.Fatalf("summary must carry the sha256 digest: %q", frame.summary)
		}
	})
}
