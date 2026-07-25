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
	"sync"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// This file is the declared-source fetcher: the engine-side HTTP input for a
// pipeline's source block. The engine fetches, the pipeline transforms — the
// script does no network I/O. Fetches are conditional (ETag / Last-Modified
// cached per pipeline), so an unchanged feed costs one 304 exchange and the
// turn carries no source frame at all.

// sourceBodyCap bounds one fetched source body.
const sourceBodyCap = 8 << 20

// sourceClockGranularity bounds how often the source clock re-reads the
// workspace for declared sources and their due times.
const sourceClockGranularity = 10 * time.Second

// SourcePollClock is the declared-source clock the lane loop runs beside it:
// the one sanctioned timer (clock doctrine otherwise holds -- events initiate
// work). Every granularity tick it scans the workspace for pipelines
// declaring a source, and when any is due per its every interval it bumps the
// watermark once, waking the parked lanes; the turn's conditional GET keeps a
// no-news wake nearly free (304, no source frame, quiet turn, re-park).
func SourcePollClock(workspace string, events *dispatch.Events, logger *slog.Logger) func(context.Context) {
	return func(ctx context.Context) {
		lastDue := map[string]time.Time{}
		tick := time.NewTicker(sourceClockGranularity)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				ws, err := declare.DiscoverWorkspace(workspace)
				if err != nil {
					if logger != nil {
						logger.Debug("source clock: workspace scan failed", "err", err)
					}
					continue
				}
				due := false
				for _, p := range ws.Pipelines {
					src := p.Declaration.Source
					if src == nil {
						continue
					}
					name := p.Declaration.Name
					if last, seen := lastDue[name]; !seen || now.Sub(last) >= src.EffectiveEvery() {
						lastDue[name] = now
						due = true
					}
				}
				if due {
					events.Bump()
				}
			}
		}
	}
}

// sourceFetchTimeout bounds one source fetch.
const sourceFetchTimeout = 60 * time.Second

// sourceFrame is one turn's prepared source input: the full protocol line for
// the pipe and the digest summary line for the capture (the body itself never
// lands in the run log).
type sourceFrame struct {
	line    string
	summary string
}

// sourceValidator caches one pipeline's conditional-GET validators, its last
// body digest, when it last went to the network, and its health: the last
// answer's status, error text, and the consecutive-failure count -- the
// operator-visible source state.
type sourceValidator struct {
	url, etag, lastModified string
	bodySHA                 [32]byte
	fetchedAt               time.Time
	every                   time.Duration
	status                  int
	errMsg                  string
	fails                   int
}

// sourceFetcher fetches declared sources over one shared client, caching each
// pipeline's response validators and health in memory (a restart refetches
// once). One instance serves the loop and the manual path, so pacing and
// health are engine-wide facts.
type sourceFetcher struct {
	mu     sync.Mutex
	client *http.Client
	cache  map[string]sourceValidator
	logger *slog.Logger
}

// newSourceFetcher builds the fetcher over one shared HTTP client. A nil
// logger discards.
func newSourceFetcher(logger *slog.Logger) *sourceFetcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &sourceFetcher{client: &http.Client{Timeout: sourceFetchTimeout}, cache: map[string]sourceValidator{}, logger: logger}
}

// Health snapshots every tracked source's operator-visible state, pipeline-sorted.
func (f *sourceFetcher) Health() []api.SourceHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.cache))
	for name := range f.cache {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]api.SourceHealth, 0, len(names))
	for _, name := range names {
		v := f.cache[name]
		out = append(out, api.SourceHealth{Pipeline: name, URL: v.url, Status: v.status, Error: v.errMsg, ConsecutiveFails: v.fails, Every: v.every.String()})
	}
	return out
}

// fetch returns the pipeline's prepared source frame, or nil when the turn
// should carry none: the declared every interval has not elapsed since the
// last fetch (no network at all -- the spin breaker: a wake caused by the
// pipeline's own produce never re-fetches early), the source is unchanged
// (304, or a 200 whose body digest matches the last -- an origin without
// validators still quiets), or the fetch failed. A failure never refuses the
// turn — it is noted in the turn's log sink and the pipeline runs without
// external input.
func (f *sourceFetcher) fetch(ctx context.Context, pipeline, url string, every time.Duration, sink io.Writer) *sourceFrame {
	f.mu.Lock()
	last := f.cache[pipeline]
	f.mu.Unlock()
	if last.url == url && !last.fetchedAt.IsZero() && time.Since(last.fetchedAt) < every {
		return nil
	}
	body, status, unchanged, err := f.do(ctx, pipeline, url)
	switch {
	case err != nil:
		// A failed attempt still stamps the pace window: an erroring origin
		// (429, outage) is retried at the declared every, never hammered. The
		// failure lands in the DAEMON log too -- the turn sink dies with a
		// quiet turn, and a failing source always quiets its turns.
		f.mu.Lock()
		last.url, last.fetchedAt, last.every = url, time.Now(), every
		last.errMsg, last.fails = err.Error(), last.fails+1
		fails := last.fails
		f.cache[pipeline] = last
		f.mu.Unlock()
		f.logger.Warn("declared source fetch failed", "pipeline", pipeline, "url", url, "err", err, "consecutive", fails, "retry_in", every)
		if sink != nil {
			fmt.Fprintf(sink, "[iris: source fetch %s failed: %v]\n", url, err)
		}
		return nil
	case unchanged:
		f.noteRecovery(pipeline, status, every, last.fails)
		return nil
	}
	f.noteRecovery(pipeline, status, every, last.fails)
	sum := sha256.Sum256(body)
	if last.url == url && sum == last.bodySHA {
		return nil // a 200 with the same bytes: unchanged in every way that matters
	}
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
	return &sourceFrame{line: line, summary: string(summary)}
}

// noteRecovery records a successful answer's health, logging the recovery
// when it ends a failure streak.
func (f *sourceFetcher) noteRecovery(pipeline string, status int, every time.Duration, prevFails int) {
	f.mu.Lock()
	v := f.cache[pipeline]
	v.status, v.errMsg, v.fails, v.every = status, "", 0, every
	f.cache[pipeline] = v
	f.mu.Unlock()
	if prevFails > 0 {
		f.logger.Info("declared source recovered", "pipeline", pipeline, "after_failures", prevFails)
	}
}

// do runs one conditional GET, updating the pipeline's cached validators.
func (f *sourceFetcher) do(ctx context.Context, pipeline, url string) (body []byte, status int, unchanged bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, sourceFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, false, err
	}
	f.mu.Lock()
	v := f.cache[pipeline]
	f.mu.Unlock()
	if v.url == url {
		if v.etag != "" {
			req.Header.Set("If-None-Match", v.etag)
		}
		if v.lastModified != "" {
			req.Header.Set("If-Modified-Since", v.lastModified)
		}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		f.mu.Lock()
		v.fetchedAt = time.Now()
		f.cache[pipeline] = v
		f.mu.Unlock()
		return nil, resp.StatusCode, true, nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, sourceBodyCap+1))
	if err != nil {
		return nil, 0, false, err
	}
	if len(body) > sourceBodyCap {
		return nil, 0, false, fmt.Errorf("source body exceeds %d bytes", sourceBodyCap)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, false, fmt.Errorf("source answered status %d", resp.StatusCode)
	}
	f.mu.Lock()
	f.cache[pipeline] = sourceValidator{
		url: url, etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified"),
		bodySHA: sha256.Sum256(body), fetchedAt: time.Now(),
	}
	f.mu.Unlock()
	return body, resp.StatusCode, false, nil
}
