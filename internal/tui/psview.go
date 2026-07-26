package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
)

// errBody is the error object inside a daemon JSON error envelope.
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// runCancelResult is the leader's reply to POST /run/cancel.
type runCancelResult struct {
	Run   string `json:"run"`
	State string `json:"state"`
}

// This file is the `iris ps` live view's runtime: the daemon client both
// output modes share, the poller goroutine that refreshes the view every
// second, and the single-writer event loop that owns the frame. The loop is
// the only goroutine that renders; keys, polls, and cancel outcomes arrive as
// messages. A failed poll never retries: the engine is gone, the view tears
// down, and the command exits no-daemon (3) with start guidance.

// psPollInterval is the live view's refresh cadence.
const psPollInterval = time.Second

// psUnreachableWarn is the standing banner while the poller cannot reach the
// engine: the view keeps its last state and the poller keeps retrying.
const psUnreachableWarn = "engine unreachable · showing last known state · retrying"

// psHistoryRefreshPolls is how many polls ride between ?history=1 reads: the
// view seeds its load rings from the daemon's recorded history once at open,
// then re-seeds every this-many ticks so the coarse rings (sealed daemon-side
// once a minute) stay current without the history document riding every poll.
const psHistoryRefreshPolls = 60

// psMaxLogLines bounds the log tail held client-side for the run detail
// screen; scrollback beyond it is `iris run logs`' job.
const psMaxLogLines = 2000

// ErrEngineGone signals the poller lost the daemon mid-view: the loop exits,
// the terminal restores, and ps() maps it to the no-daemon fault.
var ErrEngineGone = errors.New("engine no longer reachable")

// HTTPError is a /ps read the daemon answered but refused: a non-200 status
// or an undecodable body. It is a reached-daemon failure, so ps() maps it to
// operation-failed (exit 4) carrying the daemon's own message -- never the
// "start the engine" guidance a transport failure earns.
type HTTPError struct {
	Status  int
	Code    string
	Message string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("daemon returned status %d from /ps", e.Status)
}

// Client is the resolved daemon target both ps output modes drive: the
// one-shot JSON emit, the live poller, and the run-detail actions.
type Client struct {
	client  *http.Client
	base    string
	token   string
	overTCP bool
	cache   *cache // last-known-state cache for this target; nil drops it
}

// NewClient builds the ps client for a resolved daemon target. cacheTarget is
// the engine address used as the last-known-state cache key (e.g. unix:///… or
// host:port); empty disables caching.
func NewClient(httpClient *http.Client, base, token string, overTCP bool, cacheTarget string) *Client {
	var c *cache
	if cacheTarget != "" {
		c = newCache(cacheTarget)
	}
	return &Client{client: httpClient, base: base, token: token, overTCP: overTCP, cache: c}
}

// FetchPs reads GET /ps for the one-shot JSON path and the live view seed.
func (c *Client) FetchPs(ctx context.Context, all, history bool) (api.PsPayload, error) {
	return c.fetchPs(ctx, all, history)
}

// FetchPipelines reads the full pipeline listing for the live view seed.
func (c *Client) FetchPipelines(ctx context.Context) ([]api.PipelineListItem, error) {
	return c.fetchPipelines(ctx)
}

// LoadCache revives the last-known-state snapshot for this target.
func (c *Client) LoadCache() (Snapshot, time.Time, bool) {
	if c == nil || c.cache == nil {
		return Snapshot{}, time.Time{}, false
	}
	return c.cache.load()
}

// SaveCache persists a snapshot as the target's last known state.
func (c *Client) SaveCache(snap Snapshot) {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.save(snap)
}

// get issues one GET against the daemon, presenting the PAT over TCP.
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.overTCP && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.client.Do(req)
}

