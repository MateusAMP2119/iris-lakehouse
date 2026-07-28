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
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
)

// This file is the declared-source fetcher: the engine-side network for a
// pipeline's source block, driven ENTIRELY by the pipeline's own fetch frames.
// The engine holds no pacing of any kind -- no timers, no header-derived waits,
// no background polling loop. The script decides WHEN to fetch (its own case,
// its own logic, sleeping between asks as it sees fit inside its turn); the
// engine performs the fetch itself -- conditional GET over stored validators,
// digest, size bound, health -- so the input's provenance stays engine-attested
// (url, status, sha) while the script never touches the network. An unchanged
// answer (304, or a body identical to the last fetched) returns changed false
// with no body, so a quiet re-check costs the pipe nothing.

// sourceBodyCap bounds one fetched source body.
const sourceBodyCap = 8 << 20

// sourceFetchTimeout bounds one source fetch: a safety bound on a hung origin
// (the turn is blocked waiting on the answer), never a pace.
const sourceFetchTimeout = 60 * time.Second

// sourceFrame is one fetch's prepared answer: the full protocol line for the
// pipe and the digest summary line for the capture (the body itself never
// lands in the run log).
type sourceFrame struct {
	pipeline string
	line     string
	summary  string
}

// sourceState is one fetched source's remembered state: conditional-GET
// validators, the last body's digest, and operator-visible health.
type sourceState struct {
	pipeline, url      string
	etag, lastModified string
	seenSHA            [32]byte // digest of the last body fetched
	status             int
	errMsg             string
	fails              int
}

// sourceWatcher is the on-demand declared-source fetcher: the single
// engine-wide instance every turn's fetch frames are serviced by, and the ps
// plane reads health from. State is per pipeline so validators persist across
// turns (a 304 re-check costs the origin nothing).
type sourceWatcher struct {
	mu     sync.Mutex
	client *http.Client
	states map[string]*sourceState // keyed by pipeline
	logger *slog.Logger
}

// newSourceWatcher builds the fetcher over one shared HTTP client. A nil
// logger discards.
func newSourceWatcher(logger *slog.Logger) *sourceWatcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &sourceWatcher{
		client: &http.Client{Timeout: sourceFetchTimeout},
		states: map[string]*sourceState{},
		logger: logger,
	}
}

// fetch services one fetch frame: a conditional GET of the pipeline's declared
// URL, answered as a source frame. A changed body rides whole; an unchanged
// answer (304, or an identical body) rides as changed false with no body; a
// failed fetch rides as its status (0 for a transport error) with no body, so
// the script always gets an answer and decides its own next move. A URL change
// in the declaration restarts the remembered state fresh.
func (f *sourceWatcher) fetch(ctx context.Context, pipeline, url string) *sourceFrame {
	f.mu.Lock()
	st, ok := f.states[pipeline]
	if !ok || st.url != url {
		st = &sourceState{pipeline: pipeline, url: url}
		f.states[pipeline] = st
	}
	etag, lastModified := st.etag, st.lastModified
	f.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return f.noteFailure(st, err, 0)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return f.noteFailure(st, err, 0)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		return f.noteAnswer(st, resp, nil, [32]byte{}, false)
	case resp.StatusCode != http.StatusOK:
		return f.noteFailure(st, fmt.Errorf("source answered status %d", resp.StatusCode), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, sourceBodyCap+1))
	if err != nil {
		return f.noteFailure(st, err, 0)
	}
	if len(body) > sourceBodyCap {
		return f.noteFailure(st, fmt.Errorf("source body exceeds %d bytes", sourceBodyCap), 0)
	}
	sum := sha256.Sum256(body)
	return f.noteAnswer(st, resp, body, sum, sum != st.seenSHA)
}

// noteAnswer records a successful answer -- validators, digest, health -- and
// frames it for the asking turn.
func (f *sourceWatcher) noteAnswer(st *sourceState, resp *http.Response, body []byte, sum [32]byte, changed bool) *sourceFrame {
	f.mu.Lock()
	hadFails := st.fails
	if et := resp.Header.Get("ETag"); et != "" {
		st.etag = et
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		st.lastModified = lm
	}
	st.status, st.errMsg, st.fails = resp.StatusCode, "", 0
	if changed {
		st.seenSHA = sum
	}
	f.mu.Unlock()
	if hadFails > 0 {
		f.logger.Info("declared source recovered", "pipeline", st.pipeline, "after_failures", hadFails)
	}
	if changed {
		f.logger.Info("declared source changed", "pipeline", st.pipeline, "bytes", len(body))
	}
	// The frame's body must be UTF-8 to survive the protocol's JSON string; the
	// digest and byte count stay those of the bytes the origin actually served.
	text, charset := decodeUTF8(body, resp.Header.Get("Content-Type"))
	if charset != "" {
		f.logger.Info("declared source transcoded", "pipeline", st.pipeline, "charset", charset)
	}
	return frameAnswer(st.pipeline, st.url, resp.StatusCode, changed, text, len(body), sum, charset)
}

// noteFailure records a failed fetch -- health, the daemon log on power-of-two
// consecutive counts so a tight asking loop cannot flood it -- and frames the
// failure for the asking turn (status and no body; the script decides).
func (f *sourceWatcher) noteFailure(st *sourceState, ferr error, status int) *sourceFrame {
	f.mu.Lock()
	st.fails++
	st.errMsg = ferr.Error()
	st.status = status
	fails := st.fails
	f.mu.Unlock()
	if fails&(fails-1) == 0 {
		f.logger.Warn("declared source fetch failed", "pipeline", st.pipeline, "url", st.url, "err", ferr, "consecutive", fails)
	}
	return frameAnswer(st.pipeline, st.url, status, false, nil, 0, [32]byte{}, "")
}

// frameAnswer renders one fetch's answer: the protocol line and the capture's
// digest summary. body is the UTF-8 text the pipeline reads; rawBytes and sum
// describe the bytes the origin served, and charset names the encoding they were
// decoded from when they were not UTF-8 already.
func frameAnswer(pipeline, url string, status int, changed bool, body []byte, rawBytes int, sum [32]byte, charset string) *sourceFrame {
	line, err := dispatch.EncodeSourceFrame(url, status, changed, body)
	if err != nil {
		// Encoding only fails on unmarshalable bytes, which a []byte-to-string
		// body never is; answer an empty failure frame rather than silence.
		line, _ = dispatch.EncodeSourceFrame(url, 0, false, nil)
	}
	summary, _ := json.Marshal(struct {
		Event   string `json:"event"`
		URL     string `json:"url"`
		Status  int    `json:"status"`
		Changed bool   `json:"changed"`
		Bytes   int    `json:"bytes"`
		SHA256  string `json:"sha256"`
		Charset string `json:"charset,omitempty"`
	}{Event: dispatch.TurnEventSource, URL: url, Status: status, Changed: changed, Bytes: rawBytes, SHA256: hex.EncodeToString(sum[:]), Charset: charset})
	return &sourceFrame{pipeline: pipeline, line: line, summary: string(summary)}
}

// health snapshots every fetched source's operator-visible state, sorted. A
// source appears once its pipeline has asked for it at least once this term.
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
		out = append(out, api.SourceHealth{Pipeline: name, URL: st.url, Status: st.status, Error: st.errMsg, ConsecutiveFails: st.fails})
	}
	return out
}

// forget drops a pipeline's remembered source state (its registration was
// destroyed); a re-registered pipeline starts validators fresh.
func (f *sourceWatcher) forget(pipeline string) {
	f.mu.Lock()
	delete(f.states, pipeline)
	f.mu.Unlock()
}
