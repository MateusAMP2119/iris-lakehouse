package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// TestCatalogReadPlane proves GET /catalog's composition: remote previews, installed
// badges from the registry, shadowing, and degrade-to-warning on a dead catalog source.
func TestCatalogReadPlane(t *testing.T) {
	ctx := context.Background()
	blob := starterPackTar(t)
	sum := sha256.Sum256(blob)
	digest := hex.EncodeToString(sum[:])
	index, err := json.Marshal(catalog.Index{Format: catalog.IndexFormat, Packs: []catalog.IndexEntry{
		{Name: catalog.StarterPack, Path: "packs/" + catalog.StarterPack + ".tar.gz", SHA256: digest, Description: "starter"},
		{Name: "dlq-demo", Path: "packs/dlq-demo.tar.gz", SHA256: digest, Description: "dlq"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// dlq-demo reuses the same tarball bytes (sha matches) for enrichment tests.
	liveFetch := func(_ context.Context, url string) ([]byte, error) {
		if strings.HasSuffix(url, "catalog.json") {
			return index, nil
		}
		return blob, nil
	}
	live := catalog.Remote{URL: "https://cat.example/catalog.json", Fetch: liveFetch}

	t.Run("remote packs carry full previews and installed badges", func(t *testing.T) {
		reg := &regNamesFake{names: []string{"quake_feed", "quake_report"}}
		p := NewCatalogReadPlane(reg, catalog.Resolver{Catalogs: []catalog.Remote{live}}, nil)
		res, err := p.ListPacks(ctx)
		if err != nil {
			t.Fatalf("ListPacks: %v", err)
		}
		if len(res.Packs) != 2 || len(res.Warnings) != 0 {
			t.Fatalf("listing = %+v, want two remote packs and no warnings", res)
		}
		byName := map[string]bool{}
		for _, pk := range res.Packs {
			byName[pk.Name] = pk.Installed
			if pk.Source != live.URL || pk.Readme == "" || len(pk.Files) == 0 {
				t.Errorf("pack %q = %+v, want a full remote preview", pk.Name, pk)
			}
		}
		if !byName[catalog.StarterPack] {
			t.Error("starter pack must badge installed: every pipeline is registered")
		}
		// dlq-demo reuses starter tarball content, so same pipelines → also installed.
		if !byName["dlq-demo"] {
			t.Error("dlq-demo reuses the same pipelines and should badge installed")
		}
	})

	t.Run("remote entries list with shadowing, a dead catalog degrades to a warning", func(t *testing.T) {
		dupIndex, _ := json.Marshal(catalog.Index{Format: catalog.IndexFormat, Packs: []catalog.IndexEntry{
			{Name: catalog.StarterPack, Path: "p.tar.gz", SHA256: "aa"},
			{Name: "extra", Path: "e.tar.gz", SHA256: "bb", Requires: "v0.5.5"},
		}})
		second := catalog.Remote{URL: "https://other.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) {
			return dupIndex, nil
		}}
		dead := catalog.Remote{URL: "https://dead.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("dial timeout")
		}}
		p := NewCatalogReadPlane(nil, catalog.Resolver{Catalogs: []catalog.Remote{live, second, dead}}, nil)
		res, err := p.ListPacks(ctx)
		if err != nil {
			t.Fatalf("ListPacks: %v", err)
		}
		// live: starter + dlq-demo; second: starter(shadowed) + extra; dead contributes nothing.
		if len(res.Packs) != 4 {
			t.Fatalf("listing = %d packs, want 4", len(res.Packs))
		}
		var shadowed, extra bool
		for _, pk := range res.Packs {
			if pk.Name == catalog.StarterPack && pk.Source != live.URL {
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

	t.Run("empty catalogs yield an empty listing", func(t *testing.T) {
		p := NewCatalogReadPlane(nil, catalog.Resolver{}, nil)
		res, err := p.ListPacks(ctx)
		if err != nil {
			t.Fatalf("ListPacks: %v", err)
		}
		if len(res.Packs) != 0 {
			t.Fatalf("empty catalogs listing = %+v, want none", res)
		}
	})
}