// fetchPs reads the /ps readout, with the daemon-held load history attached
// under history. A transport failure returns the dial error unwrapped (the
// caller classifies it no-daemon, exit 3); a reached daemon answering non-200,
// or a body that does not decode, is a *HTTPError carrying the daemon's
// error message (operation-failed, exit 4). Never a retry in either case.
func (c *Client) fetchPs(ctx context.Context, all, history bool) (api.PsPayload, error) {
	q := url.Values{}
	if all {
		q.Set("all", "true")
	}
	if history {
		q.Set("history", "1")
	}
	path := "/ps"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := c.get(ctx, path)
	if err != nil {
		return api.PsPayload{}, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error errBody `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		return api.PsPayload{}, &HTTPError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
	}
	var env struct {
		Data api.PsPayload `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return api.PsPayload{}, &HTTPError{Status: resp.StatusCode, Code: "decode", Message: fmt.Sprintf("decode /ps response: %v", err)}
	}
	return env.Data, nil
}

// fetchPipelines reads the full pipeline listing (?all=1, idle pipelines
// included) for the lane and pipeline screens' composition.
func (c *Client) fetchPipelines(ctx context.Context) ([]api.PipelineListItem, error) {
	resp, err := c.get(ctx, "/pipeline/list?all=1")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon returned status %d from /pipeline/list", resp.StatusCode)
	}
	var env struct {
		Data api.PipelineListResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode /pipeline/list response: %w", err)
	}
	return env.Data.Pipelines, nil
}

