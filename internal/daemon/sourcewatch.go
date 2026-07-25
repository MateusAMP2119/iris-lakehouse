package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the declared-source watcher: the engine-side input for a
// pipeline's source block. One resident watcher runs beside the lane loop;
// when an origin publishes fresh bytes, that ARRIVAL is the event -- the
// watcher stores the body, bumps the watermark, the parked lane wakes, and
// the turn consumes the body with no network of its own. The loop stays
// purely event-driven: the only durations in play are the ones HTTP itself
// declares (Cache-Control max-age less Age, Expires, Retry-After), bounded by
// RFC 9111-style cache limits -- never an iris schedule, never a user knob.
// Scripts do no network I/O at all.

// The watcher's cache bounds: engine-side limits on origin-declared
// freshness, not schedules. The floor keeps a max-age=0/no-store origin from
// being hammered; the cap keeps a wildly long declaration re-checkable; the
// no-signal lifetime covers origins that declare nothing usable.
const (
	sourceWaitFloor    = 15 * time.Second
	sourceWaitCap      = 10 * time.Minute
	sourceNoSignalWait = 5 * time.Minute
	// sourceRetryAfterCap bounds an origin's Retry-After above the normal cap:
	// a long ban is honored, never fought -- but not past this.
	sourceRetryAfterCap = time.Hour
	// sourceErrorBackoffBase seeds the doubling error backoff.
	sourceErrorBackoffBase = 15 * time.Second
)

// sourceBodyCap bounds one fetched source body.
const sourceBodyCap = 8 << 20

// sourceFetchTimeout bounds one source fetch.
const sourceFetchTimeout = 60 * time.Second

// sourceFrame is one turn's prepared source input: the full protocol line for
// the pipe and the digest summary line for the capture (the body itself never
// lands in the run log).
type sourceFrame struct {
	pipeline string
	line     string
	summary  string
}

// sourceState is one watched source's whole state: identity, conditional-GET
// validators, the body awaiting delivery, health, and pacing.
type sourceState struct {
	pipeline, url      string
	etag, lastModified string
	pendingBody        []byte
	pendingSHA         [32]byte // digest of the body stored for delivery
	deliveredSHA       [32]byte // digest of the last body a turn COMMITTED
	seenSHA            [32]byte // digest of the last body fetched at all
	status             int
	errMsg             string
	fails              int
	freshFor           time.Duration // the origin's last declared freshness, bounded
	nextAttempt        time.Time
}

// sourceFetcher is the declared-source watcher and hand-off point: the single
// engine-wide instance the lane loop's companion goroutine drives, the turn
// paths (loop and manual alike) take bodies from, and the ps plane reads
// health from.
type sourceWatcher struct {
	mu        sync.Mutex
	client    *http.Client
	states    map[string]*sourceState // keyed by pipeline
	workspace string
	registry  store.RegistryReader
	events    *dispatch.Events
	refreshCh chan struct{}
	logger    *slog.Logger
}

// newSourceFetcher builds the watcher over one shared HTTP client. registry
// may be nil in compositions that never run the watcher (take and Health
// still work). A nil logger discards.
func newSourceWatcher(workspace string, registry store.RegistryReader, logger *slog.Logger) *sourceWatcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &sourceWatcher{
		client:    &http.Client{Timeout: sourceFetchTimeout},
		states:    map[string]*sourceState{},
		workspace: workspace,
		registry:  registry,
		refreshCh: make(chan struct{}, 1),
		logger:    logger,
	}
}

// refresh signals the watcher to re-read its roster (a declaration applied or
// destroyed). Non-blocking and coalescing; safe before the watcher runs.
func (f *sourceWatcher) refresh() {
	select {
	case f.refreshCh <- struct{}{}:
	default:
	}
}

// run is the watcher loop the lane loop spawns beside itself: one goroutine
// multiplexing every declared source. It exits promptly on cancellation
// (in-flight fetches ride ctx, and the loop joins it before returning).
func (f *sourceWatcher) run(ctx context.Context, events *dispatch.Events) {
	f.mu.Lock()
	f.events = events
	f.mu.Unlock()
	f.rescanRoster(ctx)
	for {
		next := f.fetchDue(ctx)
		wait := time.Until(next)
		if wait < time.Second {
			wait = time.Second // coalesce wakeups; per-source pacing is already floored
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-f.refreshCh:
			timer.Stop()
			f.rescanRoster(ctx)
		case <-timer.C:
		}
	}
}

