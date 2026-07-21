package catalog

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// TestApplyOrder proves the derived sequence: first member, composer, remaining members in composer order.
func TestApplyOrder(t *testing.T) {
	p := declPack(map[string]string{
		"pipelines/healthy/iris-declare.yaml":              "lane: healthy\norder:\n  - quake_feed\n  - quake_report\n",
		"pipelines/healthy/quake_feed/iris-declare.yaml":   "name: quake_feed\nrun: [python, main.py]\nlane: healthy\n",
		"pipelines/healthy/quake_report/iris-declare.yaml": "name: quake_report\nrun: [python, main.py]\nlane: healthy\ndepends_on: [quake_feed]\n",
	})
	want := []string{
		"pipelines/healthy/quake_feed/iris-declare.yaml",
		"pipelines/healthy/iris-declare.yaml",
		"pipelines/healthy/quake_report/iris-declare.yaml",
	}
	got, err := ApplyOrder(p)
	if err != nil {
		t.Fatalf("ApplyOrder: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ApplyOrder = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ApplyOrder[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestMaterializeDiscover proves a synthetic pack materializes into a workspace the declare loaders accept.
func TestMaterializeDiscover(t *testing.T) {
	p := declPack(map[string]string{
		"pipelines/l/iris-declare.yaml":   "lane: l\norder:\n  - a\n",
		"pipelines/l/a/iris-declare.yaml": "name: a\nrun: [sh, main.sh]\nlane: l\n",
		"pipelines/l/a/main.sh":           "#!/bin/sh\nexit 0\n",
	})
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
}

// declPack builds a synthetic pack from path->yaml pairs.
func declPack(files map[string]string) Pack {
	p := Pack{IndexEntry: IndexEntry{Name: "synthetic"}}
	for path, body := range files {
		p.Files = append(p.Files, File{Path: path, Data: []byte(body)})
	}
	return p
}

// TestApplyOrderDepAware proves the pipeline-granularity derivation: dependency-first
// member order, lane-cyclic-but-pipeline-acyclic packs accepted, true cycles and
// cross-lane duplicate names refused.
func TestApplyOrderDepAware(t *testing.T) {
	t.Run("a backward same-lane dep applies dependency-first, composer after the first member", func(t *testing.T) {
		p := declPack(map[string]string{
			"pipelines/l/iris-declare.yaml":   "lane: l\norder:\n  - a\n  - b\n",
			"pipelines/l/a/iris-declare.yaml": "name: a\nrun: [sh, x]\nlane: l\ndepends_on: [b]\n",
			"pipelines/l/b/iris-declare.yaml": "name: b\nrun: [sh, x]\nlane: l\n",
		})
		got, err := ApplyOrder(p)
		if err != nil {
			t.Fatalf("ApplyOrder: %v", err)
		}
		want := []string{"pipelines/l/b/iris-declare.yaml", "pipelines/l/iris-declare.yaml", "pipelines/l/a/iris-declare.yaml"}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("ApplyOrder = %v, want %v", got, want)
		}
	})

	t.Run("lane-cyclic but pipeline-acyclic packs derive without a false cycle", func(t *testing.T) {
		p := declPack(map[string]string{
			"pipelines/x/iris-declare.yaml":    "lane: x\norder:\n  - p1\n  - p2\n",
			"pipelines/x/p1/iris-declare.yaml": "name: p1\nrun: [sh, x]\nlane: x\ndepends_on: [q1]\n",
			"pipelines/x/p2/iris-declare.yaml": "name: p2\nrun: [sh, x]\nlane: x\n",
			"pipelines/y/iris-declare.yaml":    "lane: y\norder:\n  - q1\n  - q2\n",
			"pipelines/y/q1/iris-declare.yaml": "name: q1\nrun: [sh, x]\nlane: y\n",
			"pipelines/y/q2/iris-declare.yaml": "name: q2\nrun: [sh, x]\nlane: y\ndepends_on: [p2]\n",
		})
		got, err := ApplyOrder(p)
		if err != nil {
			t.Fatalf("ApplyOrder refused a pipeline-acyclic pack: %v", err)
		}
		if len(got) != 6 {
			t.Fatalf("ApplyOrder = %v, want 6 entries", got)
		}
		pos := map[string]int{}
		for i, s := range got {
			pos[s] = i
		}
		if pos["pipelines/y/q1/iris-declare.yaml"] > pos["pipelines/x/p1/iris-declare.yaml"] {
			t.Errorf("q1 must apply before its dependent p1: %v", got)
		}
		if pos["pipelines/x/p2/iris-declare.yaml"] > pos["pipelines/y/q2/iris-declare.yaml"] {
			t.Errorf("p2 must apply before its dependent q2: %v", got)
		}
		for _, lane := range []string{"x", "y"} {
			comp := "pipelines/" + lane + "/iris-declare.yaml"
			first := len(got)
			for _, s := range got {
				if strings.HasPrefix(s, "pipelines/"+lane+"/") && s != comp && pos[s] < first {
					first = pos[s]
				}
			}
			if pos[comp] != first+1 {
				t.Errorf("lane %s composer must ride right after its first member: %v", lane, got)
			}
		}
	})

	t.Run("a true pipeline cycle refuses", func(t *testing.T) {
		p := declPack(map[string]string{
			"pipelines/l/iris-declare.yaml":   "lane: l\norder:\n  - a\n  - b\n",
			"pipelines/l/a/iris-declare.yaml": "name: a\nrun: [sh, x]\nlane: l\ndepends_on: [b]\n",
			"pipelines/l/b/iris-declare.yaml": "name: b\nrun: [sh, x]\nlane: l\ndepends_on: [a]\n",
		})
		if _, err := ApplyOrder(p); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("ApplyOrder = %v, want the cycle refusal", err)
		}
	})

	t.Run("one name in two lanes refuses", func(t *testing.T) {
		p := declPack(map[string]string{
			"pipelines/l1/x/iris-declare.yaml": "name: x\nrun: [sh, x]\nlane: l1\n",
			"pipelines/l2/x/iris-declare.yaml": "name: x\nrun: [sh, x]\nlane: l2\n",
		})
		if _, err := ApplyOrder(p); err == nil || !strings.Contains(err.Error(), "declared twice") {
			t.Fatalf("ApplyOrder = %v, want the duplicate-name refusal", err)
		}
	})
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
	p := declPack(map[string]string{
		"pipelines/healthy/iris-declare.yaml":              "lane: healthy\norder:\n  - quake_feed\n  - quake_report\n",
		"pipelines/healthy/quake_feed/iris-declare.yaml":   "name: quake_feed\nrun: [python, main.py]\nlane: healthy\n",
		"pipelines/healthy/quake_report/iris-declare.yaml": "name: quake_report\nrun: [python, main.py]\nlane: healthy\n",
	})
	names, err := PipelineNames(p)
	if err != nil {
		t.Fatalf("PipelineNames: %v", err)
	}
	if len(names) != 2 || names[0] != "quake_feed" || names[1] != "quake_report" {
		t.Fatalf("PipelineNames = %v, want [quake_feed quake_report]", names)
	}
}

// TestPublicCatalogURL pins the setup default so a silent change is a test failure.
func TestPublicCatalogURL(t *testing.T) {
	if !strings.HasPrefix(PublicCatalogURL, "https://") || !strings.Contains(PublicCatalogURL, "iris-catalog") {
		t.Fatalf("PublicCatalogURL = %q, want the public iris-catalog index", PublicCatalogURL)
	}
}
