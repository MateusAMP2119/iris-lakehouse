package daemon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// fakeLister is a store.PipelineLister over seeded rows, recording which view
// was read, so the plane's threading is proven with no live Postgres.
type fakeLister struct {
	active, all []store.PipelineListing
	sawAll      bool
}

func (f *fakeLister) ActivePipelines(context.Context) ([]store.PipelineListing, error) {
	return f.active, nil
}

func (f *fakeLister) AllPipelines(context.Context) ([]store.PipelineListing, error) {
	f.sawAll = true
	return f.all, nil
}

// TestPipelinePlaneListing proves the pipeline plane threads every listing
// field -- name, active, lane -- from the store rows into the wire rows, for
// the default and --all views alike, and faults with ErrControlUnavailable
// while no lister is wired.
func TestPipelinePlaneListing(t *testing.T) {
	t.Run("pipeline-plane-listing", func(t *testing.T) {
		active := []store.PipelineListing{{Name: "extract", Active: true, Lane: "ingest"}}
		all := []store.PipelineListing{
			{Name: "extract", Active: true, Lane: "ingest"},
			{Name: "sweep", Active: false},
		}

		t.Run("threads name, active, and lane for both views", func(t *testing.T) {
			lister := &fakeLister{active: active, all: all}
			plane := newPipelinePlane(lister, nil)

			got, err := plane.ListPipelines(context.Background(), false)
			if err != nil {
				t.Fatalf("ListPipelines(all=false): %v", err)
			}
			want := []api.PipelineListItem{{Name: "extract", Active: true, Lane: "ingest"}}
			if !reflect.DeepEqual(got.Pipelines, want) {
				t.Errorf("default listing = %+v, want %+v", got.Pipelines, want)
			}
			if lister.sawAll {
				t.Error("default view read AllPipelines, want ActivePipelines")
			}

			got, err = plane.ListPipelines(context.Background(), true)
			if err != nil {
				t.Fatalf("ListPipelines(all=true): %v", err)
			}
			want = append(want, api.PipelineListItem{Name: "sweep", Active: false, Lane: ""})
			if !reflect.DeepEqual(got.Pipelines, want) {
				t.Errorf("all listing = %+v, want %+v", got.Pipelines, want)
			}
			if !lister.sawAll {
				t.Error("all view never read AllPipelines")
			}
		})

		t.Run("no wired lister is an internal fault", func(t *testing.T) {
			plane := newPipelinePlane(nil, nil)
			if _, err := plane.ListPipelines(context.Background(), false); !errors.Is(err, api.ErrControlUnavailable) {
				t.Fatalf("nil-lister ListPipelines error = %v, want ErrControlUnavailable", err)
			}
		})
	})
}
