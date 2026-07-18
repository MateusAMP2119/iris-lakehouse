package catalog

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// TestEmbedded proves the embedded catalog loads: both founding packs, README and files present.
func TestEmbedded(t *testing.T) {
	packs, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("Embedded() = %d packs, want 2", len(packs))
	}
	byName := map[string]Pack{}
	for _, p := range packs {
		byName[p.Name] = p
		if p.Source != SourceEmbedded {
			t.Errorf("pack %q source = %q, want %q", p.Name, p.Source, SourceEmbedded)
		}
		if p.README == "" {
			t.Errorf("pack %q carries no README", p.Name)
		}
		if p.Description == "" || len(p.Tags) == 0 {
			t.Errorf("pack %q index entry is bare: %+v", p.Name, p.IndexEntry)
		}
		for _, f := range p.Files {
			if strings.HasSuffix(f.Path, ReadmeName) && !strings.Contains(f.Path, "/") {
				t.Errorf("pack %q materializes its README (%s)", p.Name, f.Path)
			}
		}
	}
	if _, ok := byName[StarterPack]; !ok {
		t.Fatalf("starter pack %q missing from the embedded catalog", StarterPack)
	}
	if _, ok := byName["dlq-demo"]; !ok {
		t.Fatal("dlq-demo missing from the embedded catalog")
	}
}

// TestEmbeddedPacksParseAndDiscover proves every embedded pack materializes into a valid workspace under the real declare loaders.
func TestEmbeddedPacksParseAndDiscover(t *testing.T) {
	packs, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() error: %v", err)
	}
	for _, p := range packs {
		t.Run(p.Name, func(t *testing.T) {
			root := t.TempDir()
			if _, err := Materialize(root, p, false); err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			ws, err := declare.DiscoverWorkspace(root)
			if err != nil {
				t.Fatalf("DiscoverWorkspace over the materialized pack: %v", err)
			}
			if len(ws.Pipelines) == 0 {
				t.Fatal("materialized pack discovers no pipelines")
			}
		})
	}
}

// TestApplyOrder proves the derived sequence: first member, composer, remaining members in composer order.
func TestApplyOrder(t *testing.T) {
	cases := []struct {
		pack string
		want []string
	}{
		{"quake-monitor", []string{
			"pipelines/healthy/quake_feed/iris-declare.yaml",
			"pipelines/healthy/iris-declare.yaml",
			"pipelines/healthy/quake_report/iris-declare.yaml",
		}},
		{"dlq-demo", []string{
			"pipelines/doomed/boom/iris-declare.yaml",
			"pipelines/doomed/iris-declare.yaml",
			"pipelines/doomed/aftershock/iris-declare.yaml",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.pack, func(t *testing.T) {
			p, ok, err := EmbeddedPack(tc.pack)
			if err != nil || !ok {
				t.Fatalf("EmbeddedPack(%q) = ok %v, err %v", tc.pack, ok, err)
			}
			got, err := ApplyOrder(p)
			if err != nil {
				t.Fatalf("ApplyOrder: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ApplyOrder = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ApplyOrder[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestApplyOrderInterlock proves a 2+ member lane without a composer is refused.
func TestApplyOrderInterlock(t *testing.T) {
	p := Pack{IndexEntry: IndexEntry{Name: "broken"}, Files: []File{
		{Path: "pipelines/l/a/iris-declare.yaml", Data: []byte("name: a\nrun: [sh, x]\nlane: l\n")},
		{Path: "pipelines/l/b/iris-declare.yaml", Data: []byte("name: b\nrun: [sh, x]\nlane: l\n")},
	}}
	if _, err := ApplyOrder(p); err == nil || !strings.Contains(err.Error(), "interlock") {
		t.Fatalf("ApplyOrder = %v, want the 2+ interlock refusal", err)
	}
}

// TestParseIndex proves the format gate and the nameless-entry refusal.
func TestParseIndex(t *testing.T) {
	if _, err := ParseIndex([]byte(`{"format":2,"packs":[]}`)); err == nil {
		t.Error("format 2 accepted, want refusal")
	}
	if _, err := ParseIndex([]byte(`{"format":1,"packs":[{"name":""}]}`)); err == nil {
		t.Error("nameless entry accepted, want refusal")
	}
	idx, err := ParseIndex([]byte(`{"format":1,"packs":[{"name":"p","tags":["t"]}]}`))
	if err != nil || len(idx.Packs) != 1 {
		t.Errorf("ParseIndex = %+v, %v; want one pack", idx, err)
	}
}

// TestPipelineNames proves the pack's declared pipeline roster.
func TestPipelineNames(t *testing.T) {
	p, _, err := EmbeddedPack(StarterPack)
	if err != nil {
		t.Fatalf("EmbeddedPack: %v", err)
	}
	names, err := PipelineNames(p)
	if err != nil {
		t.Fatalf("PipelineNames: %v", err)
	}
	if len(names) != 2 || names[0] != "quake_feed" || names[1] != "quake_report" {
		t.Fatalf("PipelineNames = %v, want [quake_feed quake_report]", names)
	}
}
