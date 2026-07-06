package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/MateusAMP2119/iris-engine-cli/internal/declare"
)

// crossModeReaderYAML is a pipeline declaration that reads one upstream table.
// The seam under test treats the reader as permanent-data and its upstream as
// disposable-mode, the mid-promotion state that raises a cross-mode read warning.
const crossModeReaderYAML = `name: load_orders
run: [python, main.py]
reads:
  - table: raw.orders_staging
    fields: [id]
`

// TestCrossModeWarningRidesJSON proves the cross-mode read warning is carried in
// apply's --json output (specification section 5). The CLI apply path computes
// advisory warnings through an injected seam and, under --json, surfaces them in
// the success envelope's warnings array. The seam stands in for the meta-backed
// data-mode facts apply reads once it runs against the daemon (E03.10); here it
// runs the real declare.CheckCrossModeReads over the parsed declaration, so what
// is proven is the warning structure riding the --json envelope end to end.
func TestCrossModeWarningRidesJSON(t *testing.T) {
	// spec: S05/cross-mode-warning-rides-json
	dir := t.TempDir()
	target := filepath.Join(dir, "iris-declare.yaml")
	if err := os.WriteFile(target, []byte(crossModeReaderYAML), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	// Inject the meta-backed data-mode facts: this reader is permanent-data, and
	// every table it reads is a disposable-mode upstream (mid-promotion).
	a.applyWarnings = func(d *declare.Declaration) []declare.Warning {
		if d.Kind != declare.KindPipeline {
			return nil
		}
		var ups []declare.UpstreamRead
		for _, r := range d.Pipeline.Reads {
			ups = append(ups, declare.UpstreamRead{Table: r.Table, Mode: declare.DataDisposable})
		}
		return declare.CheckCrossModeReads(declare.DataPermanent, ups)
	}

	code := a.run([]string{"--json", "declare", "apply", target})
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (apply surfaces its warnings and succeeds)\nstdout: %s\nstderr: %s", code, exitOK, out.String(), errb.String())
	}

	// stdout is exactly one JSON document -- the success envelope carrying warnings.
	var env struct {
		Data struct {
			Warnings []declare.Warning `json:"warnings"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\nstdout: %q", err, out.String())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout carries content after the single JSON envelope: %q", out.String())
	}

	if len(env.Data.Warnings) != 1 {
		t.Fatalf("envelope warnings = %d, want 1\nstdout: %s", len(env.Data.Warnings), out.String())
	}
	w := env.Data.Warnings[0]
	if w.Kind != declare.WarnCrossModeRead {
		t.Errorf("warning kind = %q, want cross_mode_read", w.Kind)
	}
	if w.Table != "raw.orders_staging" {
		t.Errorf("warning table = %q, want raw.orders_staging", w.Table)
	}
	if w.Message == "" {
		t.Error("warning rode --json with no message")
	}
}

// TestApplyNoWarningsUnchanged proves the warning seam never disturbs the ordinary
// apply path: with no cross-mode warning to surface, apply resolves its target and
// reaches the daemon-dial stub exactly as before (exit 3, no daemon reachable).
// This pins that the --json warning surface is additive, not a behavior change to
// apply's existing single-file resolution contract.
func TestApplyNoWarningsUnchanged(t *testing.T) {
	// spec: S05/cross-mode-warning-rides-json
	dir := t.TempDir()
	target := filepath.Join(dir, "iris-declare.yaml")
	if err := os.WriteFile(target, []byte(crossModeReaderYAML), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sock := shortSocket(t)
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	// A reader with no disposable upstream produces no warning; apply proceeds.
	a.applyWarnings = func(_ *declare.Declaration) []declare.Warning {
		return declare.CheckCrossModeReads(declare.DataPermanent, nil)
	}
	code := a.run([]string{"--socket", sock, "--json", "declare", "apply", target})
	if code != exitNoDaemon {
		t.Fatalf("exit = %d, want %d (no warning: apply reaches the daemon-dial stub)\nstdout: %s\nstderr: %s", code, exitNoDaemon, out.String(), errb.String())
	}
}
