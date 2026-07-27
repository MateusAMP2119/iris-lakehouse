package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"sync"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// This file is the daemon's live catalog source set: the iris.toml catalogs
// list held behind a lock, snapshotted into a catalog.Resolver per request so
// the read and install planes see additions immediately. POST /catalog/sources
// grows it: the URL is validated, its index probed (a source that does not
// serve a parseable catalog.json is refused), the grown list persisted back to
// iris.toml, and only then activated.

// catalogSources is the daemon's api.CatalogSourcesHandler and the live
// resolver source for the catalog planes.
type catalogSources struct {
	mu      sync.RWMutex
	remotes []catalog.Remote
	// persist rewrites the configured catalogs list durably; nil skips
	// persistence (tests, ephemeral daemons) and the add is process-lifetime.
	persist func(urls []string) error
	// probe verifies a candidate serves a parseable index; injectable in tests.
	probe  func(ctx context.Context, r catalog.Remote) error
	logger *slog.Logger
}

// compile-time proof the store is the mux's source handler.
var _ api.CatalogSourcesHandler = (*catalogSources)(nil)

// newCatalogSources builds the live source set over the configured URLs.
func newCatalogSources(urls []string, persist func([]string) error, logger *slog.Logger) *catalogSources {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	remotes := make([]catalog.Remote, 0, len(urls))
	for _, u := range urls {
		remotes = append(remotes, catalog.Remote{URL: u})
	}
	return &catalogSources{
		remotes: remotes,
		persist: persist,
		probe: func(ctx context.Context, r catalog.Remote) error {
			_, err := r.Index(ctx)
			return err
		},
		logger: logger,
	}
}

// resolver snapshots the live source list into a resolver value; planes call
// this per request so a fresh source resolves without a restart.
func (s *catalogSources) resolver() catalog.Resolver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return catalog.Resolver{Catalogs: append([]catalog.Remote(nil), s.remotes...)}
}

// urls lists the configured index URLs in order.
func (s *catalogSources) urls() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.remotes))
	for i, r := range s.remotes {
		out[i] = r.URL
	}
	return out
}

// AddSource validates, probes, persists, and activates one catalog index URL.
func (s *catalogSources) AddSource(ctx context.Context, req api.CatalogSourceRequest) (api.CatalogSourceResult, error) {
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return api.CatalogSourceResult{}, fmt.Errorf("catalog source %q is not an http(s) index URL", req.URL)
	}
	for _, have := range s.urls() {
		if have == req.URL {
			return api.CatalogSourceResult{}, fmt.Errorf("catalog source %s is already configured", req.URL)
		}
	}
	if err := s.probe(ctx, catalog.Remote{URL: req.URL}); err != nil {
		return api.CatalogSourceResult{}, fmt.Errorf("catalog source refused: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grown := make([]string, 0, len(s.remotes)+1)
	for _, r := range s.remotes {
		grown = append(grown, r.URL)
	}
	grown = append(grown, req.URL)
	if s.persist != nil {
		if err := s.persist(grown); err != nil {
			return api.CatalogSourceResult{}, fmt.Errorf("catalog source not saved: %w", err)
		}
	}
	s.remotes = append(s.remotes, catalog.Remote{URL: req.URL})
	s.logger.Info("catalog source added", "url", req.URL, "sources", len(grown))
	return api.CatalogSourceResult{Sources: grown}, nil
}
