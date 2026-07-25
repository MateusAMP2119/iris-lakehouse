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

// TestSelectExistingEngineActionPreselect proves the non-interactive
// short-circuit of the existing-engine menu (IRIS_SETUP_EXISTING).
func TestSelectExistingEngineActionPreselect(t *testing.T) {
	for pre, want := range map[string]existingEngineAction{
		"reuse": existingReuse, "restart": existingRestart, "wipe": existingWipe,
	} {
		got, err := selectExistingEngineAction(pre, true, nil)
		if err != nil || got != want {
			t.Fatalf("preselect %q: got %v err %v, want %v", pre, got, err, want)
		}
	}
}

// TestWipeEngineState proves the clean-install wipe erases exactly the durable
// state — Postgres, workspace, logs, cache, config, pidfile, socket — and
// keeps the binary directory.
func TestWipeEngineState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("IRIS_HOME", home)
	for _, d := range []string{"pg/data", "workspace/pipelines", "logs", "ps-cache", "bin"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{config.FileName, "iris.pid", "iris.sock", "bin/iris"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := config.Settings{Socket: filepath.Join(home, "iris.sock")}
	if err := wipeEngineState(s); err != nil {
		t.Fatalf("wipeEngineState = %v", err)
	}
	for _, gone := range []string{"pg", "workspace", "logs", "ps-cache", config.FileName, "iris.pid", "iris.sock"} {
		if _, err := os.Stat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the wipe (err %v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "iris")); err != nil {
		t.Errorf("the binary must survive the wipe: %v", err)
	}
}

// TestApplySetupDefaults proves --default fills only unset answers.
func TestApplySetupDefaults(t *testing.T) {
	m, e, c := applySetupDefaults("", "", "")
	if m != "local" || e != "wipe" || c != "public" {
		t.Fatalf("defaults = %q %q %q, want local wipe public", m, e, c)
	}
	m, e, c = applySetupDefaults("skip", "reuse", "skip")
	if m != "skip" || e != "reuse" || c != "skip" {
		t.Fatalf("explicit answers must win, got %q %q %q", m, e, c)
	}
}
