package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// regNamesFake is a RegistryReader answering a fixed pipeline roster.
type regNamesFake struct{ names []string }

func (f *regNamesFake) RegisteredPipelines(context.Context) ([]string, error) { return f.names, nil }
func (f *regNamesFake) DependencyEdges(context.Context) ([]store.DependencyEdge, error) {
	return nil, nil
}
func (f *regNamesFake) LaneMembers(context.Context, string) ([]string, error) { return nil, nil }

// TestCatalogReadPlane proves GET /catalog's composition: embedded previews, installed
// badges from the registry, remote entries with shadowing, and degrade-to-warning on a
// dead catalog source.
func TestCatalogReadPlane(t *testing.T) {
	ctx := context.Background()

	t.Run("embedded packs carry full previews and installed badges", func(t *testing.T) {
		reg := &regNamesFake{names: []string{"quake_feed", "quake_report"}}
		p := NewCatalogReadPlane(reg, catalog.Resolver{}, nil)
		res, err := p.ListPacks(ctx)
		if err != nil {
			t.Fatalf("ListPacks: %v", err)
		}
		if len(res.Packs) != 2 || len(res.Warnings) != 0 {
			t.Fatalf("listing = %+v, want two embedded packs and no warnings", res)
		}
		byName := map[string]bool{}
		for _, pk := range res.Packs {
			byName[pk.Name] = pk.Installed
			if pk.Source != catalog.SourceEmbedded || pk.Readme == "" || len(pk.Files) == 0 {
				t.Errorf("pack %q = %+v, want an embedded full preview", pk.Name, pk)
			}
		}
		if !byName["quake-monitor"] {
			t.Error("quake-monitor must badge installed: every pipeline is registered")
		}
		if byName["dlq-demo"] {
			t.Error("dlq-demo must not badge installed: boom and aftershock are unregistered")
		}
	})

	t.Run("remote entries list with shadowing, a dead catalog degrades to a warning", func(t *testing.T) {
		index, _ := json.Marshal(catalog.Index{Format: catalog.IndexFormat, Packs: []catalog.IndexEntry{
			{Name: "quake-monitor", Path: "p.tar.gz", SHA256: "aa"},
			{Name: "extra", Path: "e.tar.gz", SHA256: "bb", Requires: "v0.5.5"},
		}})
		live := catalog.Remote{URL: "https://cat.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) { return index, nil }}
		dead := catalog.Remote{URL: "https://dead.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) { return nil, errors.New("dial timeout") }}
		p := NewCatalogReadPlane(nil, catalog.Resolver{Catalogs: []catalog.Remote{live, dead}}, nil)
		res, err := p.ListPacks(ctx)
		if err != nil {
			t.Fatalf("ListPacks: %v", err)
		}
		if len(res.Packs) != 4 {
			t.Fatalf("listing = %d packs, want embedded 2 + remote 2", len(res.Packs))
		}
		var shadowed, extra bool
		for _, pk := range res.Packs {
			if pk.Name == "quake-monitor" && pk.Source != catalog.SourceEmbedded {
				shadowed = pk.Shadowed
			}
			if pk.Name == "extra" {
				extra = pk.Requires == "v0.5.5" && !pk.Shadowed
			}
		}
		if !shadowed || !extra {
			t.Errorf("listing = %+v, want the remote duplicate shadowed and extra visible", res.Packs)
		}
		if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "dead.example") {
			t.Errorf("warnings = %v, want the dead catalog named", res.Warnings)
		}
	})
}
