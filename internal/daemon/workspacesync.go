package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the leader's workspace sync (POST /workspace/apply): discover
// every declaration under the workspace tree, diff each file's checksum
// against its recorded declaration head, and apply what changed through the
// existing single-file apply — in catalog.ApplyOrder dependency order, so
// "edit yaml + script, run `iris apply`" is the whole creation flow.

// ApplyWorkspace routes to the live orchestrator, or faults when none is installed.
func (c *controlPlane) ApplyWorkspace(ctx context.Context, req api.WorkspaceApplyRequest) (api.WorkspaceApplyResult, error) {
	o := c.orchestrator()
	if o == nil {
		return api.WorkspaceApplyResult{}, api.ErrControlUnavailable
	}
	return o.applyWorkspace(ctx, req)
}

// syncTarget is one discovered declaration file: bytes, checksum, and
// declared identity (its workspace-relative path keys the targets map).
type syncTarget struct {
	data     []byte
	checksum string
	kind     string
	target   string
	detail   string
}

// applyWorkspace runs the workspace sync. Discovery validates the whole tree
// first (a malformed folder refuses the sync before any write); the plan then
// carries every declaration with its action, and the changed ones are applied
// in dependency order through the same path as a single-file apply. Recorded
// heads whose file is gone are reported missing, never destroyed.
func (o *controlOrchestrator) applyWorkspace(ctx context.Context, req api.WorkspaceApplyRequest) (api.WorkspaceApplyResult, error) {
	ws, err := declare.DiscoverWorkspace(o.workspace)
	if err != nil {
		return api.WorkspaceApplyResult{}, err
	}

	targets, err := o.collectSyncTargets(ws)
	if err != nil {
		return api.WorkspaceApplyResult{}, err
	}

	order, err := workspaceApplyOrder(targets)
	if err != nil {
		return api.WorkspaceApplyResult{}, err
	}

	heads := map[string]string{}
	if hr, ok := o.registry.(store.DeclarationHeadReader); ok {
		if heads, err = hr.DeclarationHeads(ctx); err != nil {
			return api.WorkspaceApplyResult{}, err
		}
	}

	res := api.WorkspaceApplyResult{DryRun: req.DryRun, Changes: []api.WorkspaceChange{}}
	for _, rel := range order {
		t := targets[rel]
		change := api.WorkspaceChange{Kind: t.kind, Target: t.target, Path: rel, Detail: t.detail}
		switch prev, known := heads[rel]; {
		case !known:
			change.Action = api.ChangeRegistered
		case prev != t.checksum:
			change.Action = api.ChangeUpdated
		default:
			change.Action = api.ChangeUnchanged
		}

		if change.Action != api.ChangeUnchanged && !req.DryRun {
			one, aerr := o.apply(ctx, api.ControlRequest{Path: rel})
			if aerr != nil {
				return api.WorkspaceApplyResult{}, fmt.Errorf("workspace apply: applied %d change(s) but %s failed: %w", res.Applied, rel, aerr)
			}
			for _, warn := range one.Warnings {
				res.Warnings = append(res.Warnings, t.target+": "+warn)
			}
			if serr := o.recordHead(ctx, rel, t.checksum); serr != nil {
				return api.WorkspaceApplyResult{}, serr
			}
			res.Applied++
		}
		res.Changes = append(res.Changes, change)
	}

	// A sync with nothing to apply still provisions once: the schemas/ tree can
	// change without any declaration changing, and provisioning is idempotent.
	if res.Applied == 0 && !req.DryRun {
		if err := o.provision(ctx, false); err != nil {
			return api.WorkspaceApplyResult{}, err
		}
	}

	// Heads whose file is gone: reported, kept. Teardown stays an explicit
	// `iris declare destroy` — the sync never removes anything.
	var gone []string
	for rel := range heads {
		if _, present := targets[rel]; !present {
			gone = append(gone, rel)
		}
	}
	sort.Strings(gone)
	for _, rel := range gone {
		res.Changes = append(res.Changes, api.WorkspaceChange{
			Action: api.ChangeMissing, Path: rel, Target: path.Dir(rel),
			Detail: "file gone from workspace; still registered — `iris declare destroy` tears it down",
		})
	}
	return res, nil
}

// collectSyncTargets reads every discovered declaration file and keys it by
// workspace-relative path (slash-separated, the declaration-head key).
func (o *controlOrchestrator) collectSyncTargets(ws *declare.Workspace) (map[string]syncTarget, error) {
	targets := map[string]syncTarget{}
	add := func(rel, kind, target, detail string) error {
		data, err := os.ReadFile(filepath.Join(o.workspace, filepath.FromSlash(rel))) //nolint:gosec // G304: the path is a discovered declaration under the leader-owned workspace root.
		if err != nil {
			return fmt.Errorf("workspace apply: read %s: %w", rel, err)
		}
		sum := sha256.Sum256(data)
		targets[rel] = syncTarget{data: data, checksum: hex.EncodeToString(sum[:]), kind: kind, target: target, detail: detail}
		return nil
	}
	for _, c := range ws.Composers {
		rel := path.Join("pipelines", c.Lane, "iris-declare.yaml")
		if err := add(rel, "composer", c.Lane, fmt.Sprintf("orders %d members", len(c.Spec.Order))); err != nil {
			return nil, err
		}
	}
	for _, p := range ws.Pipelines {
		rel := path.Join("pipelines", p.Lane, p.Declaration.Name, "iris-declare.yaml")
		if err := add(rel, "pipeline", p.Declaration.Name, "lane "+p.Lane); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

// workspaceApplyOrder orders the discovered declarations dependency-first by
// riding catalog.ApplyOrder over a synthetic pack of the declaration files.
func workspaceApplyOrder(targets map[string]syncTarget) ([]string, error) {
	files := make([]catalog.File, 0, len(targets))
	for rel, t := range targets {
		files = append(files, catalog.File{Path: rel, Data: t.data})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) == 0 {
		return nil, nil
	}
	order, err := catalog.ApplyOrder(catalog.Pack{IndexEntry: catalog.IndexEntry{Name: "workspace"}, Files: files})
	if err != nil {
		return nil, fmt.Errorf("workspace apply: %w", err)
	}
	return order, nil
}

// forgetHead drops a destroyed declaration's recorded head, best-effort: the
// destroy already succeeded, so a cleanup failure logs rather than fails it.
func (o *controlOrchestrator) forgetHead(ctx context.Context, resolved string) {
	if o.submit == nil {
		return
	}
	rel, err := filepath.Rel(o.workspace, resolved)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if err := o.submit.Submit(ctx, func(w *store.Writer) error {
		return w.DeleteDeclarationHead(ctx, rel)
	}); err != nil {
		o.logger.Warn("declare destroy: could not drop declaration head", "path", rel, "err", err)
	}
}

// recordHead persists a declaration file's applied checksum through the single
// meta writer; an unwired submitter (shape-test composition) skips recording.
func (o *controlOrchestrator) recordHead(ctx context.Context, rel, checksum string) error {
	if o.submit == nil {
		return nil
	}
	if err := o.submit.Submit(ctx, func(w *store.Writer) error {
		return w.RecordDeclarationHead(ctx, rel, checksum)
	}); err != nil {
		return fmt.Errorf("workspace apply: record head %s: %w", rel, err)
	}
	return nil
}
