package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the daemon's leader-side catalog plane (#217): POST /catalog/install
// resolves a pack (embedded only at this stage), preflights it against the registry
// and the workspace schemas, materializes it into the leader's own workspace, and
// optionally runs the declare sequence through the control orchestrator.

// catalogPlane is the daemon's api.CatalogHandler: a stable handle delegating to the live orchestrator while leading.
type catalogPlane struct {
	mu   sync.RWMutex
	live *catalogOrchestrator
}

// compile-time proof the plane is the mux's catalog handler.
var _ api.CatalogHandler = (*catalogPlane)(nil)

// newCatalogPlane returns an unwired catalog plane: installs fault until a leader wires an orchestrator.
func newCatalogPlane() *catalogPlane { return &catalogPlane{} }

// install wires the live orchestrator (on winning leadership).
func (p *catalogPlane) install(o *catalogOrchestrator) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = o
}

// clear removes the orchestrator (on demotion) so a racing request faults rather than writing off-path.
func (p *catalogPlane) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = nil
}

// orchestrator returns the installed orchestrator, or nil when not leading.
func (p *catalogPlane) orchestrator() *catalogOrchestrator {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.live
}

// InstallPack routes to the live orchestrator, or faults when none is installed.
func (p *catalogPlane) InstallPack(ctx context.Context, req api.CatalogInstallRequest) (api.CatalogInstallResult, error) {
	o := p.orchestrator()
	if o == nil {
		return api.CatalogInstallResult{}, api.ErrCatalogUnavailable
	}
	return o.installPack(ctx, req)
}

// applyFunc is the control-orchestrator apply seam the catalog rides (injectable for tests).
type applyFunc func(ctx context.Context, req api.ControlRequest) (api.ControlResult, error)

// catalogOrchestrator materializes resolved packs into the leader's workspace and optionally applies them.
type catalogOrchestrator struct {
	workspace string
	registry  store.RegistryReader
	apply     applyFunc
	logger    *slog.Logger
}

// newCatalogOrchestrator builds the leader's catalog orchestrator over its workspace, the registry reader, and the apply seam.
func newCatalogOrchestrator(workspace string, registry store.RegistryReader, apply applyFunc, logger *slog.Logger) *catalogOrchestrator {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &catalogOrchestrator{workspace: workspace, registry: registry, apply: apply, logger: logger}
}

// installPack resolves the pack, preflights, materializes, and (with req.Apply) runs the declare sequence in the derived order.
func (o *catalogOrchestrator) installPack(ctx context.Context, req api.CatalogInstallRequest) (api.CatalogInstallResult, error) {
	p, ok, err := catalog.EmbeddedPack(req.Pack)
	if err != nil {
		return api.CatalogInstallResult{}, fmt.Errorf("catalog install: %w", err)
	}
	if !ok {
		return api.CatalogInstallResult{}, fmt.Errorf("catalog install: no such pack %q (run iris catalog list)", req.Pack)
	}
	order, err := catalog.ApplyOrder(p)
	if err != nil {
		return api.CatalogInstallResult{}, err
	}
	var registered []string
	if o.registry != nil {
		if registered, err = o.registry.RegisteredPipelines(ctx); err != nil {
			return api.CatalogInstallResult{}, fmt.Errorf("catalog install: read registry: %w", err)
		}
	}
	if err := catalog.PreflightRegistry(p, registered, req.Force); err != nil {
		return api.CatalogInstallResult{}, err
	}
	if err := catalog.PreflightSchemas(o.workspace, p); err != nil {
		return api.CatalogInstallResult{}, err
	}
	files, err := catalog.Materialize(o.workspace, p, req.Force)
	if err != nil {
		return api.CatalogInstallResult{}, err
	}
	res := api.CatalogInstallResult{Pack: p.Name, Files: files, ApplyOrder: order}
	if !req.Apply {
		o.logger.Info("catalog install: pack materialized", "pack", p.Name, "files", len(files))
		return res, nil
	}
	if o.apply == nil {
		return res, errors.New("catalog install: control plane not wired for apply")
	}
	for _, target := range order {
		ar, aerr := o.apply(ctx, api.ControlRequest{Path: target})
		if aerr != nil {
			return res, fmt.Errorf("catalog install: materialized %d file(s) but apply failed at %s: %w", len(files), target, aerr)
		}
		res.Warnings = append(res.Warnings, ar.Warnings...)
	}
	res.Applied = true
	o.logger.Info("catalog install: pack applied", "pack", p.Name, "files", len(files), "declarations", len(order))
	return res, nil
}