// rescanRoster rebuilds the watched set: the workspace's source-declaring
// pipelines INTERSECTED with the registered roster -- a destroyed pipeline's
// files stay on disk, and its origin must not keep being polled. A vanished
// entry's state drops whole; an in-place URL change restarts the state fresh.
func (f *sourceWatcher) rescanRoster(ctx context.Context) {
	ws, err := declare.DiscoverWorkspace(f.workspace)
	if err != nil {
		f.logger.Debug("source watcher: workspace scan failed", "err", err)
		return
	}
	registered := map[string]bool{}
	if f.registry != nil {
		names, rerr := f.registry.RegisteredPipelines(ctx)
		if rerr != nil {
			f.logger.Debug("source watcher: registry read failed", "err", rerr)
			return
		}
		for _, n := range names {
			registered[n] = true
		}
	}
	live := map[string]string{} // pipeline -> url
	for _, p := range ws.Pipelines {
		if p.Declaration.Source != nil && registered[p.Declaration.Name] {
			live[p.Declaration.Name] = p.Declaration.Source.HTTP
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, st := range f.states {
		if url, ok := live[name]; !ok || st.url != url {
			delete(f.states, name)
		}
	}
	for name, url := range live {
		if _, ok := f.states[name]; !ok {
			f.states[name] = &sourceState{pipeline: name, url: url}
		}
	}
}

// fetchDue fetches every source whose next attempt is due and returns the
// earliest next attempt across the roster (a no-signal wait when empty).
func (f *sourceWatcher) fetchDue(ctx context.Context) time.Time {
	now := time.Now()
	f.mu.Lock()
	var due []*sourceState
	for _, st := range f.states {
		if !st.nextAttempt.After(now) {
			due = append(due, st)
		}
	}
	f.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].pipeline < due[j].pipeline })
	for _, st := range due {
		if ctx.Err() != nil {
			break
		}
		f.fetchOne(ctx, st)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	earliest := time.Now().Add(sourceNoSignalWait)
	for _, st := range f.states {
		if st.nextAttempt.Before(earliest) {
			earliest = st.nextAttempt
		}
	}
	return earliest
}

// fetchOne runs one conditional GET for st and hands the answer to
// noteAnswer, which stores a changed body and paces the next attempt.
func (f *sourceWatcher) fetchOne(ctx context.Context, st *sourceState) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, st.url, nil)
	if err != nil {
		f.noteFailure(st, err, 0)
		return
	}
	f.mu.Lock()
	if st.etag != "" {
		req.Header.Set("If-None-Match", st.etag)
	}
	if st.lastModified != "" {
		req.Header.Set("If-Modified-Since", st.lastModified)
	}
	f.mu.Unlock()

	resp, err := f.client.Do(req)
	if err != nil {
		f.noteFailure(st, err, 0)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		f.noteAnswer(st, resp, nil, [32]byte{}, false)
		return
	case resp.StatusCode != http.StatusOK:
		wait := time.Duration(0)
		if ra, rerr := parseDeltaSeconds(resp.Header.Get("Retry-After")); rerr == nil {
			wait = ra
		}
		if wait > sourceRetryAfterCap {
			wait = sourceRetryAfterCap
		}
		f.noteFailure(st, fmt.Errorf("source answered status %d", resp.StatusCode), wait)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, sourceBodyCap+1))
	if err != nil {
		f.noteFailure(st, err, 0)
		return
	}
	if len(body) > sourceBodyCap {
		f.noteFailure(st, fmt.Errorf("source body exceeds %d bytes", sourceBodyCap), 0)
		return
	}
	sum := sha256.Sum256(body)
	f.noteAnswer(st, resp, body, sum, sum != st.seenSHA)
}

// noteAnswer records a successful answer: validators, health, the pace the
// origin's own freshness declarations set for the next attempt, and -- for a
// changed body -- the pending store and watermark bump (store first, THEN
// bump: the woken turn must find it).
func (f *sourceWatcher) noteAnswer(st *sourceState, resp *http.Response, body []byte, sum [32]byte, changed bool) {
	fresh := boundWait(originFreshness(resp))
	f.mu.Lock()
	hadFails := st.fails
	if et := resp.Header.Get("ETag"); et != "" {
		st.etag = et
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		st.lastModified = lm
	}
	st.status, st.errMsg, st.fails = resp.StatusCode, "", 0
	st.freshFor = fresh
	st.nextAttempt = time.Now().Add(fresh)
	if changed {
		st.seenSHA, st.pendingBody, st.pendingSHA = sum, body, sum
	}
	events := f.events
	f.mu.Unlock()
	if hadFails > 0 {
		f.logger.Info("declared source recovered", "pipeline", st.pipeline, "after_failures", hadFails)
	}
	if changed {
		f.logger.Info("declared source changed", "pipeline", st.pipeline, "bytes", len(body), "fresh_for", fresh)
		if events != nil {
			events.Bump() // fresh bytes are the event; the parked lane wakes to them
		}
	}
}

