package daemon

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// This file is the GET /catalog read plane (#219, #220): the pack listing the ps
// overlay renders, served on any role from the configured remote catalogs, badged
// installed when every pack pipeline is currently registered. An unreachable
// catalog degrades to a warning beside the partial listing, mirroring the ps
// offline banner pattern. Non-shadowed packs are resolved (fetch + sha verify)
// so previews and installed badges work the same for every source.

// catalogReadPlane is the daemon's api.CatalogListHandler.
type catalogReadPlane struct {
	registry store.RegistryReader
	resolver catalog.Resolver
	logger   *slog.Logger
}

// compile-time proof the plane is the mux's catalog listing reader.
var _ api.CatalogListHandler = (*catalogReadPlane)(nil)

// NewCatalogReadPlane builds the pack-listing reader; a nil registry skips the installed badges.
func NewCatalogReadPlane(registry store.RegistryReader, resolver catalog.Resolver, logger *slog.Logger) api.CatalogListHandler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &catalogReadPlane{registry: registry, resolver: resolver, logger: logger}
}

// ListPacks answers every visible pack with badges and preview material.
func (p *catalogReadPlane) ListPacks(ctx context.Context) (api.CatalogListResult, error) {
	listings, lerr := p.resolver.List(ctx)
	registered := map[string]bool{}
	if p.registry != nil {
		names, rerr := p.registry.RegisteredPipelines(ctx)
		if rerr != nil {
			p.logger.Warn("catalog list: registry read failed; installed badges skipped", "err", rerr)
		}
		for _, n := range names {
			registered[n] = true
		}
	}
	res := api.CatalogListResult{Packs: make([]api.CatalogPack, 0, len(listings))}
	var enrichWarns []string
	for _, l := range listings {
		entry, w := p.describeListing(ctx, l, registered)
		res.Packs = append(res.Packs, entry)
		if w != "" {
			enrichWarns = append(enrichWarns, w)
		}
	}
	if lerr != nil {
		res.Warnings = strings.Split(lerr.Error(), "\n")
	}
	res.Warnings = append(res.Warnings, enrichWarns...)
	return res, nil
}

// describeListing renders one listing entry. Shadowed rows stay index-only;
// winners are resolved for full preview and installed badges. A resolve failure
// keeps the index facts and returns a soft warning (never fails the list).
func (p *catalogReadPlane) describeListing(ctx context.Context, l catalog.Listing, registered map[string]bool) (api.CatalogPack, string) {
	entry := api.CatalogPack{
		Name: l.Name, Description: l.Description, Tags: l.Tags,
		Requires: l.Requires, SHA256: l.SHA256, Source: l.Source, Shadowed: l.Shadowed,
	}
	if l.Shadowed {
		return entry, ""
	}
	pk, ok, err := p.resolver.Resolve(ctx, l.Name)
	if err != nil {
		return entry, "catalog pack " + l.Name + " preview unavailable: " + err.Error()
	}
	if !ok {
		return entry, ""
	}
	entry.Readme = pk.README
	for _, f := range pk.Files {
		entry.Files = append(entry.Files, f.Path)
	}
	if names, nerr := catalog.PipelineNames(pk); nerr == nil {
		entry.Pipelines = names
		entry.Installed = len(names) > 0 && allRegistered(names, registered)
	}
	if order, oerr := catalog.ApplyOrder(pk); oerr == nil {
		entry.ApplyOrder = order
	}
	return entry, ""
}

// allRegistered reports whether every name is currently registered.
func allRegistered(names []string, registered map[string]bool) bool {
	for _, n := range names {
		if !registered[n] {
			return false
		}
	}
	return true
}
