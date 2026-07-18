package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
	"github.com/MateusAMP2119/iris-lakehouse/internal/exec"
	"github.com/MateusAMP2119/iris-lakehouse/internal/plugin"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the engine-side plugin seam (#215): resolving a pipeline's
// declared plugins to installed digest-verified binaries before a turn starts
// (a missing install or a drifted digest refuses the run), and dispatching a
// turn's call frames to run-to-completion tool subprocesses. The tool contract
// is one exec per call: argv is [binary, verb], the call's JSON args ride
// stdin, the JSON result rides stdout, exit 0 is ok, and the manifest's
// per-verb timeout bounds the whole call. The script never sees the plugin's
// process, environment, or network -- only the verb's JSON in and out.

// toolResultCap bounds a tool verb's inline stdout result. Large payloads are
// the service epic's file-spill; a tool reply is a small JSON document.
const toolResultCap = 8 << 20

// pluginResolution is one pipeline's resolved plugin set: the ledger pins and
// the alias-keyed installs the verb dispatch serves, cached under the
// declaration checksum they were resolved from.
type pluginResolution struct {
	checksum string
	pins     []store.RunPluginPin
	aliases  map[string]plugin.Installed
}

// pluginResolver resolves declared plugins against the engine home's installed
// plugin store, caching per pipeline keyed by declaration checksum (resolution
// hashes every binary, so a turn costs no re-hash until the declaration's bytes
// change). A nil *pluginResolver refuses every plugin-declaring pipeline: the
// shape tests compose without an engine home.
type pluginResolver struct {
	installer *plugin.Installer
	mu        sync.Mutex
	m         map[string]pluginResolution
}

// newPluginResolver builds the resolver over the engine home's plugin store.
func newPluginResolver(home string) *pluginResolver {
	return &pluginResolver{installer: plugin.NewInstaller(home), m: map[string]pluginResolution{}}
}

// resolve verifies every declared plugin for the pipeline: the ref parses, the
// plugin is installed, its binary still hashes to the manifest pin, its kind is
// tool, and its lifetime is one the engine can serve today. It returns the
// ledger pins (sorted by alias) and the alias-keyed installs; any failure
// refuses the run with a reason naming the alias. Zero declared plugins resolve
// to nothing.
func (r *pluginResolver) resolve(pipeline, checksum string, reqs map[string]declare.PluginRequirement) (pluginResolution, error) {
	if len(reqs) == 0 {
		return pluginResolution{}, nil
	}
	if r == nil {
		return pluginResolution{}, errors.New("plugins are declared but no plugin store is wired")
	}
	r.mu.Lock()
	got, ok := r.m[pipeline]
	r.mu.Unlock()
	if ok && got.checksum == checksum {
		return got, nil
	}

	res := pluginResolution{checksum: checksum, aliases: map[string]plugin.Installed{}}
	aliases := make([]string, 0, len(reqs))
	for alias := range reqs {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		req := reqs[alias]
		if req.Lifetime != "" && req.Lifetime != declare.LifetimeRun {
			return pluginResolution{}, fmt.Errorf("plugin %q lifetime %q is not supported yet; only run-lifetime tool plugins execute today", alias, req.Lifetime)
		}
		ref, err := plugin.ParseRef(req.Ref)
		if err != nil {
			return pluginResolution{}, fmt.Errorf("plugin %q: %w", alias, err)
		}
		inst, err := r.installer.Resolve(ref)
		if err != nil {
			return pluginResolution{}, fmt.Errorf("plugin %q: %w", alias, err)
		}
		if inst.Kind != plugin.KindTool {
			return pluginResolution{}, fmt.Errorf("plugin %q (%s) is a %s plugin; only tool plugins execute today", alias, ref, inst.Kind)
		}
		res.aliases[alias] = inst
		res.pins = append(res.pins, store.RunPluginPin{
			Alias:   alias,
			Plugin:  ref.Name,
			Version: ref.Version,
			Digest:  inst.Digest,
		})
	}

	r.mu.Lock()
	r.m[pipeline] = res
	r.mu.Unlock()
	return res, nil
}

// caller returns the verb dispatch for a resolution: nil when nothing was
// declared (every call then answers with an err frame), otherwise a
// pluginVerbCaller execing the resolved tools in dir.
func (res pluginResolution) caller(runner exec.Runner, dir string) verbCaller {
	if len(res.aliases) == 0 {
		return nil
	}
	return &pluginVerbCaller{runner: runner, dir: dir, aliases: res.aliases}
}

// pluginVerbCaller dispatches one turn's call frames to resolved tool plugins:
// one bounded subprocess per call, scrubbed environment, the pipeline folder as
// the confined workdir.
type pluginVerbCaller struct {
	runner  exec.Runner
	dir     string
	aliases map[string]plugin.Installed
}

// boundedBuffer collects a tool's stdout up to toolResultCap; past it the
// overflow is dropped and flagged (the call then fails rather than inlining an
// oversized result).
type boundedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

// Write appends p up to the cap; capture never errors the pipe.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := toolResultCap - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.overflow = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

// Call executes one tool verb: exec [binary, verb] with the args on stdin,
// bounded by the manifest's per-verb timeout (the context kills the process
// group), and returns the stdout JSON result. Every failure -- unknown alias or
// verb, start refusal, timeout, non-zero exit, non-JSON output -- is an error
// the turn driver turns into an err reply.
func (c *pluginVerbCaller) Call(ctx context.Context, call dispatch.TurnCall) (json.RawMessage, error) {
	alias, verb, _ := dispatch.CutVerb(call.Verb)
	inst, ok := c.aliases[alias]
	if !ok {
		return nil, fmt.Errorf("plugin alias %q is not declared by this pipeline", alias)
	}
	v, ok := inst.Manifest.Verbs[verb]
	if !ok {
		return nil, fmt.Errorf("plugin %s has no verb %q (verbs: %s)",
			inst.Ref, verb, strings.Join(inst.Manifest.VerbNames(), ", "))
	}

	timeout := v.Duration()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &boundedBuffer{}
	tail := &tailRing{}
	h, err := c.runner.Start(cctx, exec.Spec{
		Dir:    c.dir,
		Argv:   []string{inst.Path, verb},
		Env:    scrubbedPluginEnv(),
		Stdin:  bytes.NewReader(call.Args),
		Stdout: out,
		Stderr: tail,
	})
	if err != nil {
		return nil, fmt.Errorf("start plugin %s: %w", inst.Ref, err)
	}
	status, werr := h.Wait()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("plugin %s verb %q timed out after %s", inst.Ref, verb, timeout)
	}
	if werr != nil {
		return nil, fmt.Errorf("plugin %s verb %q: %w", inst.Ref, verb, werr)
	}
	if status.Signaled || status.Code != 0 {
		detail := exitDetail(status)
		if t := strings.TrimSpace(tail.String()); t != "" {
			detail += "; stderr tail: " + t
		}
		return nil, fmt.Errorf("plugin %s verb %q failed: %s", inst.Ref, verb, detail)
	}
	if out.overflow {
		return nil, fmt.Errorf("plugin %s verb %q result exceeds %d bytes", inst.Ref, verb, toolResultCap)
	}
	result := bytes.TrimSpace(out.buf.Bytes())
	if len(result) == 0 {
		result = []byte("{}")
	}
	if !json.Valid(result) {
		return nil, fmt.Errorf("plugin %s verb %q returned invalid JSON", inst.Ref, verb)
	}
	return json.RawMessage(result), nil
}

// scrubbedPluginEnv is the tool subprocess environment: PATH only. A plugin
// binary sees no daemon environment, no credentials, and no engine settings --
// the manifest's verbs are its whole contract.
func scrubbedPluginEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH")}
}
