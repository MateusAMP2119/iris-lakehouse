package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// fixedSchemas is a SchemaListHandler answering a canned document.
type fixedSchemas struct {
	res api.SchemaListResult
	err error
}

func (f fixedSchemas) ListSchemas(context.Context) (api.SchemaListResult, error) {
	return f.res, f.err
}

// getSchemas drives one request against /schemas.
func getSchemas(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// TestSchemasRoute proves the GET /schemas contract: the data envelope on
// success, method-not-allowed on a write verb, a rejected unknown parameter,
// and the internal-fault envelope while the seam is unwired.
func TestSchemasRoute(t *testing.T) {
	t.Run("schemas-route", func(t *testing.T) {
		doc := api.SchemaListResult{
			Tables: []api.TableShape{{
				Schema: "demo", Table: "orders", PrimaryKey: []string{"id"},
				Columns: []api.ColumnShape{
					{Name: "id", Type: "bigint", PgType: "bigint", PrimaryKey: true},
					{Name: "placed_at", Type: "timestamptz", PgType: "timestamp with time zone", Nullable: true},
				},
			}},
			Outputs: []api.PipelineOutput{{Pipeline: "load_orders", Schema: "demo", Table: "orders"}},
		}

		t.Run("success renders the data envelope carrying both type vocabularies", func(t *testing.T) {
			rec := getSchemas(leaderMux(api.WithSchemas(fixedSchemas{res: doc})), http.MethodGet, "/schemas")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
			}
			var env struct {
				Data api.SchemaListResult `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if len(env.Data.Tables) != 1 || len(env.Data.Tables[0].Columns) != 2 {
				t.Fatalf("document = %+v, want the one seeded table", env.Data)
			}
			col := env.Data.Tables[0].Columns[1]
			if col.Type != "timestamptz" || col.PgType != "timestamp with time zone" {
				t.Errorf("column = %+v, want the declared token AND its resolved pg type", col)
			}
			if len(env.Data.Outputs) != 1 || env.Data.Outputs[0].Pipeline != "load_orders" {
				t.Errorf("outputs = %+v, want the seeded write binding", env.Data.Outputs)
			}
		})

		t.Run("a write verb is method not allowed", func(t *testing.T) {
			rec := getSchemas(leaderMux(api.WithSchemas(fixedSchemas{res: doc})), http.MethodPost, "/schemas")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
		})

		t.Run("an unknown parameter is refused", func(t *testing.T) {
			rec := getSchemas(leaderMux(api.WithSchemas(fixedSchemas{res: doc})), http.MethodGet, "/schemas?table=orders")
			if rec.Code == http.StatusOK {
				t.Errorf("status = %d, want a refusal for an unknown parameter", rec.Code)
			}
		})

		t.Run("an unwired seam answers the internal fault", func(t *testing.T) {
			rec := getSchemas(leaderMux(), http.MethodGet, "/schemas")
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 while unwired", rec.Code)
			}
		})

		t.Run("a handler error propagates as the internal fault", func(t *testing.T) {
			boom := errors.New("schemas tree unreadable")
			rec := getSchemas(leaderMux(api.WithSchemas(fixedSchemas{err: boom})), http.MethodGet, "/schemas")
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
		})
	})
}
