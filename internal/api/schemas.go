package api

import (
	"context"
	"errors"
	"net/http"
)

// This file is the declared-shape read surface: GET /schemas, the workspace's
// declared tables and the pipelines that write them. It is declaration truth,
// not a live-database read -- the same schemas/ tree provisioning materializes
// -- so a table declared but never written still describes itself.
//
// It is its own route rather than a block on /pipeline/show because the whole
// pipeline segment requires control scope (see requiredScope); a read-PAT
// session watching the dashboard must not need control to name its columns.
// Shapes are workspace-static between declare applies, so the document covers
// the workspace once instead of being refetched per selection.

// ColumnShape is one declared column: the type token the operator wrote, the
// Postgres type it resolves to, and the declared constraints.
type ColumnShape struct {
	// Name is the column name.
	Name string `json:"name"`
	// Type is the DECLARED token from the closed set (text, bigint, double,
	// timestamptz, varchar(n), numeric(p,s)) -- the vocabulary the operator
	// wrote, never one invented for display.
	Type string `json:"type"`
	// PgType is the Postgres type the token resolves to (int -> integer,
	// double -> double precision, bool -> boolean).
	PgType string `json:"pg_type"`
	// PrimaryKey marks a primary-key column.
	PrimaryKey bool `json:"primary_key,omitempty"`
	// Nullable is the effective nullability (false under a primary key).
	Nullable bool `json:"nullable"`
	// Unique marks a column declared unique.
	Unique bool `json:"unique,omitempty"`
	// Default is the raw SQL default expression, verbatim; empty when none.
	Default string `json:"default,omitempty"`
}

// TableShape is one declared table: its columns in declaration order and its
// primary key.
type TableShape struct {
	// Schema is the table's schema name.
	Schema string `json:"schema"`
	// Table is the table name.
	Table string `json:"table"`
	// Columns are the declared columns, in declaration order.
	Columns []ColumnShape `json:"columns"`
	// PrimaryKey is the primary-key column list, in declaration order.
	PrimaryKey []string `json:"primary_key,omitempty"`
}

// PipelineOutput binds one pipeline to one table it declares a write on.
type PipelineOutput struct {
	// Pipeline is the writing pipeline's name.
	Pipeline string `json:"pipeline"`
	// Schema is the written table's schema.
	Schema string `json:"schema"`
	// Table is the written table's name.
	Table string `json:"table"`
}

// SchemaListResult is the GET /schemas document.
type SchemaListResult struct {
	// Tables are the declared tables, ordered schema then table.
	Tables []TableShape `json:"tables"`
	// Outputs bind pipelines to the tables they hold a write grant on --
	// declaration truth, so a pipeline that has never run still names its
	// output. Ordered pipeline then schema then table.
	Outputs []PipelineOutput `json:"outputs,omitempty"`
}

// SchemaListHandler serves the declared-shape listing; the daemon wires it
// over the workspace tree and the grant reader.
type SchemaListHandler interface {
	// ListSchemas returns the workspace's declared tables and write bindings.
	ListSchemas(ctx context.Context) (SchemaListResult, error)
}

// ErrSchemasUnavailable is the unwired-reader fault for GET /schemas.
var ErrSchemasUnavailable = errors.New("api: schema listing not available")

// WithSchemas wires the declared-shape reader GET /schemas serves from.
func WithSchemas(h SchemaListHandler) MuxOption {
	return func(m *mux) {
		if h != nil {
			m.schemas = h
		}
	}
}

// noSchemas faults the listing until a real reader is wired.
type noSchemas struct{}

func (noSchemas) ListSchemas(context.Context) (SchemaListResult, error) {
	return SchemaListResult{}, ErrSchemasUnavailable
}

// serveSchemas handles GET /schemas: render the declared shapes in the data
// envelope.
func (m *mux) serveSchemas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET "+r.URL.Path+" only")
		return
	}
	if !noParams(w, r) {
		return
	}
	res, err := m.schemas.ListSchemas(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	WriteData(w, http.StatusOK, res)
}
