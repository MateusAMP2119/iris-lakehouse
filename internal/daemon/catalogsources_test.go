package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// TestCatalogSourcesAdd proves the live source store: validation, dedupe, the
// index probe gate, persistence ordering (persist before activate), and the
// resolver snapshot seeing the grown list.
func TestCatalogSourcesAdd(t *testing.T) {
	okProbe := func(context.Context, catalog.Remote) error { return nil }
	add := func(s *catalogSources, url string) (api.CatalogSourceResult, error) {
		return s.AddSource(context.Background(), api.CatalogSourceRequest{URL: url})
	}
	// newSources builds a token-free source set, the shape every case here uses.
	newSources := func(t *testing.T, urls []string, persist func([]string) error) *catalogSources {
		t.Helper()
		s, err := newCatalogSources(urls, nil, persist, nil)
		if err != nil {
			t.Fatalf("newCatalogSources = %v, want success", err)
		}
		return s
	}

	t.Run("a valid url probes, persists, and activates", func(t *testing.T) {
		var persisted []string
		s := newSources(t, []string{"https://a/catalog.json"}, func(urls []string) error {
			persisted = append([]string(nil), urls...)
			return nil
		})
		s.probe = okProbe
		res, err := add(s, "https://b/catalog.json")
		if err != nil {
			t.Fatalf("AddSource = %v, want success", err)
		}
		want := []string{"https://a/catalog.json", "https://b/catalog.json"}
		if len(res.Sources) != 2 || res.Sources[1] != want[1] {
			t.Errorf("result sources = %v, want %v", res.Sources, want)
		}
		if len(persisted) != 2 || persisted[1] != want[1] {
			t.Errorf("persisted = %v, want the grown list written", persisted)
		}
		if r := s.resolver(); len(r.Catalogs) != 2 || r.Catalogs[1].URL != want[1] {
			t.Errorf("resolver snapshot = %+v, want the new source live", r.Catalogs)
		}
	})

	t.Run("a non-http url is refused before any probe", func(t *testing.T) {
		s := newSources(t, nil, nil)
		s.probe = func(context.Context, catalog.Remote) error {
			t.Fatal("an invalid url must not be probed")
			return nil
		}
		for _, bad := range []string{"", "ftp://x/catalog.json", "not a url", "file:///etc/passwd"} {
			if _, err := add(s, bad); err == nil {
				t.Errorf("AddSource(%q) succeeded, want a refusal", bad)
			}
		}
	})

	t.Run("a duplicate is refused", func(t *testing.T) {
		s := newSources(t, []string{"https://a/catalog.json"}, nil)
		s.probe = okProbe
		if _, err := add(s, "https://a/catalog.json"); err == nil || !strings.Contains(err.Error(), "already configured") {
			t.Fatalf("duplicate add = %v, want the already-configured refusal", err)
		}
	})

	t.Run("a failed probe refuses without persisting or activating", func(t *testing.T) {
		s := newSources(t, nil, func([]string) error {
			t.Fatal("a refused source must not persist")
			return nil
		})
		s.probe = func(context.Context, catalog.Remote) error { return errors.New("no catalog.json here") }
		if _, err := add(s, "https://dead/catalog.json"); err == nil || !strings.Contains(err.Error(), "no catalog.json here") {
			t.Fatalf("AddSource = %v, want the probe failure surfaced", err)
		}
		if len(s.urls()) != 0 {
			t.Errorf("urls = %v, want the refused source inactive", s.urls())
		}
	})

	t.Run("a failed persist refuses and stays on the old list", func(t *testing.T) {
		s := newSources(t, []string{"https://a/catalog.json"}, func([]string) error {
			return errors.New("disk full")
		})
		s.probe = okProbe
		if _, err := add(s, "https://b/catalog.json"); err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("AddSource = %v, want the persist failure surfaced", err)
		}
		if urls := s.urls(); len(urls) != 1 {
			t.Errorf("urls = %v, want the unpersisted source inactive", urls)
		}
	})
}

// TestCatalogSourcesTokens proves configured tokens reach every remote the
// daemon builds -- the ones from iris.toml, the probe of a runtime addition,
// and the remote that addition activates -- and that a malformed entry stops
// the daemon rather than starting it silently unauthenticated.
func TestCatalogSourcesTokens(t *testing.T) {
	const entry = "raw.githubusercontent.com=ghp_example"

	t.Run("configured sources carry the fetcher", func(t *testing.T) {
		s, err := newCatalogSources([]string{"https://a/catalog.json"}, []string{entry}, nil, nil)
		if err != nil {
			t.Fatalf("newCatalogSources = %v, want success", err)
		}
		for i, r := range s.resolver().Catalogs {
			if r.Fetch == nil {
				t.Errorf("catalog %d fetches with the default unauthenticated path", i)
			}
		}
	})

	t.Run("a runtime addition probes and activates with the same fetcher", func(t *testing.T) {
		s, err := newCatalogSources(nil, []string{entry}, nil, nil)
		if err != nil {
			t.Fatalf("newCatalogSources = %v, want success", err)
		}
		probed := false
		s.probe = func(_ context.Context, r catalog.Remote) error {
			probed = true
			if r.Fetch == nil {
				t.Error("the probe fetches unauthenticated, so a private source would be refused")
			}
			return nil
		}
		if _, err := s.AddSource(context.Background(), api.CatalogSourceRequest{URL: "https://b/catalog.json"}); err != nil {
			t.Fatalf("AddSource = %v, want success", err)
		}
		if !probed {
			t.Fatal("AddSource did not probe")
		}
		live := s.resolver().Catalogs
		if len(live) != 1 || live[0].Fetch == nil {
			t.Errorf("activated remote = %+v, want the token-aware fetcher", live)
		}
	})

	t.Run("a malformed entry refuses to start", func(t *testing.T) {
		for _, bad := range []string{"no-equals-sign", "=token", "host=", "https://host=token"} {
			if _, err := newCatalogSources(nil, []string{bad}, nil, nil); err == nil {
				t.Errorf("newCatalogSources(tokens=%q) succeeded, want a refusal", bad)
			}
		}
	})
}
