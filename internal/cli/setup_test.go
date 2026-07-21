package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
)

// TestParseCatalogSetupURLs covers the headless catalog URL grammar.
func TestParseCatalogSetupURLs(t *testing.T) {
	t.Run("single https", func(t *testing.T) {
		got, err := parseCatalogSetupURLs("https://example.com/catalog.json")
		if err != nil || len(got) != 1 || got[0] != "https://example.com/catalog.json" {
			t.Fatalf("got %v, err %v", got, err)
		}
	})
	t.Run("comma list", func(t *testing.T) {
		got, err := parseCatalogSetupURLs("https://a/c.json, https://b/c.json")
		if err != nil || len(got) != 2 {
			t.Fatalf("got %v, err %v", got, err)
		}
	})
	t.Run("rejects non-http", func(t *testing.T) {
		if _, err := parseCatalogSetupURLs("file:///tmp/catalog.json"); err == nil {
			t.Fatal("expected refusal of file://")
		}
	})
	t.Run("rejects empty", func(t *testing.T) {
		if _, err := parseCatalogSetupURLs("  ,  "); err == nil {
			t.Fatal("expected empty refusal")
		}
	})
}

// TestSelectCatalogSetupPreselect proves the non-interactive short-circuit.
func TestSelectCatalogSetupPreselect(t *testing.T) {
	choice, urls, err := selectCatalogSetup("public", nil)
	if err != nil || choice != catalogSetupPublic || urls != nil {
		t.Fatalf("public: choice=%v urls=%v err=%v", choice, urls, err)
	}
	choice, urls, err = selectCatalogSetup("skip", nil)
	if err != nil || choice != catalogSetupSkip {
		t.Fatalf("skip: choice=%v err=%v", choice, err)
	}
	choice, urls, err = selectCatalogSetup("https://x/catalog.json", nil)
	if err != nil || choice != catalogSetupCustom || len(urls) != 1 {
		t.Fatalf("custom: choice=%v urls=%v err=%v", choice, urls, err)
	}
}

// TestSetupCatalogsWritesTOML proves the public preselect lands catalogs in iris.toml.
func TestSetupCatalogsWritesTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("IRIS_HOME", home)
	t.Setenv("HOME", home) // belt-and-suspenders if Home falls back

	var out bytes.Buffer
	a := newApp(&out, &out)
	log := newCeremonyLog(&out)
	done := func(label string) {
		log.line(formatCeremonyLine(label, ceremonyCheckMark("✓")))
	}
	if err := a.setupCatalogs("public", log, done); err != nil {
		t.Fatalf("setupCatalogs: %v", err)
	}
	tomlPath := filepath.Join(home, config.FileName)
	res, err := config.LoadTOMLFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadTOMLFile: %v", err)
	}
	if res.Layer.Catalogs == nil || len(*res.Layer.Catalogs) != 1 || (*res.Layer.Catalogs)[0] != catalog.PublicCatalogURL {
		t.Fatalf("Catalogs = %#v, want [%s]", res.Layer.Catalogs, catalog.PublicCatalogURL)
	}
	if !strings.Contains(out.String(), "Catalog configured") && !strings.Contains(out.String(), "Public catalog") {
		t.Errorf("ceremony output missing catalog lines:\n%s", out.String())
	}
}

// TestSetupCatalogsSkipLeavesNoFile proves skip does not create iris.toml.
func TestSetupCatalogsSkipLeavesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("IRIS_HOME", home)

	var out bytes.Buffer
	a := newApp(&out, &out)
	log := newCeremonyLog(&out)
	done := func(string) {}
	if err := a.setupCatalogs("skip", log, done); err != nil {
		t.Fatalf("setupCatalogs skip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, config.FileName)); !os.IsNotExist(err) {
		t.Fatalf("skip wrote iris.toml: %v", err)
	}
	if !strings.Contains(out.String(), "Catalog: skipped") {
		t.Errorf("output = %q, want Catalog: skipped", out.String())
	}
}
