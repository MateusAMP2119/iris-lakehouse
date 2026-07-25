package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

// This file is the workspace-sync route: POST /workspace/apply diffs the
// leader's whole workspace tree against the last-applied declaration heads and
// applies what changed, in dependency order — the "edit files, run one
// command" flow beside the single-file /apply.

// WorkspaceApplyRequest is the body for POST /workspace/apply.
type WorkspaceApplyRequest struct {
	// DryRun previews the plan without writing anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// The workspace-sync change actions.
const (
	// ChangeRegistered marks a declaration applied for the first time.
	ChangeRegistered = "registered"
	// ChangeUpdated marks a declaration re-applied because its file changed.
	ChangeUpdated = "updated"
	// ChangeUnchanged marks a declaration whose file matches its recorded head.
	ChangeUnchanged = "unchanged"
	// ChangeMissing marks a recorded head whose file is gone from the workspace
	// (reported, never destroyed; `iris declare destroy` owns teardown).
	ChangeMissing = "missing"
)

// WorkspaceChange is one declaration's place in the sync plan.
type WorkspaceChange struct {
	// Action is one of the Change* actions.
	Action string `json:"action"`
	// Kind is the declaration kind: pipeline or composer.
	Kind string `json:"kind,omitempty"`
	// Target is the declared name: the pipeline name or the composer's lane.
	Target string `json:"target"`
	// Path is the declaration file's workspace-relative path.
	Path string `json:"path"`
	// Detail is an optional one-line elaboration for rendering.
	Detail string `json:"detail,omitempty"`
}

// WorkspaceApplyResult is the sync outcome: the full plan (every declaration,
// changed or not) and how many applies ran.
type WorkspaceApplyResult struct {
	// DryRun echoes the request's preview flag.
	DryRun bool `json:"dry_run,omitempty"`
	// Changes is the plan, in apply order; missing heads ride at the end.
	Changes []WorkspaceChange `json:"changes"`
	// Applied counts the declarations actually applied (0 on a dry run).
	Applied int `json:"applied"`
	// Warnings carries the per-apply advisories, prefixed by target.
	Warnings []string `json:"warnings,omitempty"`
}

// serveWorkspaceApply handles POST /workspace/apply: decode, run the leader's
// workspace sync, render the data envelope. Error mapping mirrors the other
// control routes.
func (m *mux) serveWorkspaceApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST "+r.URL.Path+" only")
		return
	}
	var req WorkspaceApplyRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, CodeBadRequest, "malformed workspace apply request body: "+err.Error())
		return
	}
	res, err := m.control.ApplyWorkspace(r.Context(), req)
	if err != nil {
		if errors.Is(err, ErrControlUnavailable) {
			WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		WriteError(w, http.StatusUnprocessableEntity, CodeOpFailed, err.Error())
		return
	}
	WriteData(w, http.StatusOK, res)
}
