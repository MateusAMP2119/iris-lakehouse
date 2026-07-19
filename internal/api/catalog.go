package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// This file is the control-plane catalog surface: POST /catalog/install, the route
// `iris catalog install` drives. The client names a pack, never a URL (no
// client-directed egress); the leader resolves it against its configured catalogs,
// materializes into its own workspace, and answers the derived apply order.

// CatalogInstallRequest is the body of a POST /catalog/install: a pack name plus flags.
type CatalogInstallRequest struct {
	// Pack is the pack name the leader resolves against its catalogs.
	Pack string `json:"pack"`
	// Apply runs the declare sequence in the derived order after materializing.
	Apply bool `json:"apply,omitempty"`
	// Force overwrites existing workspace paths instead of refusing.
	Force bool `json:"force,omitempty"`
}

// CatalogInstallResult is the success payload: what landed and the order to apply it in.
type CatalogInstallResult struct {
	// Pack is the pack installed.
	Pack string `json:"pack"`
	// Files are the workspace-relative paths materialized, sorted.
	Files []string `json:"files"`
	// ApplyOrder is the derived declare sequence (first member, composer, rest).
	ApplyOrder []string `json:"apply_order"`
	// Applied reports whether the declare sequence ran.
	Applied bool `json:"applied,omitempty"`
	// Warnings are advisory messages from the applies.
	Warnings []string `json:"warnings,omitempty"`
}

// CatalogHandler runs the leader-side pack install; the daemon wires it over the pack resolver and the control orchestrator.
type CatalogHandler interface {
	// InstallPack resolves, preflights, materializes, and optionally applies the named pack.
	InstallPack(ctx context.Context, req CatalogInstallRequest) (CatalogInstallResult, error)
}

// ErrCatalogUnavailable is the unwired-handler fault; the leader installs the real handler before reporting the leader role.
var ErrCatalogUnavailable = errors.New("api: catalog plane not available")

// WithCatalog wires the leader-side catalog handler the mux routes POST /catalog/install to.
func WithCatalog(h CatalogHandler) MuxOption {
	return func(m *mux) {
		if h != nil {
			m.catalog = h
		}
	}
}

// noCatalog faults every install until a real handler is wired, never a silent success.
type noCatalog struct{}

func (noCatalog) InstallPack(context.Context, CatalogInstallRequest) (CatalogInstallResult, error) {
	return CatalogInstallResult{}, ErrCatalogUnavailable
}

// serveCatalogInstall handles POST /catalog/install: decode, run the leader install, render the envelope.
func (m *mux) serveCatalogInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST "+r.URL.Path+" only")
		return
	}
	var req CatalogInstallRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "malformed catalog install request body: "+err.Error())
		return
	}
	if req.Pack == "" {
		WriteError(w, http.StatusUnprocessableEntity, CodeOpFailed, "catalog install requires a pack name")
		return
	}
	res, err := m.catalog.InstallPack(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrCatalogUnavailable) {
			WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		WriteError(w, http.StatusUnprocessableEntity, CodeOpFailed, err.Error())
		return
	}
	WriteData(w, http.StatusOK, res)
}