// noteFailure records a failed attempt: doubling backoff, overridden by a
// longer origin-declared Retry-After. Every failure lands in the DAEMON log
// -- a failing source's turns are quiet, so the turn sink cannot carry this.
func (f *sourceWatcher) noteFailure(st *sourceState, ferr error, retryAfter time.Duration) {
	f.mu.Lock()
	st.fails++
	st.errMsg = ferr.Error()
	st.status = 0
	shift := st.fails - 1
	if shift > 6 {
		shift = 6
	}
	wait := boundWait(sourceErrorBackoffBase << shift)
	if retryAfter > wait {
		wait = retryAfter // a declared ban is honored past the normal cap (bounded by sourceRetryAfterCap)
	}
	st.nextAttempt = time.Now().Add(wait)
	fails := st.fails
	f.mu.Unlock()
	f.logger.Warn("declared source fetch failed", "pipeline", st.pipeline, "url", st.url, "err", ferr, "consecutive", fails, "next_attempt_in", wait)
}

// take returns the pipeline's undelivered source frame, or nil when the turn
// should carry none. It does NOT consume: the body stays takeable until
// delivered() records a committed turn, so a failed turn (or its replay)
// re-takes the same bytes instead of losing the captured change forever.
func (f *sourceWatcher) take(pipeline string, sink io.Writer) *sourceFrame {
	f.mu.Lock()
	st, ok := f.states[pipeline]
	if !ok || st.pendingBody == nil || st.pendingSHA == st.deliveredSHA {
		f.mu.Unlock()
		return nil
	}
	url, body, sum, status := st.url, st.pendingBody, st.pendingSHA, st.status
	f.mu.Unlock()

	line, err := dispatch.EncodeSourceFrame(url, status, body)
	if err != nil {
		if sink != nil {
			fmt.Fprintf(sink, "[iris: source frame for %s failed: %v]\n", url, err)
		}
		return nil
	}
	summary, _ := json.Marshal(struct {
		Event  string `json:"event"`
		URL    string `json:"url"`
		Status int    `json:"status"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}{Event: dispatch.TurnEventSource, URL: url, Status: status, Bytes: len(body), SHA256: hex.EncodeToString(sum[:])})
	return &sourceFrame{pipeline: pipeline, line: line, summary: string(summary)}
}

// delivered records that a turn COMMITTED the frame's body: only now does it
// stop being takeable, so a failed turn (or its replay) re-takes the same
// bytes. A nil frame (the turn carried no source) is a no-op; the turn paths
// call it on commit success.
func (f *sourceWatcher) delivered(src *sourceFrame) {
	if src == nil {
		return
	}
	f.mu.Lock()
	if st, ok := f.states[src.pipeline]; ok {
		st.deliveredSHA = st.pendingSHA
		st.pendingBody = nil
	}
	f.mu.Unlock()
}

// health snapshots every watched source's operator-visible state, sorted.
func (f *sourceWatcher) health() []api.SourceHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.states))
	for name := range f.states {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]api.SourceHealth, 0, len(names))
	for _, name := range names {
		st := f.states[name]
		h := api.SourceHealth{Pipeline: name, URL: st.url, Status: st.status, Error: st.errMsg, ConsecutiveFails: st.fails}
		if st.freshFor > 0 {
			h.FreshFor = st.freshFor.String()
		}
		out = append(out, h)
	}
	return out
}

// originFreshness reads the answer's own freshness lifetime: Cache-Control
// max-age less the CDN's Age, else Expires less Date, else the RFC 9111
// heuristic (a tenth of the copy's age per Last-Modified), else no signal.
func originFreshness(resp *http.Response) time.Duration {
	if ma, ok := parseMaxAge(resp.Header.Get("Cache-Control")); ok {
		age := time.Duration(0)
		if a, err := parseDeltaSeconds(resp.Header.Get("Age")); err == nil {
			age = a
		}
		return ma - age
	}
	if exp, err := http.ParseTime(resp.Header.Get("Expires")); err == nil {
		base := time.Now()
		if d, derr := http.ParseTime(resp.Header.Get("Date")); derr == nil {
			base = d
		}
		return exp.Sub(base)
	}
	if lm, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		return time.Since(lm) / 10
	}
	return 0
}

// boundWait applies the cache bounds: the no-signal lifetime for a zero or
// negative input, then the floor and cap.
func boundWait(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return sourceNoSignalWait
	case d < sourceWaitFloor:
		return sourceWaitFloor
	case d > sourceWaitCap:
		return sourceWaitCap
	}
	return d
}

// parseMaxAge reads max-age out of a Cache-Control value.
func parseMaxAge(cc string) (time.Duration, bool) {
	for _, part := range strings.Split(cc, ",") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age="); ok {
			if secs, err := parseDeltaSeconds(rest); err == nil {
				return secs, true
			}
		}
	}
	return 0, false
}

// parseDeltaSeconds parses a delta-seconds header value (Retry-After, Age,
// max-age) as a duration.
func parseDeltaSeconds(v string) (time.Duration, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, err
	}
	if secs < 0 {
		return 0, fmt.Errorf("negative seconds")
	}
	return time.Duration(secs) * time.Second, nil
}
