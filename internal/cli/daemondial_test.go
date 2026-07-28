package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
	"github.com/MateusAMP2119/iris-lakehouse/internal/daemon"
)

// staleSocketFile leaves a socket file at path with nothing listening: a bind
// whose listener is closed without unlinking, which is what a daemon killed
// without a graceful shutdown leaves behind. Dialing it is refused.
func staleSocketFile(t *testing.T, path string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("bind stale socket %s: %v", path, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatalf("close stale socket %s: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file missing after close: %v", err)
	}
}

// TestLocalDialHandover proves a command that lands on a socket mid-handover
// waits it out instead of reporting the engine unreachable, while a command with
// no socket at all still fails fast. The engine start that reports "started"
// leaves the CLI free to dial immediately; a daemon replacing a predecessor
// unlinks and rebinds the path, so a dial refused on a socket file that exists
// means the handover is in flight, not that there is no engine.
func TestLocalDialHandover(t *testing.T) {
	t.Setenv("IRIS_HOST", "")
	t.Setenv("IRIS_SOCKET", "")
	t.Setenv("IRIS_TOKEN", "")

	t.Run("daemon-handover-dial", func(t *testing.T) {
		t.Run("a refused socket being rebound is waited out", func(t *testing.T) {
			sock := shortSocket(t)
			staleSocketFile(t, sock)

			// The daemon comes up on that path the production way: Server.Start
			// unlinks the stale socket file and binds its own listener.
			srv := daemon.NewServer(config.Settings{Socket: sock}, api.NewMux())
			t.Cleanup(func() { _ = srv.Shutdown() })
			started := make(chan error, 1)
			go func() {
				time.Sleep(100 * time.Millisecond)
				started <- srv.Start(context.Background())
			}()

			var out, errb bytes.Buffer
			a := newApp(&out, &errb)
			if err := a.probeDaemon(context.Background(), config.Settings{Socket: sock}); err != nil {
				t.Fatalf("probeDaemon = %v, want nil: a dial refused on an existing socket must be waited out", err)
			}
			if err := <-started; err != nil {
				t.Fatalf("server Start: %v", err)
			}
		})

		t.Run("no socket still fails fast", func(t *testing.T) {
			sock := shortSocket(t) // the path exists as a temp dir; the socket does not

			var out, errb bytes.Buffer
			a := newApp(&out, &errb)
			start := time.Now()
			err := a.probeDaemon(context.Background(), config.Settings{Socket: sock})
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("probeDaemon = nil with no socket, want an error")
			}
			// No socket means no engine to wait for: the retry window never opens,
			// so a stopped engine still reports unreachable at once.
			if elapsed >= localDialRetryWindow {
				t.Errorf("probeDaemon took %v with no socket, want well under the %v retry window", elapsed, localDialRetryWindow)
			}
		})
	})
}
