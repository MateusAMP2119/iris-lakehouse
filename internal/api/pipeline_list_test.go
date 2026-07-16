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

// fixedPipelines is a PipelineHandler serving a canned listing, recording the
// all flag it was asked for. The run mutation is never driven here.
type fixedPipelines struct {
	result  api.PipelineListResult
	lastAll *bool
}

func (f *fixedPipelines) RunPipeline(context.Context, api.PipelineRunRequest) (api.PipelineRunResult, error) {
	return api.PipelineRunResult{}, api.ErrControlUnavailable
}

func (f *fixedPipelines) ListPipelines(_ context.Context, all bool) (api.PipelineListResult, error) {
	if f.lastAll != nil {
		*f.lastAll = all
	}
	return f.result, nil
}

// TestPipelineListLane proves GET /pipeline/list carries each row's composer
// lane on the wire -- present for a composed pipeline, absent (omitempty) for
// one that runs as its own lane -- and that ?all=1 reaches the handler.
func TestPipelineListLane(t *testing.T) {
	t.Run("pipeline-list-lane", func(t *testing.T) {
		result := api.PipelineListResult{Pipelines: []api.PipelineListItem{
			{Name: "extract_orders", Active: true, Lane: "ingest"},
			{Name: "sweep", Active: false},
		}}

		t.Run("the envelope carries the lane, omitted when unassigned", func(t *testing.T) {
			var all bool
			h := leaderMux(api.WithPipelines(&fixedPipelines{result: result, lastAll: &all}))
			req := httptest.NewRequest(http.MethodGet, "/pipeline/list", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			resp := rec.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /pipeline/list = %d, want 200", resp.StatusCode)
			}

			raw := rec.Body.String()
			if !strings.Contains(raw, `"lane":"ingest"`) {
				t.Errorf("composed pipeline row carries no lane on the wire:\n%s", raw)
			}
			if strings.Count(raw, `"lane"`) != 1 {
				t.Errorf("unassigned pipeline row must omit the lane key entirely:\n%s", raw)
			}

			var env struct {
				Data api.PipelineListResult `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if len(env.Data.Pipelines) != 2 || env.Data.Pipelines[0].Lane != "ingest" || env.Data.Pipelines[1].Lane != "" {
				t.Errorf("listing did not round-trip lanes: %+v", env.Data.Pipelines)
			}
			if all {
				t.Error("bare GET /pipeline/list asked the handler for all=true, want false")
			}
		})

		t.Run("?all=1 reaches the handler", func(t *testing.T) {
			var all bool
			h := leaderMux(api.WithPipelines(&fixedPipelines{result: result, lastAll: &all}))
			req := httptest.NewRequest(http.MethodGet, "/pipeline/list?all=1", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Result().StatusCode != http.StatusOK {
				t.Fatalf("GET /pipeline/list?all=1 = %d, want 200", rec.Result().StatusCode)
			}
			if !all {
				t.Error("GET /pipeline/list?all=1 asked the handler for all=false, want true")
			}
		})
	})
}
