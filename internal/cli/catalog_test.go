package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// packGzipTar builds a minimal pack tarball for fixture catalogs.
func packGzipTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		b := []byte(body)
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// startCatalogDaemon serves the real mux with a catalog read plane over a unix socket.
func startCatalogDaemon(t *testing.T, sock string, resolver catalog.Resolver) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	srv := &http.Server{
		Handler:           api.NewMux(api.WithCatalogList(daemon.NewCatalogReadPlane(nil, resolver, nil))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
}

// fixtureResolver serves one pack from an in-memory catalog for CLI list tests.
func fixtureResolver(t *testing.T) catalog.Resolver {
	t.Helper()
	// Minimal tarball: one declaration so resolve succeeds for list enrichment.
	// The daemon catalog_test helper is not imported; keep a tiny tar here.
	body := []byte("name: solo\nrun: [sh, x]\n")
	// Build via a tiny gzip+tar by reusing catalog server patterns would need
	// archive imports — use a Fetch that returns pre-built bytes from install
	// tests' shape is heavy; index-only enrichment failure is OK if blob missing.
	// Provide a real tiny tar through catalog.Remote.Pack path:
	return fixtureResolverWithPack(t, "demo-pack", body)
}

func fixtureResolverWithPack(t *testing.T, name string, declareYAML []byte) catalog.Resolver {
	t.Helper()
	// Use httptest-free fetch: build pack with archive in test by calling daemon's
	// approach — import archive packages here.
	blob := packTinyTar(t, "pipelines/l/a/iris-declare.yaml", string(declareYAML))
	sum := sha256.Sum256(blob)
	index, err := json.Marshal(catalog.Index{Format: catalog.IndexFormat, Packs: []catalog.IndexEntry{{
		Name: name, Path: "packs/" + name + ".tar.gz", SHA256: hex.EncodeToString(sum[:]),
		Description: "fixture",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(_ context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "catalog.json") {
			return index, nil
		}
		return blob, nil
	}
	return catalog.Resolver{Catalogs: []catalog.Remote{{URL: "https://cat.example/catalog.json", Fetch: fetch}}}
}

// packTinyTar is defined in setup_test.go-adjacent; implement here to keep catalog_test self-contained.
func packTinyTar(t *testing.T, path, body string) []byte {
	t.Helper()
	return packGzipTar(t, map[string]string{path: body, "README.md": "# demo\n"})
}

// TestCatalogListVerb proves `iris catalog list` is daemon-backed and faults without an engine.
func TestCatalogListVerb(t *testing.T) {
	t.Setenv("IRIS_HOST", "")
	t.Setenv("IRIS_SOCKET", "")
	t.Setenv("IRIS_TOKEN", "")
	t.Setenv("IRIS_CATALOGS", "")

	t.Run("catalog-list-verb", func(t *testing.T) {
		t.Run("the live daemon listing renders with sources", func(t *testing.T) {
			sock := shortSocket(t)
			startCatalogDaemon(t, sock, fixtureResolver(t))
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", sock, "catalog", "list", "--json"})
			if code != exitOK {
				t.Fatalf("catalog list exit = %d, want 0\nstderr: %s", code, errb.String())
			}
			var doc struct {
				Data catalogListPayload `json:"data"`
			}
			decodeSingleJSON(t, out.Bytes(), &doc)
			if len(doc.Data.Packs) != 1 || doc.Data.Packs[0].Name != "demo-pack" {
				t.Fatalf("live listing = %+v, want the fixture pack", doc.Data)
			}
			if doc.Data.Packs[0].Source != "https://cat.example/catalog.json" {
				t.Errorf("Source = %q, want the fixture catalog URL", doc.Data.Packs[0].Source)
			}
			if len(doc.Data.Warnings) != 0 {
				t.Errorf("live listing warnings = %v, want none", doc.Data.Warnings)
			}
		})

		t.Run("no engine is operation-failed", func(t *testing.T) {
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", filepath.Join(t.TempDir(), "gone.sock"), "catalog", "list", "--json"})
			if code != exitOpFailed {
				t.Fatalf("fallback exit = %d, want %d\nstderr: %s\nstdout: %s", code, exitOpFailed, errb.String(), out.String())
			}
			if !strings.Contains(errb.String()+out.String(), "engine unreachable") {
				t.Errorf("error output %q %q, want engine unreachable", errb.String(), out.String())
			}
		})

		t.Run("show without engine is operation-failed", func(t *testing.T) {
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", filepath.Join(t.TempDir(), "gone.sock"), "catalog", "show", catalog.StarterPack, "--json"})
			if code != exitOpFailed {
				t.Fatalf("show exit = %d, want %d", code, exitOpFailed)
			}
		})

		t.Run("an unknown pack is operation-failed", func(t *testing.T) {
			sock := shortSocket(t)
			startCatalogDaemon(t, sock, fixtureResolver(t))
			var out, errb bytes.Buffer
			code := newApp(&out, &errb).run([]string{"--socket", sock, "catalog", "show", "nope"})
			if code != exitOpFailed {
				t.Fatalf("show nope exit = %d, want %d", code, exitOpFailed)
			}
		})
	})
}
