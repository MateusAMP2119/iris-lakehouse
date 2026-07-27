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

	t.Run("a valid url probes, persists, and activates", func(t *testing.T) {
		var persisted []string
		s := newCatalogSources([]string{"https://a/catalog.json"}, func(urls []string) error {
			persisted = append([]string(nil), urls...)
			return nil
		}, nil)
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
		s := newCatalogSources(nil, nil, nil)
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
		s := newCatalogSources([]string{"https://a/catalog.json"}, nil, nil)
		s.probe = okProbe
		if _, err := add(s, "https://a/catalog.json"); err == nil || !strings.Contains(err.Error(), "already configured") {
			t.Fatalf("duplicate add = %v, want the already-configured refusal", err)
		}
	})

	t.Run("a failed probe refuses without persisting or activating", func(t *testing.T) {
		s := newCatalogSources(nil, func([]string) error {
			t.Fatal("a refused source must not persist")
			return nil
		}, nil)
		s.probe = func(context.Context, catalog.Remote) error { return errors.New("no catalog.json here") }
		if _, err := add(s, "https://dead/catalog.json"); err == nil || !strings.Contains(err.Error(), "no catalog.json here") {
			t.Fatalf("AddSource = %v, want the probe failure surfaced", err)
		}
		if len(s.urls()) != 0 {
			t.Errorf("urls = %v, want the refused source inactive", s.urls())
		}
	})

	t.Run("a failed persist refuses and stays on the old list", func(t *testing.T) {
		s := newCatalogSources([]string{"https://a/catalog.json"}, func([]string) error {
			return errors.New("disk full")
		}, nil)
		s.probe = okProbe
		if _, err := add(s, "https://b/catalog.json"); err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("AddSource = %v, want the persist failure surfaced", err)
		}
		if urls := s.urls(); len(urls) != 1 {
			t.Errorf("urls = %v, want the unpersisted source inactive", urls)
		}
	})
}
