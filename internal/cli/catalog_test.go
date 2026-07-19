package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/daemon"
)

// startCatalogDaemon serves the real mux with the real catalog read plane over a unix socket.
func startCatalogDaemon(t *testing.T, sock string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	srv := &http.Server{
		Handler:           api.NewMux(api.WithCatalogList(daemon.NewCatalogReadPlane(nil, catalog.Resolver{}, nil))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}

// TestCatalogListVerb proves `iris catalog list`: the daemon-backed listing and the
// embedded fallback when no engine answers, one JSON document either way.
func TestCatalogListVerb(t *testing.T) {
	t.Setenv("IRIS_HOST", "")
	t.Setenv("IRIS_SOCKET", "")
	t.Setenv("IRIS_TOKEN", "")
	t.Setenv("IRIS_CATALOGS", "")

	t.Run("catalog-list-verb", func(t *testing.T) {
		t.Run("the live daemon listing renders with sources", func(t *testing.T) {
			sock := shortSocket(t)
			startCatalogDaemon(t, sock)
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", sock, "catalog", "list", "--json"})
			if code != exitOK {
				t.Fatalf("catalog list exit = %d, want 0\nstderr: %s", code, errb.String())
			}
			var doc struct {
				Data catalogListPayload `json:"data"`
			}
			decodeSingleJSON(t, out.Bytes(), &doc)
			if len(doc.Data.Packs) != 2 || doc.Data.Packs[0].Source != catalog.SourceEmbedded {
				t.Fatalf("live listing = %+v, want the two embedded packs", doc.Data)
			}
			if len(doc.Data.Warnings) != 0 {
				t.Errorf("live listing warnings = %v, want none", doc.Data.Warnings)
			}
		})

		t.Run("no engine falls back to the embedded packs with the warning", func(t *testing.T) {
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", filepath.Join(t.TempDir(), "gone.sock"), "catalog", "list", "--json"})
			if code != exitOK {
				t.Fatalf("fallback exit = %d, want 0\nstderr: %s", code, errb.String())
			}
			var doc struct {
				Data catalogListPayload `json:"data"`
			}
			decodeSingleJSON(t, out.Bytes(), &doc)
			if len(doc.Data.Packs) != 2 || len(doc.Data.Warnings) == 0 || !strings.Contains(doc.Data.Warnings[0], "engine unreachable") {
				t.Fatalf("fallback listing = %+v, want embedded packs plus the unreachable warning", doc.Data)
			}
		})

		t.Run("show falls back with the full embedded preview", func(t *testing.T) {
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", filepath.Join(t.TempDir(), "gone.sock"), "catalog", "show", catalog.StarterPack, "--json"})
			if code != exitOK {
				t.Fatalf("show exit = %d, want 0\nstderr: %s", code, errb.String())
			}
			var doc struct {
				Data api.CatalogPack `json:"data"`
			}
			decodeSingleJSON(t, out.Bytes(), &doc)
			if doc.Data.Name != catalog.StarterPack || len(doc.Data.ApplyOrder) != 3 || doc.Data.Readme == "" {
				t.Fatalf("show payload = %+v, want the full embedded preview", doc.Data)
			}
		})

		t.Run("an unknown pack is operation-failed", func(t *testing.T) {
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", filepath.Join(t.TempDir(), "gone.sock"), "catalog", "show", "nope"})
			if code != exitOpFailed {
				t.Fatalf("show nope exit = %d, want %d", code, exitOpFailed)
			}
		})
	})
}
