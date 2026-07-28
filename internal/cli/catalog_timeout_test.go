package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// TestCatalogListOutlastsDaemonFetch proves the listing read waits longer than
// the daemon spends fetching a catalog index. The daemon fetches each index under
// catalog.IndexFetchTimeout; a client budget below that abandons a healthy engine
// mid-fetch, and with only a transport error to go on reports it as unreachable
// -- which is what made the first `iris catalog list` after a fresh start (a cold
// fetch, nothing warm yet) tell the operator to start an engine that was already
// running and answering.
func TestCatalogListOutlastsDaemonFetch(t *testing.T) {
	if catalogListTimeout <= catalog.IndexFetchTimeout {
		t.Fatalf("catalogListTimeout = %s, want more than the daemon's own index fetch ceiling of %s: "+
			"a client that gives up first cannot tell a slow engine from an absent one",
			catalogListTimeout, catalog.IndexFetchTimeout)
	}
}

// hangingCatalogDaemon serves /catalog by blocking until the request context is
// cancelled: a daemon that is up and accepting but has not answered yet, the way
// one mid-fetch behaves.
func hangingCatalogDaemon(t *testing.T, sock string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// TestCatalogListSlowEngineIsNotUnreachable proves a listing that runs out of
// time against a live daemon is reported as a timeout, not as a missing engine.
// The two need different answers from the operator: an unreachable engine wants
// starting, a slow one wants retrying, and telling them apart is the difference
// between a correct retry and a needless engine restart.
func TestCatalogListSlowEngineIsNotUnreachable(t *testing.T) {
	t.Setenv("IRIS_HOST", "")
	t.Setenv("IRIS_SOCKET", "")
	t.Setenv("IRIS_TOKEN", "")
	t.Setenv("IRIS_CATALOGS", "")

	sock := shortSocket(t)
	hangingCatalogDaemon(t, sock)

	// Cancelling the command's context stands in for the listing deadline passing:
	// both leave the CLI with a transport error against a daemon that is up.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	var out, errb bytes.Buffer
	code := newApp(&out, &errb).runContext(ctx, []string{"--socket", sock, "catalog", "list", "--json"})
	if code != exitOpFailed {
		t.Fatalf("catalog list exit = %d, want %d\nstdout: %s\nstderr: %s", code, exitOpFailed, out.String(), errb.String())
	}
	got := out.String() + errb.String()
	if strings.Contains(got, "engine unreachable") {
		t.Errorf("a live daemon that did not answer in time was reported unreachable:\n%s", got)
	}
	if !strings.Contains(got, "did not answer within") {
		t.Errorf("output %q, want it to name the wait that elapsed", got)
	}
}
