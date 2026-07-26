package tui

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// scriptedView wires a psView over scripted channels, a buffer, and a fixed
// geometry -- the loop under test with no terminal, poller, or daemon.
type scriptedView struct {
	v        *psView
	out      *bytes.Buffer
	keys     chan psKey
	polls    chan psPollMsg
	notes    chan string
	cancelCh chan string
}

func newScriptedView() *scriptedView {
	s := &scriptedView{
		out:      &bytes.Buffer{},
		keys:     make(chan psKey, 16),
		polls:    make(chan psPollMsg, 1),
		notes:    make(chan string, 1),
		cancelCh: make(chan string, 4),
	}
	s.v = &psView{
		out: s.out, p: painter{}, size: func() (int, int) { return 80, 24 },
		keys: s.keys, polls: s.polls, notes: s.notes, cancelCh: s.cancelCh,
	}
	return s
}

// TestRunPsLoop proves the event loop's exits and message plumbing: q and a
// closed key stream exit clean, a failed poll exits ErrEngineGone, ctx
// cancellation exits clean, a confirmed cancel reaches the poller channel,
// and a fresh poll re-renders with the new snapshot.
func TestRunPsLoop(t *testing.T) {
	t.Run("run-ps-loop", func(t *testing.T) {
		t.Run("q exits clean", func(t *testing.T) {
			s := newScriptedView()
			s.keys <- key('q')
			if err := runPsLoop(context.Background(), s.v, newPsModel(psvFixture(), "")); err != nil {
				t.Fatalf("q exit = %v, want nil", err)
			}
			if !strings.Contains(s.out.String(), "IRIS") {
				t.Error("the loop never rendered a frame")
			}
		})

		t.Run("a failed poll exits with the poll error", func(t *testing.T) {
			s := newScriptedView()
			s.polls <- psPollMsg{err: ErrEngineGone}
			if err := runPsLoop(context.Background(), s.v, newPsModel(psvFixture(), "")); err != ErrEngineGone {
				t.Fatalf("poll-failure exit = %v, want the poll error back", err)
			}
			// A reached daemon's refusal surfaces as the typed error, so ps()
			// can keep its exit-4 classification.
			s = newScriptedView()
			s.polls <- psPollMsg{err: &HTTPError{Status: 500, Code: "internal", Message: "meta down"}}
			var herr *HTTPError
			if err := runPsLoop(context.Background(), s.v, newPsModel(psvFixture(), "")); !errors.As(err, &herr) {
				t.Fatalf("http poll-failure exit = %v, want the *HTTPError back", err)
			}
		})

		t.Run("context cancellation exits clean", func(t *testing.T) {
			s := newScriptedView()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := runPsLoop(ctx, s.v, newPsModel(psvFixture(), "")); err != nil {
				t.Fatalf("cancelled-ctx exit = %v, want nil", err)
			}
		})

		t.Run("a confirmed cancel reaches the poller, its note lands on screen", func(t *testing.T) {
			s := newScriptedView()
			sb := &syncBuffer{}
			s.v.out = sb
			m := newPsModel(psvFixture(), "")
			m.selectTree(psTreeRow{lane: "ingest", pipeline: "load_orders"}) // cursor lands on running 14
			m.pane = psPaneStats

			done := make(chan error, 1)
			go func() { done <- runPsLoop(context.Background(), s.v, m) }()

			s.keys <- key('c')
			s.keys <- key('y')
			select {
			case id := <-s.cancelCh:
				if id != "14" {
					t.Errorf("cancel request = %q, want 14", id)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the confirmed cancel never reached the poller channel")
			}
			s.notes <- "cancelled 14 (dead_lettered)"
			deadline := time.After(5 * time.Second)
			for !strings.Contains(sb.String(), "cancelled 14 (dead_lettered)") {
				select {
				case <-deadline:
					t.Fatal("the cancel note never rendered")
				case <-time.After(time.Millisecond):
				}
			}
			s.keys <- key('q')
			if err := <-done; err != nil {
				t.Fatalf("loop exit = %v, want nil", err)
			}
		})

		t.Run("a fresh poll re-renders the new snapshot", func(t *testing.T) {
			s := newScriptedView()
			sb := &syncBuffer{}
			s.v.out = sb
			m := newPsModel(psvFixture(), "")
			next := psvFixture()
			next.Ps.Engine.Uptime = "9h9m"

			done := make(chan error, 1)
			go func() { done <- runPsLoop(context.Background(), s.v, m) }()
			s.polls <- psPollMsg{snap: next}
			deadline := time.After(5 * time.Second)
			for !strings.Contains(sb.String(), "9h9m") {
				select {
				case <-deadline:
					t.Fatal("the re-poll never re-rendered the engine facts")
				case <-time.After(time.Millisecond):
				}
			}
			s.keys <- key('q')
			if err := <-done; err != nil {
				t.Fatalf("loop exit = %v, want nil", err)
			}
		})
	})
}

// syncBuffer is a mutex-guarded frame sink for tests that read the output
// while the loop goroutine writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// recordingCancel records the cancelled run id.
type recordingCancel struct{ last atomic.Value }

func (r *recordingCancel) CancelRun(_ context.Context, run string) error {
	r.last.Store(run)
	return nil
}

func (r *recordingCancel) CancelPipeline(_ context.Context, pipeline string) (string, error) {
	r.last.Store(pipeline)
	return "1", nil
}

// TestPollPs drives the real poller against the real api mux over a unix
// socket: every tick reads the whole history and the pipeline listing, a
// pointed focus tails the run's logs, a cancel request POSTs /run/cancel, and
// a stopped daemon ends the poller with the fatal message.
func TestPollPs(t *testing.T) {
	t.Setenv("IRIS_HOST", "")
	t.Setenv("IRIS_SOCKET", "")
	t.Setenv("IRIS_TOKEN", "")

	t.Run("poll-ps", func(t *testing.T) {
		sock := shortSocket(t)
		var sawAll atomic.Bool
		cancels := &recordingCancel{}
		role := api.NewRoleState()
		role.SetLeader()

		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("listen unix %s: %v", sock, err)
		}
		mux := api.NewMux(
			api.WithRole(role),
			api.WithPs(psFunc(func(_ context.Context, all, _ bool) (api.PsPayload, error) {
				sawAll.Store(all)
				return psFixture(), nil
			})),
			api.WithPipelines(&pipelinesListFunc{items: []api.PipelineListItem{
				{Name: "extract", Active: true, Lane: "ingest"},
			}}),
			api.WithRunCancel(cancels),
		)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		shutdown := func() { _ = srv.Shutdown(context.Background()) }
		t.Cleanup(shutdown)

		c := unixClient(sock)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		polls := make(chan psPollMsg, 1)
		notes := make(chan string, 1)
		cancelCh := make(chan string, 1)
		done := make(chan struct{})
		go func() {
			pollPs(ctx, c, 5*time.Millisecond, cancelCh, polls, notes)
			close(done)
		}()

		waitPoll := func(what string) psPollMsg {
			t.Helper()
			select {
			case pm := <-polls:
				return pm
			case <-time.After(5 * time.Second):
				t.Fatalf("poller never delivered %s", what)
				return psPollMsg{}
			}
		}

		pm := waitPoll("a first snapshot")
		if pm.err != nil {
			t.Fatalf("first poll failed: %v", pm.err)
		}
		if !sawAll.Load() {
			t.Error("the poller must read the whole history (?all=true)")
		}
		if len(pm.snap.Pipelines) != 1 || pm.snap.Pipelines[0].Lane != "ingest" {
			t.Errorf("snapshot listing = %+v, want the lane-carrying row", pm.snap.Pipelines)
		}

		cancelCh <- "7"
		select {
		case note := <-notes:
			if !strings.Contains(note, "cancelled 7") {
				t.Errorf("cancel note = %q, want the cancelled outcome", note)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancel outcome never returned")
		}
		if got, _ := cancels.last.Load().(string); got != "7" {
			t.Errorf("daemon cancelled %q, want 7", got)
		}

		// The daemon stopping mid-view is an unreachable tick, not a teardown:
		// the poller reports it and keeps ticking (the reconnect loop).
		shutdown()
		_ = ln.Close()
		deadline := time.After(5 * time.Second)
		for {
			pm = waitPoll("an unreachable tick")
			if pm.err != nil {
				t.Fatalf("a stopped daemon must read unreachable, not fatal: %v", pm.err)
			}
			if pm.unreachable {
				break
			}
			select {
			case <-deadline:
				t.Fatal("the poller never reported the stopped daemon")
			default:
			}
		}
		select {
		case <-done:
			t.Fatal("the poller quit on an unreachable engine; it must keep retrying")
		case <-time.After(50 * time.Millisecond):
		}
		cancel() // only the view's exit ends the reconnect loop
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the poller outlived its context")
		}
	})

	// The view opens on a seed carrying /ps and the listing only, so every
	// poll-derived surface stays empty until a poll lands. The poller must take
	// one before it enters the ticker, whatever the interval.
	t.Run("poll-ps-open", func(t *testing.T) {
		sock := shortSocket(t)
		role := api.NewRoleState()
		role.SetLeader()

		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatalf("listen unix %s: %v", sock, err)
		}
		mux := api.NewMux(
			api.WithRole(role),
			api.WithPs(psFunc(func(context.Context, bool, bool) (api.PsPayload, error) { return psFixture(), nil })),
			api.WithPipelines(&pipelinesListFunc{items: []api.PipelineListItem{{Name: "extract", Lane: "ingest"}}}),
		)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		polls := make(chan psPollMsg, 1)
		go pollPs(ctx, unixClient(sock), time.Hour, make(chan string), polls, make(chan string, 1))

		select {
		case pm := <-polls:
			if pm.err != nil {
				t.Fatalf("the opening poll failed: %v", pm.err)
			}
			if len(pm.snap.Pipelines) != 1 {
				t.Errorf("opening snapshot listing = %+v, want the seeded row", pm.snap.Pipelines)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the poller waited for its first tick instead of polling at open")
		}
	})
}

// pipelinesListFunc serves a canned pipeline listing.
type pipelinesListFunc struct{ items []api.PipelineListItem }

func (p *pipelinesListFunc) RunPipeline(context.Context, api.PipelineRunRequest) (api.PipelineRunResult, error) {
	return api.PipelineRunResult{}, api.ErrControlUnavailable
}

func (p *pipelinesListFunc) ListPipelines(context.Context, bool) (api.PipelineListResult, error) {
	return api.PipelineListResult{Pipelines: p.items}, nil
}
