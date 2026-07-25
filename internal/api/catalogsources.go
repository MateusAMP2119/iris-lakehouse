package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// This file is the catalog source surface: POST /catalog/sources, the route the
// `iris ps` catalog surfaces add index URLs through. The daemon validates and
// probes the URL, persists the grown list to iris.toml, and serves it live from
// then on. As a mutation it rides the mux's leader gate, and its PAT scope is
// control like the rest of /catalog/* (see requiredScope).

// CatalogSourceRequest is the body of a POST /catalog/sources: one index URL to add.
type CatalogSourceRequest struct {
	// URL locates the catalog index document (catalog.json) to add.
	URL string `json:"url"`
}

// CatalogSourceResult is the POST /catalog/sources payload: the updated source list.
type CatalogSourceResult struct {
	// Sources is the full ordered catalog index URL list after the add.
	Sources []string `json:"sources"`
}

// CatalogSourcesHandler grows the daemon's configured catalog source list.
type CatalogSourcesHandler interface {
	// AddSource validates, persists, and activates one catalog index URL.
	AddSource(ctx context.Context, req CatalogSourceRequest) (CatalogSourceResult, error)
}

// ErrCatalogSourcesUnavailable is the unwired-handler fault for POST /catalog/sources.
var ErrCatalogSourcesUnavailable = errors.New("api: catalog sources not available")

// WithCatalogSources wires the source-list handler POST /catalog/sources routes to.
func WithCatalogSources(h CatalogSourcesHandler) MuxOption {
	return func(m *mux) {
		if h != nil {
			m.catalogSources = h
		}
	}
}

// noCatalogSources faults every add until a real handler is wired.
type noCatalogSources struct{}

func (noCatalogSources) AddSource(context.Context, CatalogSourceRequest) (CatalogSourceResult, error) {
	return CatalogSourceResult{}, ErrCatalogSourcesUnavailable
}

// serveCatalogSources handles POST /catalog/sources: decode, add, render the envelope.
func (m *mux) serveCatalogSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST "+r.URL.Path+" only")
		return
	}
	var req CatalogSourceRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "malformed catalog source request body: "+err.Error())
		return
	}
	if req.URL == "" {
		WriteError(w, http.StatusUnprocessableEntity, CodeOpFailed, "catalog source add requires a url")
		return
	}
	res, err := m.catalogSources.AddSource(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrCatalogSourcesUnavailable) {
			WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		WriteError(w, http.StatusUnprocessableEntity, CodeOpFailed, err.Error())
		return
	}
	WriteData(w, http.StatusOK, res)
}
