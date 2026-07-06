// Package api is the one net/http handler the Iris daemon's two listeners serve:
// the control plane and the read API on a single mux (specification sections 2
// and 7). It is deliberately a leaf: it renders exactly the resource-shaped
// HTTP/JSON views the CLI's read commands print and never reaches back up into
// the daemon, dispatcher, or a database.
//
// The route roster is filled by the read-API epics (E09/E12/E14); today the mux
// carries GET /healthz -- the liveness probe both the CLI's daemon-reachability
// check and the conformance harness hit -- plus the closed error envelope for
// unknown routes and non-GET methods, so the mux is built for extension.
//
// Authorization differs by listener, not by route: a unix-socket request is
// ambient (local, filesystem-guarded), while a TCP request must present a PAT.
// That split lives in RequirePAT (auth.go), which the daemon wraps around this
// mux for the TCP listener only.
package api

import (
	"encoding/json"
	"net/http"
)

// Envelope is the read-API success document of specification section 7:
// {"data": ...}. Every non-streaming success response is one Envelope on the
// wire.
type Envelope struct {
	// Data is the response payload, shaped per route.
	Data any `json:"data"`
}

// ErrorEnvelope is the read-API error document of specification section 7:
// {"error": {"code": ..., "message": ...}} with a code drawn from the closed set
// (bad_param, unauthorized, forbidden, not_found, method_not_allowed, internal).
type ErrorEnvelope struct {
	// Error is the error object.
	Error ErrorBody `json:"error"`
}

// ErrorBody is the error object inside an ErrorEnvelope.
type ErrorBody struct {
	// Code is a closed-set machine code.
	Code string `json:"code"`
	// Message is the human-readable detail.
	Message string `json:"message"`
}

// Health is the payload of GET /healthz: the daemon's liveness and its leadership
// role. Role is "unknown" until leader election lands (E02.6), when GET /healthz
// and GET /leader report leader/standby on both listeners.
type Health struct {
	// Status is "ok" when the daemon is serving.
	Status string `json:"status"`
	// Role is the leadership role: leader, standby, or unknown (until E02.6).
	Role string `json:"role"`
}

// NewMux returns the single http.Handler both daemon listeners serve. It owns the
// route roster; unknown routes and non-GET methods get the closed error envelope,
// never the standard library's plain-text default.
func NewMux() http.Handler {
	return &mux{}
}

// mux is the daemon's route table. It is a hand-rolled matcher (rather than
// http.ServeMux) so unknown routes and disallowed methods return the read-API
// error envelope, not net/http's plain-text 404/405.
type mux struct{}

// ServeHTTP dispatches a request to its route, or returns the closed-code error
// envelope for an unknown route or a disallowed method.
func (m *mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET /healthz only")
			return
		}
		WriteData(w, http.StatusOK, Health{Status: "ok", Role: "unknown"})
	default:
		WriteError(w, http.StatusNotFound, "not_found", "no such route: "+r.URL.Path)
	}
}

// WriteData writes a success envelope wrapping v at the given status. It is the
// one place a data response is serialized, so every route emits the identical
// {"data": ...} shape.
func WriteData(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, Envelope{Data: v})
}

// WriteError writes an error envelope with the given closed-set code and detail
// at the given status. It is the one place an error response is serialized, so
// every failure emits the identical {"error": {...}} shape.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorEnvelope{Error: ErrorBody{Code: code, Message: message}})
}

// writeJSON sets the JSON content type, writes the status, and encodes v. A
// late encode error cannot change the already-sent status, so it is dropped
// rather than wrapped.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
