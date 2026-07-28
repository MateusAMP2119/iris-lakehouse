package daemon

// This file is the detach-readiness suite: what `iris engine start -d` waits on
// before it reports the engine started. The gate is a served response, not a
// dial, so the command a user (or install.sh) issues the instant a start returns
// finds a control plane that answers instead of "engine unreachable".

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
)

// listenBare binds a unix socket that never accepts, standing in for a listener
// that is bound but not serving -- a predecessor draining on the path, or this
// daemon's own listener before its HTTP server accepts. It does not unlink the
// socket file on close, so a daemon replacing it exercises the real
// unlink-then-bind handover.
func listenBare(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("bind bare unix socket %s: %v", path, err)
	}
	ln.SetUnlinkOnClose(false)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// getHealthz issues one GET /healthz over the unix socket at path, the way a CLI
// command does, and returns its status code.
func getHealthz(t *testing.T, path string) int {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://iris/healthz", nil)
	if err != nil {
		t.Fatalf("build healthz request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz over %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestWaitControlPlaneReady proves the detach readiness gate is a served
// response, never a bare dial. A dial is strictly weaker: connect(2) on a unix
// socket succeeds against any bound listener, including one that serves nothing
// and one the starting daemon is about to unlink and replace, so a dial-only gate
// reported the engine started while the control plane could not answer -- the
// startup race where the first command after a start was refused.
func TestWaitControlPlaneReady(t *testing.T) {
	t.Run("detach-readiness", func(t *testing.T) {
		t.Run("a bound socket that serves nothing is not ready", func(t *testing.T) {
			path := shortSocket(t)
			listenBare(t, path)

			// The weaker gate passes here: the dial connects into the listener's
			// backlog even though nothing will ever answer.
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatalf("dial bare socket: %v, want the dial to connect", err)
			}
			_ = conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if err := waitControlPlaneReady(ctx, path); err == nil {
				t.Fatal("waitControlPlaneReady = nil on a socket that serves nothing, want an error")
			}
		})

		t.Run("a serving control plane is ready", func(t *testing.T) {
			path := shortSocket(t)
			srv := NewServer(config.Settings{Socket: path}, api.NewMux())
			startServer(t, srv)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := waitControlPlaneReady(ctx, path); err != nil {
				t.Fatalf("waitControlPlaneReady = %v, want nil against a serving daemon", err)
			}
		})

		t.Run("a socket that appears late is waited for", func(t *testing.T) {
			path := shortSocket(t)
			srv := NewServer(config.Settings{Socket: path}, api.NewMux())
			t.Cleanup(func() { _ = srv.Shutdown() })
			started := make(chan error, 1)
			go func() {
				time.Sleep(150 * time.Millisecond)
				started <- srv.Start(context.Background())
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := waitControlPlaneReady(ctx, path); err != nil {
				t.Fatalf("waitControlPlaneReady = %v, want nil once the daemon binds", err)
			}
			if err := <-started; err != nil {
				t.Fatalf("server Start: %v", err)
			}
		})

		// The production shape of the race: a daemon is started on a path a
		// predecessor still holds. Server.Start unlinks the socket file it finds and
		// binds its own, so the predecessor's listener answers a dial right up until
		// it is replaced. The wait must outlast that, and the command issued the
		// instant it returns must be served.
		t.Run("a predecessor on the path does not report the new daemon ready", func(t *testing.T) {
			path := shortSocket(t)
			listenBare(t, path)

			srv := NewServer(config.Settings{Socket: path}, api.NewMux())
			t.Cleanup(func() { _ = srv.Shutdown() })
			var serving atomic.Bool
			started := make(chan error, 1)
			go func() {
				time.Sleep(150 * time.Millisecond)
				err := srv.Start(context.Background())
				serving.Store(err == nil)
				started <- err
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := waitControlPlaneReady(ctx, path); err != nil {
				t.Fatalf("waitControlPlaneReady = %v, want nil once the new daemon serves", err)
			}
			if !serving.Load() {
				t.Fatal("the wait returned before the new daemon served; the predecessor's socket satisfied it")
			}
			if err := <-started; err != nil {
				t.Fatalf("server Start: %v", err)
			}
			if code := getHealthz(t, path); code != http.StatusOK {
				t.Errorf("GET /healthz right after the wait = %d, want %d", code, http.StatusOK)
			}
		})
	})
}
