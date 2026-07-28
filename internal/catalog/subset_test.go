package catalog

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// subsetPack builds a pack shaped like a real one: two single-member lanes, one
// two-member lane with a composer and an in-lane depends_on, a cross-lane
// depends_on, a README, and a declared table.
func subsetPack() Pack {
	decl := func(name, lane string, deps ...string) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, "name: %s\nrun: [python3, main.py]\n", name)
		if lane != "" {
			fmt.Fprintf(&b, "lane: %s\n", lane)
		}
		if len(deps) > 0 {
			fmt.Fprintf(&b, "depends_on: [%s]\n", strings.Join(deps, ", "))
		}
		return []byte(b.String())
	}
	composer := []byte("lane: lusa\norder: [lusa_wire, lusa_articles]\n")
	return Pack{
		IndexEntry: IndexEntry{Name: "news"},
		README:     "readme",
		Files: []File{
			{Path: "README.md", Data: []byte("# news")},
			{Path: "schemas/news/articles/table.yaml", Data: []byte("name: articles\n")},
			{Path: "pipelines/dn/dn_feed/iris-declare.yaml", Data: decl("dn_feed", "dn")},
			{Path: "pipelines/dn/dn_feed/main.py", Data: []byte("dn")},
			{Path: "pipelines/lusa/iris-declare.yaml", Data: composer},
			{Path: "pipelines/lusa/lusa_wire/iris-declare.yaml", Data: decl("lusa_wire", "lusa")},
			{Path: "pipelines/lusa/lusa_wire/main.py", Data: []byte("wire")},
			{Path: "pipelines/lusa/lusa_articles/iris-declare.yaml", Data: decl("lusa_articles", "lusa", "lusa_wire")},
			{Path: "pipelines/lusa/lusa_articles/main.py", Data: []byte("articles")},
			{Path: "pipelines/pulse/news_pulse/iris-declare.yaml", Data: decl("news_pulse", "pulse", "dn_feed")},
			{Path: "pipelines/pulse/news_pulse/main.py", Data: []byte("pulse")},
		},
	}
}

// names returns the subset's pipeline names, sorted.
func names(t *testing.T, p Pack) []string {
	t.Helper()
	got, err := PipelineNames(p)
	if err != nil {
		t.Fatalf("PipelineNames: %v", err)
	}
	sort.Strings(got)
	return got
}

// TestPackSubset proves the narrowing rule: what a selection drags along, what
// it leaves behind, and that a narrowed pack still applies.
func TestPackSubset(t *testing.T) {
	t.Run("an empty selection is the whole pack", func(t *testing.T) {
		p := subsetPack()
		got, err := p.Subset(nil)
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		if len(got.Files) != len(p.Files) {
			t.Errorf("files = %d, want the pack untouched at %d", len(got.Files), len(p.Files))
		}
	})

	t.Run("a lone pipeline takes only itself and the pack-wide files", func(t *testing.T) {
		got, err := subsetPack().Subset([]string{"dn_feed"})
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		if want := []string{"dn_feed"}; len(names(t, got)) != 1 || names(t, got)[0] != want[0] {
			t.Errorf("pipelines = %v, want %v", names(t, got), want)
		}
		var paths []string
		for _, f := range got.Files {
			paths = append(paths, f.Path)
		}
		for _, want := range []string{"README.md", "schemas/news/articles/table.yaml", "pipelines/dn/dn_feed/main.py"} {
			if !contains(paths, want) {
				t.Errorf("files %v missing %q: the README and the tables belong to every install", paths, want)
			}
		}
		for _, gone := range []string{"pipelines/lusa/iris-declare.yaml", "pipelines/pulse/news_pulse/main.py"} {
			if contains(paths, gone) {
				t.Errorf("files %v still carry %q", paths, gone)
			}
		}
	})

	t.Run("a depends_on comes along", func(t *testing.T) {
		got, err := subsetPack().Subset([]string{"news_pulse"})
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		if want := []string{"dn_feed", "news_pulse"}; !equal(names(t, got), want) {
			t.Errorf("pipelines = %v, want %v: a declaration cannot name an absent dependency", names(t, got), want)
		}
	})

	t.Run("a lane comes along whole", func(t *testing.T) {
		// lusa_wire alone would leave the composer ordering an absent member.
		got, err := subsetPack().Subset([]string{"lusa_wire"})
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		if want := []string{"lusa_articles", "lusa_wire"}; !equal(names(t, got), want) {
			t.Errorf("pipelines = %v, want %v", names(t, got), want)
		}
		var paths []string
		for _, f := range got.Files {
			paths = append(paths, f.Path)
		}
		if !contains(paths, "pipelines/lusa/iris-declare.yaml") {
			t.Errorf("files %v dropped the lane composer", paths)
		}
	})

	t.Run("a narrowed pack still derives an apply order", func(t *testing.T) {
		got, err := subsetPack().Subset([]string{"lusa_articles"})
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		order, err := ApplyOrder(got)
		if err != nil {
			t.Fatalf("ApplyOrder on the subset: %v", err)
		}
		// wire before articles (depends_on), composer spliced after the first member.
		want := []string{
			"pipelines/lusa/lusa_wire/iris-declare.yaml",
			"pipelines/lusa/iris-declare.yaml",
			"pipelines/lusa/lusa_articles/iris-declare.yaml",
		}
		if !equal(order, want) {
			t.Errorf("apply order = %v, want %v", order, want)
		}
	})

	t.Run("an unknown pipeline refuses", func(t *testing.T) {
		if _, err := subsetPack().Subset([]string{"nope"}); err == nil {
			t.Error("Subset succeeded on an unknown pipeline, want a refusal")
		}
	})

	t.Run("no pack file is rewritten", func(t *testing.T) {
		p := subsetPack()
		got, err := p.Subset([]string{"lusa_articles"})
		if err != nil {
			t.Fatalf("Subset: %v", err)
		}
		orig := map[string]string{}
		for _, f := range p.Files {
			orig[f.Path] = string(f.Data)
		}
		for _, f := range got.Files {
			if orig[f.Path] != string(f.Data) {
				t.Errorf("file %s was rewritten; the index pinned its digest", f.Path)
			}
		}
	})
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