// fetchRunLogs reads the tail window of a run's captured output, raw
// naturalized lines. The poller accumulates windows across ticks (raw lines
// are stable across polls; humanized fold lines are not) and humanizes the
// accumulated tail for display.
func (c *Client) fetchRunLogs(ctx context.Context, id string) ([]string, error) {
	resp, err := c.get(ctx, "/runs/"+id+"/logs?tailbytes=65536")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon returned status %d from /runs/%s/logs", resp.StatusCode, id)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

// logGapMarker is the line the accumulator inserts when consecutive tail
// windows do not overlap (output outpaced the follower between polls).
const logGapMarker = "· · · output outpaced the follower; lines skipped · · ·"

// mergeLogTail folds a freshly fetched tail window into the accumulated log:
// the overlap between the accumulation's suffix and the window's prefix is
// found and only the new lines append, so watching a run accumulates history
// instead of rotating a fixed window. A window with no overlap appends whole
// behind a gap marker. The accumulation is capped from the head.
func mergeLogTail(acc, win []string) []string {
	if len(win) == 0 {
		return acc
	}
	if len(acc) == 0 {
		return capLogTail(append(acc, win...))
	}
	if k := tailOverlap(acc, win); k > 0 {
		acc = append(acc, win[k:]...)
	} else if k := tailOverlap(acc[:len(acc)-1], win); k > 0 {
		// The accumulation's last line was a mid-write partial the daemon cut;
		// the window carries its completed form, so the partial is replaced.
		acc = append(acc[:len(acc)-1], win[k:]...)
	} else {
		acc = append(acc, logGapMarker)
		acc = append(acc, win...)
	}
	return capLogTail(acc)
}

// capLogTail bounds the accumulated log from the head.
func capLogTail(acc []string) []string {
	if len(acc) > psMaxLogLines {
		acc = append(acc[:0], acc[len(acc)-psMaxLogLines:]...)
	}
	return acc
}

// tailOverlap returns the largest k where acc's last k lines equal win's
// first k.
func tailOverlap(acc, win []string) int {
	limit := len(win)
	if len(acc) < limit {
		limit = len(acc)
	}
	for k := limit; k > 0; k-- {
		match := true
		for i := 0; i < k; i++ {
			if acc[len(acc)-k+i] != win[i] {
				match = false
				break
			}
		}
		if match {
			return k
		}
	}
	return 0
}

// cancelRun POSTs the run cancel and renders the outcome as the view's note
// line: success, a not_leader rejection, or the daemon's error message. The
// view keeps running whatever the outcome -- an in-view action never crashes
// the view.
func (c *Client) cancelRun(ctx context.Context, id string) string {
	body := fmt.Sprintf(`{"run":%q}`, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/run/cancel", strings.NewReader(body))
	if err != nil {
		return "cancel failed: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if c.overTCP && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "cancel failed: " + err.Error()
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusOK {
		var env struct {
			Data runCancelResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			return "cancelled " + id
		}
		return fmt.Sprintf("cancelled %s (%s)", env.Data.Run, env.Data.State)
	}
	// The not_leader rejection carries a leader hint; keep the same retry
	// guidance `iris run cancel` prints.
	var env struct {
		Error struct {
			Message string `json:"message"`
			Leader  string `json:"leader"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	msg := env.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("daemon status %d", resp.StatusCode)
	}
	if env.Error.Leader != "" {
		msg += "; retry against the leader (" + env.Error.Leader + ")"
	}
	return "cancel failed: " + msg
}

// catalogAction services one overlay request against the daemon (#219).
func (c *Client) catalogAction(ctx context.Context, req psCatalogReq) psCatalogMsg {
	switch req.kind {
	case psCatalogList:
		return c.fetchCatalog(ctx)
	case psCatalogAddSource:
		return c.addCatalogSource(ctx, req)
	default:
		return c.installPack(ctx, req)
	}
}

// addCatalogSource POSTs /catalog/sources for the '+' add-source prompt.
func (c *Client) addCatalogSource(ctx context.Context, req psCatalogReq) psCatalogMsg {
	msg := psCatalogMsg{kind: psCatalogAddSource}
	body, _ := json.Marshal(api.CatalogSourceRequest{URL: req.url})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/catalog/sources", bytes.NewReader(body))
	if err != nil {
		msg.err = "catalog source: " + err.Error()
		return msg
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.overTCP && c.token != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(hreq)
	if err != nil {
		msg.err = "catalog source: engine unreachable: " + err.Error()
		return msg
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error errBody `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		msg.err = catalogFailText("catalog source add", resp.StatusCode, env.Error.Message, "")
		return msg
	}
	var env struct {
		Data api.CatalogSourceResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		msg.err = "decode /catalog/sources response: " + err.Error()
		return msg
	}
	msg.sources = env.Data.Sources
	return msg
}

// fetchCatalog reads GET /catalog for the overlay's pack list.
func (c *Client) fetchCatalog(ctx context.Context) psCatalogMsg {
	msg := psCatalogMsg{kind: psCatalogList}
	resp, err := c.get(ctx, "/catalog")
	if err != nil {
		msg.err = "catalog unreachable: " + err.Error()
		return msg
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error errBody `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		msg.err = catalogFailText("catalog list", resp.StatusCode, env.Error.Message, "")
		return msg
	}
	var env struct {
		Data api.CatalogListResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		msg.err = "decode /catalog response: " + err.Error()
		return msg
	}
	msg.packs, msg.warnings = env.Data.Packs, env.Data.Warnings
	return msg
}

// installPack POSTs /catalog/install for the batch apply (always install+apply).
func (c *Client) installPack(ctx context.Context, req psCatalogReq) psCatalogMsg {
	msg := psCatalogMsg{kind: psCatalogApply}
	body, _ := json.Marshal(api.CatalogInstallRequest{Pack: req.pack, Apply: true, Force: req.force})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/catalog/install", bytes.NewReader(body))
	if err != nil {
		msg.err = "catalog install: " + err.Error()
		return msg
	}
	hreq.Header.Set("Content-Type", "application/json")
	if c.overTCP && c.token != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(hreq)
	if err != nil {
		msg.err = "catalog install: engine unreachable: " + err.Error()
		return msg
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error struct {
				Message string `json:"message"`
				Leader  string `json:"leader"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		msg.err = catalogFailText("catalog install", resp.StatusCode, env.Error.Message, env.Error.Leader)
		return msg
	}
	var env struct {
		Data api.CatalogInstallResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		msg.err = "decode /catalog/install response: " + err.Error()
		return msg
	}
	msg.res = &env.Data
	return msg
}

// catalogFailText renders a daemon refusal for the overlay banner, leader hint included.
func catalogFailText(op string, status int, message, leader string) string {
	if message == "" {
		message = fmt.Sprintf("daemon status %d", status)
	}
	if leader != "" {
		message += " · leader: " + leader
	}
	return op + " failed: " + message
}

// drainClose drains and closes a response body so the connection is reused.
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// psPollMsg is one poll outcome: a fresh snapshot (with a warning line when a
// soft fetch failed), an unreachable tick (the engine did not answer; the view
// keeps its last state and the poller keeps trying), or the error that ends
// the view (the daemon answered and refused the read).
type psPollMsg struct {
	snap        Snapshot
	warn        string
	unreachable bool
	err         error
}

// pollPs is the live view's poller goroutine: every tick it re-reads the /ps
// history and the pipeline listing, plus the focused run's log tail, and ships
// one snapshot. A transport failure is an unreachable tick, not a teardown:
// the poller keeps ticking and reconnects when the engine returns (the
// docker-parity behavior the stale banner narrates). Only a reached daemon
// REFUSING the read ends the poller (and the view). The listing and logs are
// soft -- their last good value rides along. Cancel requests arrive on
// cancelCh and their outcomes return as notes.
func pollPs(ctx context.Context, c *Client, every time.Duration,
	focusCh <-chan string, cancelCh <-chan string, polls chan psPollMsg, notes chan<- string) {
	var (
		focus       string
		lastPipes   []api.PipelineListItem
		lastLogs    []string
		runPipes    map[string]string // run id -> pipeline, from the last payload
		runSwitched bool              // focus moved to the same pipeline's next run
		ticks       int
		journal     *psJournal // accumulated write activity (#238 phase 3)
	)
	poll := func(history bool) bool {
		ps, err := c.fetchPs(ctx, true, history)
		if err != nil {
			if ctx.Err() != nil {
				return false // the view is exiting; not an engine-gone verdict
			}
			var herr *HTTPError
			if errors.As(err, &herr) {
				sendPoll(polls, psPollMsg{err: err})
				return false
			}
			sendPoll(polls, psPollMsg{unreachable: true})
			return true // keep ticking: the view shows its last state until the engine returns
		}
		// The listing and the log tail are soft: their last good value rides
		// along, but the failure is surfaced -- an empty lanes screen on a
		// healthy engine must say why.
		var warn string
		if pipes, perr := c.fetchPipelines(ctx); perr == nil {
			lastPipes = pipes
		} else {
			warn = "pipeline listing unavailable; lanes may be incomplete"
		}
		runPipes = map[string]string{}
		for _, r := range ps.Runs {
			runPipes[r.ID] = r.Pipeline
		}
		if focus != "" {
			if win, lerr := c.fetchRunLogs(ctx, focus); lerr == nil {
				if runSwitched {
					// Same pipeline, next run: a new capture file, appended
					// whole -- the pane is the pipeline's continuous console,
					// run boundaries marked by their [iris] stamps.
					lastLogs = capLogTail(append(lastLogs, win...))
					runSwitched = false
				} else {
					lastLogs = mergeLogTail(lastLogs, win)
				}
			} else if warn == "" {
				warn = "run logs unavailable"
			}
		}
		// A failing declared source outranks softer warnings: its turns stay
		// quiet, so this line is the pane's only live trace of the failure.
		for _, sh := range ps.Sources {
			if sh.ConsecutiveFails > 0 {
				warn = fmt.Sprintf("source %s failing ×%d (%s) · backing off", sh.Pipeline, sh.ConsecutiveFails, sh.Error)
				break
			}
		}
		// The activity aggregate is soft like the listing: a failing (or
		// missing) route leaves the last accumulated state riding along.
		since := int64(0)
		if journal != nil {
			since = journal.Watermark
		}
		if act, aerr := c.fetchJournalActivity(ctx, since); aerr == nil {
			journal = foldJournal(journal, act)
		}
		snap := Snapshot{Ps: ps, Pipelines: lastPipes, Journal: journal}
		if focus != "" {
			snap.Logs, snap.LogsRun = humanizeCapture(lastLogs), focus
		}
		// A history-carrying poll (once a minute) refreshes the last-known-state
		// cache: the snapshot a later unreachable-at-open view revives.
		if ps.History != nil {
			c.cache.save(snap)
		}
		sendPoll(polls, psPollMsg{snap: snap, warn: warn})
		return true
	}

	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-focusCh:
			// A focus move within one pipeline (the loop minted its next run)
			// keeps the accumulated console and appends; anything else -- a
			// different pipeline, an unknown run -- starts fresh.
			samePipe := f != "" && focus != "" && runPipes[f] != "" && runPipes[f] == runPipes[focus]
			if samePipe && f != focus {
				runSwitched = true
			} else if f != focus {
				lastLogs, runSwitched = nil, false
			}
			focus = f
			if focus != "" && !poll(false) { // fetch the tail now, not a tick later
				return
			}
		case id := <-cancelCh:
			select {
			case notes <- c.cancelRun(ctx, id):
			case <-ctx.Done():
				return
			}
		case <-tick.C:
			// One poll in psHistoryRefreshPolls re-reads the daemon's recorded
			// history so the rings re-seed; the rest ride the light payload.
			ticks++
			if !poll(ticks%psHistoryRefreshPolls == 0) {
				return
			}
		}
	}
}

// sendPoll ships a snapshot with drop-and-replace semantics on the buffered
// channel (the poller holds it bidirectionally for exactly this): a slow
// render never backs the poller up, and the loop always sees the freshest
// snapshot.
func sendPoll(polls chan psPollMsg, m psPollMsg) {
	for {
		select {
		case polls <- m:
			return
		default:
		}
		select {
		case <-polls:
		default:
		}
	}
}

// psView bundles the event loop's seams: the frame writer, the message
// channels, the geometry probe, and the painter. Production wires the real
// terminal and poller; tests wire scripted channels, a buffer, and a fixed
// size.
type psView struct {
	out      io.Writer
	p        painter
	size     func() (w, h int)
	keys     <-chan psKey
	polls    <-chan psPollMsg
	notes    <-chan string
	focusCh  chan<- string
	cancelCh chan<- string
	// catalogMsgs delivers overlay action outcomes; runCatalog services a parked
	// overlay request off the loop (#219). Nil seams leave the overlay inert.
	catalogMsgs <-chan psCatalogMsg
	runCatalog  func(psCatalogReq)
}

// runPsLoop is the live view's single writer: render the current state, then
// absorb exactly one message. It returns nil on a clean exit (q, Ctrl-C,
// SIGTERM) and the poll error when the poller lost the daemon (a transport
// failure) or the daemon refused the read (a *HTTPError).
func runPsLoop(ctx context.Context, v *psView, m *psModel) error {
	// The poller starts with no log target; the first push below points it at
	// the initial selection's run, and every later push follows a change from
	// any message (a key moved the selection, a poll started or finished runs).
	sentFocus := ""
	syncFocus := func() {
		if f := m.focus(); f != sentFocus {
			select {
			case v.focusCh <- f:
				sentFocus = f
			default:
			}
		}
	}
	// Catalog fetches can be parked outside a keypress too (the idle card's
	// inline catalog opens with the model or on a poll), so drain everywhere.
	drainCatalog := func() {
		if req := m.takeCatalogReq(); req != nil && v.runCatalog != nil {
			v.runCatalog(*req)
		}
	}
	syncFocus()
	drainCatalog()
	for {
		w, h := v.size()
		if _, err := v.out.Write(renderPsFrame(m, w, h, !v.p.enabled).render(v.p)); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-v.keys:
			if !ok {
				return nil
			}
			cancelIDs := m.update(k)
			if m.quit {
				return nil
			}
			for _, id := range cancelIDs {
				select {
				case v.cancelCh <- id:
				default:
					m.note = "cancel already in flight"
				}
			}
			drainCatalog()
			syncFocus()
		case pm := <-v.polls:
			if pm.err != nil {
				return pm.err
			}
			if pm.unreachable {
				// Keep the last state on screen under a standing banner; the
				// poller is already retrying, and the next good poll clears it.
				m.warn = psUnreachableWarn
				continue
			}
			m.warn = pm.warn
			m.absorb(pm.snap)
			drainCatalog()
			syncFocus()
		case note := <-v.notes:
			m.note = note
		case cm := <-v.catalogMsgs:
			m.absorbCatalog(cm)
			drainCatalog() // a batch apply chains its next pack off the absorb
			syncFocus()
		}
	}
}

// TargetLabel names the watched engine for cache keys and connection identity.
func TargetLabel(s config.Settings, overTCP bool) string {
	if overTCP {
		return "remote " + strings.TrimPrefix(strings.TrimPrefix(s.Host, "https://"), "http://")
	}
	return "local " + s.Socket
}
