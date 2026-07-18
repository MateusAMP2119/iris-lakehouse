package store

import (
	"context"
	"fmt"
)

// This file is the plugin provenance write surface (#215). A run that declares
// plugins records its pins -- (alias, plugin, version, digest), verified by a
// resolve before the turn started -- and every mid-run verb call records its
// verb, args digest, and response digest (or error), so an external effect is a
// recorded ingest event, never a hole in lineage. Both ride the single Writer.

// RunPluginPin is one recorded plugin pin: the declared alias and the resolved
// identity plus binary digest the run executed under.
type RunPluginPin struct {
	// Alias is the declaration's alias key (the call prefix).
	Alias string
	// Plugin is the plugin name.
	Plugin string
	// Version is the plugin version.
	Version string
	// Digest is the hex sha256 the installed binary hashed to at resolve.
	Digest string
}

// The plugin_calls status values.
const (
	// PluginCallOK records a call the verb answered.
	PluginCallOK = "ok"
	// PluginCallErr records a call that failed (crash, timeout, refusal); the
	// engine still replied with an err frame.
	PluginCallErr = "err"
)

// PluginCallRecord is one recorded plugin call: the pipeline's call id, the
// alias-qualified verb, and the digests that pin what crossed the seam.
type PluginCallRecord struct {
	// CallID is the pipeline's call id, unique within the run.
	CallID string
	// Verb is the alias-qualified verb ("mail.send").
	Verb string
	// ArgsDigest is the hex sha256 of the call's argument bytes.
	ArgsDigest string
	// ResponseDigest is the hex sha256 of the ok result bytes; empty for err.
	ResponseDigest string
	// Status is ok or err.
	Status string
	// Error is the failure message an err call carried; empty for ok.
	Error string
}

// insertRunPluginsSQL records a run's plugin pins in one statement over
// parallel-array unnest; empty arrays record nothing.
const insertRunPluginsSQL = `INSERT INTO run_plugins (run_id, alias, plugin, version, digest)
SELECT $1, a, p, v, d
FROM unnest($2::text[], $3::text[], $4::text[], $5::text[]) AS t(a, p, v, d)`

// insertPluginCallsSQL records a run's plugin calls in one statement over
// parallel-array unnest; recorded_at is filled DB-side like every audit string.
const insertPluginCallsSQL = `INSERT INTO plugin_calls
    (run_id, call_id, verb, args_digest, response_digest, status, error, recorded_at)
SELECT $1, c, v, a, NULLIF(r, ''), s, NULLIF(e, ''), now()::text
FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) AS t(c, v, a, r, s, e)`

// pluginPinArrays folds pins into the parallel arrays insertRunPluginsSQL binds.
func pluginPinArrays(pins []RunPluginPin) (aliases, plugins, versions, digests []string) {
	for _, p := range pins {
		aliases = append(aliases, p.Alias)
		plugins = append(plugins, p.Plugin)
		versions = append(versions, p.Version)
		digests = append(digests, p.Digest)
	}
	return aliases, plugins, versions, digests
}

// RecordRunPlugins records a pre-minted run's plugin pins (the mint-first manual
// path stamps them when the run starts; the turn-run mints carry pins in their
// own atomic CTE instead). Recording zero pins is a no-op.
func (w *Writer) RecordRunPlugins(ctx context.Context, runID string, pins []RunPluginPin) error {
	if len(pins) == 0 {
		return nil
	}
	aliases, plugins, versions, digests := pluginPinArrays(pins)
	if err := w.conn.Exec(ctx, insertRunPluginsSQL, runID, aliases, plugins, versions, digests); err != nil {
		return fmt.Errorf("store: writer record run plugins for run %s: %w", runID, err)
	}
	return nil
}

// RecordPluginCalls records a run's plugin-call audit rows once the run's row
// exists (a turn's calls are held in memory until its run mints; a turn that
// called a plugin always records). Recording zero calls is a no-op.
func (w *Writer) RecordPluginCalls(ctx context.Context, runID string, calls []PluginCallRecord) error {
	if len(calls) == 0 {
		return nil
	}
	var ids, verbs, argsDigests, respDigests, statuses, errs []string
	for _, c := range calls {
		ids = append(ids, c.CallID)
		verbs = append(verbs, c.Verb)
		argsDigests = append(argsDigests, c.ArgsDigest)
		respDigests = append(respDigests, c.ResponseDigest)
		statuses = append(statuses, c.Status)
		errs = append(errs, c.Error)
	}
	if err := w.conn.Exec(ctx, insertPluginCallsSQL, runID, ids, verbs, argsDigests, respDigests, statuses, errs); err != nil {
		return fmt.Errorf("store: writer record plugin calls for run %s: %w", runID, err)
	}
	return nil
}
