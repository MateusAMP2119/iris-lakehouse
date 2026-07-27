package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
)

// fixedCatalogSources is a CatalogSourcesHandler answering a canned result, recording the request.
type fixedCatalogSources struct {
	res  api.CatalogSourceResult
	err  error
	last *api.CatalogSourceRequest
}

func (f *fixedCatalogSources) AddSource(_ context.Context, req api.CatalogSourceRequest) (api.CatalogSourceResult, error) {
	if f.last != nil {
		*f.last = req
	}
	return f.res, f.err
}

// postCatalogSources drives POST /catalog/sources with the given body.
func postCatalogSources(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/catalog/sources", strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec
}

// TestCatalogSourcesRoute proves the /catalog/sources contract: envelope on
// success, 422 on an empty URL or a refused add, 400 malformed, 500 unwired,
// not_leader on a standby.
func TestCatalogSourcesRoute(t *testing.T) {
	t.Run("success renders the updated source list in the envelope", func(t *testing.T) {
		var got api.CatalogSourceRequest
		h := &fixedCatalogSources{res: api.CatalogSourceResult{Sources: []string{"https://a/catalog.json", "https://b/catalog.json"}}, last: &got}
		rec := postCatalogSources(leaderMux(api.WithCatalogSources(h)), `{"url":"https://b/catalog.json"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if got.URL != "https://b/catalog.json" {
			t.Errorf("handler saw %+v, want the url forwarded", got)
		}
		var env struct {
			Data api.CatalogSourceResult `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Data.Sources) != 2 {
			t.Errorf("body = %s, want both sources in the data envelope (err %v)", rec.Body.String(), err)
		}
	})

	t.Run("an empty url is operation_failed", func(t *testing.T) {
		rec := postCatalogSources(leaderMux(api.WithCatalogSources(&fixedCatalogSources{})), `{}`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("a malformed body is bad_request", func(t *testing.T) {
		rec := postCatalogSources(leaderMux(api.WithCatalogSources(&fixedCatalogSources{})), `{"url":`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("an unwired handler is an internal fault", func(t *testing.T) {
		rec := postCatalogSources(leaderMux(), `{"url":"https://a/catalog.json"}`)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("a standby answers not_leader", func(t *testing.T) {
		rec := postCatalogSources(api.NewMux(api.WithCatalogSources(&fixedCatalogSources{})), `{"url":"https://a/catalog.json"}`)
		if rec.Code != api.StatusNotLeader {
			t.Errorf("status = %d, want %d", rec.Code, api.StatusNotLeader)
		}
	})
}
