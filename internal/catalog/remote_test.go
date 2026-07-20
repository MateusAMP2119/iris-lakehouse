package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tarEntry is one member for packTarGz: a header plus its body.
type tarEntry struct {
	hdr  tar.Header
	body []byte
}

// regEntry builds a regular-file tar entry.
func regEntry(name, body string) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte(body)}
}

// packTarGz builds a gzipped tarball from entries, the shape a remote catalog serves.
func packTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		h := e.hdr
		h.Size = int64(len(e.body))
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("tar header %q: %v", h.Name, err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatalf("tar write %q: %v", h.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// sha256Hex returns the lowercase hex sha256 of b, the form an index pins.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// catalogServer serves an index at /catalog.json plus tarballs at their paths, returning the catalog Remote.
func catalogServer(t *testing.T, entries []IndexEntry, blobs map[string][]byte) Remote {
	t.Helper()
	raw, err := json.Marshal(Index{Format: IndexFormat, Packs: entries})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	})
	for p, blob := range blobs {
		mux.HandleFunc("/"+p, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(blob)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Remote{URL: srv.URL + "/catalog.json"}
}

// demoTarball builds a small valid pack tarball with a root README, a nested readme, and a skipped directory.
func demoTarball(t *testing.T) []byte {
	t.Helper()
	return packTarGz(t, []tarEntry{
		{hdr: tar.Header{Name: "pipelines/", Typeflag: tar.TypeDir, Mode: 0o755}},
		regEntry("pipelines/demo/iris-declare.yaml", "kind: pipeline\n"),
		regEntry("pipelines/demo/README.md", "nested readme stays a file\n"),
		regEntry("schemas/demo/table.yaml", "schema: demo\n"),
		regEntry(ReadmeName, "# Demo pack\n"),
	})
}

// demoEntry pins blob for name at packs/<name>.tar.gz.
func demoEntry(name string, blob []byte) IndexEntry {
	return IndexEntry{Name: name, Path: "packs/" + name + ".tar.gz", SHA256: sha256Hex(blob)}
}

// TestRemotePack proves fetch, sha verify, README capture, and extraction against a real server.
func TestRemotePack(t *testing.T) {
	blob := demoTarball(t)
	e := demoEntry("demo", blob)
	e.Requires = "v0.5.0"
	r := catalogServer(t, []IndexEntry{e}, map[string][]byte{"packs/demo.tar.gz": blob})

	p, err := r.Pack(context.Background(), e)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if p.Source != r.URL {
		t.Errorf("Source = %q, want the catalog url %q", p.Source, r.URL)
	}
	if p.Requires != "v0.5.0" || p.Name != "demo" {
		t.Errorf("IndexEntry not carried: %+v", p.IndexEntry)
	}
	if p.README != "# Demo pack\n" {
		t.Errorf("README = %q, want the root README.md content", p.README)
	}
	got := map[string]string{}
	for _, f := range p.Files {
		got[f.Path] = string(f.Data)
	}
	want := map[string]string{
		"pipelines/demo/iris-declare.yaml": "kind: pipeline\n",
		"pipelines/demo/README.md":         "nested readme stays a file\n",
		"schemas/demo/table.yaml":          "schema: demo\n",
	}
	if len(got) != len(want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
	for path, data := range want {
		if got[path] != data {
			t.Errorf("file %q = %q, want %q", path, got[path], data)
		}
	}
}

// TestRemotePackShaMismatch proves a digest deviation refuses before parsing, naming both digests.
func TestRemotePackShaMismatch(t *testing.T) {
	blob := demoTarball(t)
	e := demoEntry("demo", blob)
	e.SHA256 = strings.Repeat("ab", 32)
	r := catalogServer(t, []IndexEntry{e}, map[string][]byte{"packs/demo.tar.gz": blob})

	_, err := r.Pack(context.Background(), e)
	if err == nil {
		t.Fatal("Pack: nil error on sha mismatch, want a refusal")
	}
	if !strings.Contains(err.Error(), e.SHA256) || !strings.Contains(err.Error(), sha256Hex(blob)) {
		t.Errorf("mismatch error %q does not name both digests", err)
	}
}

// TestRemotePackRefusesUnsafeTarballs proves hostile tar members and escaping paths refuse extraction.
func TestRemotePackRefusesUnsafeTarballs(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{"traversal path", []tarEntry{regEntry("../evil.yaml", "x")}},
		{"absolute path", []tarEntry{regEntry("/etc/evil.yaml", "x")}},
		{"dot segment", []tarEntry{regEntry("pipelines/./a.yaml", "x")}},
		{"backslash path", []tarEntry{regEntry(`pipelines\a.yaml`, "x")}},
		{"symlink", []tarEntry{{hdr: tar.Header{Name: "pipelines/link", Typeflag: tar.TypeSymlink, Linkname: "../../etc/passwd", Mode: 0o777}}}},
		{"hardlink", []tarEntry{{hdr: tar.Header{Name: "pipelines/hard", Typeflag: tar.TypeLink, Linkname: "catalog.json", Mode: 0o644}}}},
		{"char device", []tarEntry{{hdr: tar.Header{Name: "pipelines/dev", Typeflag: tar.TypeChar, Mode: 0o644}}}},
		{"oversize file", []tarEntry{regEntry("pipelines/big.yaml", strings.Repeat("a", maxPackFileBytes+1))}},
		{"empty tarball", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := packTarGz(t, tc.entries)
			e := demoEntry("evil", blob)
			r := catalogServer(t, []IndexEntry{e}, map[string][]byte{"packs/evil.tar.gz": blob})
			if _, err := r.Pack(context.Background(), e); err == nil {
				t.Fatal("Pack: nil error on unsafe tarball, want a refusal")
			}
		})
	}
}

// TestRemotePackURLGuards proves missing pins and non-http(s) path escapes refuse before any fetch.
func TestRemotePackURLGuards(t *testing.T) {
	blob := demoTarball(t)
	cases := []struct {
		name  string
		entry IndexEntry
	}{
		{"no path", IndexEntry{Name: "p", SHA256: sha256Hex(blob)}},
		{"no sha256", IndexEntry{Name: "p", Path: "packs/p.tar.gz"}},
		{"scheme escape", IndexEntry{Name: "p", Path: "ftp://evil.example/p.tar.gz", SHA256: sha256Hex(blob)}},
		{"file escape", IndexEntry{Name: "p", Path: "file:///etc/passwd", SHA256: sha256Hex(blob)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Remote{URL: "https://catalog.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) {
				t.Fatal("fetch reached on a guarded entry")
				return nil, nil
			}}
			if _, err := r.Pack(context.Background(), tc.entry); err == nil {
				t.Fatal("Pack: nil error, want a refusal")
			}
		})
	}
}

// TestResolverList proves source order, shadowing, and the failed-catalog partial listing.
func TestResolverList(t *testing.T) {
	alphaA := demoTarball(t)
	a := catalogServer(t, []IndexEntry{demoEntry(StarterPack, alphaA), demoEntry("alpha", alphaA)}, nil)
	b := catalogServer(t, []IndexEntry{demoEntry("alpha", alphaA), demoEntry("beta", alphaA)}, nil)

	t.Run("shadowing order", func(t *testing.T) {
		out, err := Resolver{Catalogs: []Remote{a, b}}.List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		type row struct {
			name, source string
			shadowed     bool
		}
		var got []row
		for _, l := range out {
			got = append(got, row{l.Name, l.Source, l.Shadowed})
		}
		want := []row{
			{StarterPack, SourceEmbedded, false},
			{"dlq-demo", SourceEmbedded, false},
			{StarterPack, a.URL, true},
			{"alpha", a.URL, false},
			{"alpha", b.URL, true},
			{"beta", b.URL, false},
		}
		if len(got) != len(want) {
			t.Fatalf("List = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("listing[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("failed catalog keeps partial listing", func(t *testing.T) {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(broken.Close)
		down := Remote{URL: broken.URL + "/catalog.json"}
		out, err := Resolver{Catalogs: []Remote{down, b}}.List(context.Background())
		if err == nil {
			t.Fatal("List: nil error with an unreachable catalog, want a joined error")
		}
		names := map[string]string{}
		for _, l := range out {
			names[l.Name+"@"+l.Source] = l.Source
		}
		for _, want := range []string{StarterPack + "@" + SourceEmbedded, "dlq-demo@" + SourceEmbedded, "alpha@" + b.URL, "beta@" + b.URL} {
			if _, ok := names[want]; !ok {
				t.Errorf("partial listing lacks %s: %v", want, names)
			}
		}
	})
}

// TestResolverResolve proves embedded-first ownership, remote sha-verified wins, and clean not-found.
func TestResolverResolve(t *testing.T) {
	winner := packTarGz(t, []tarEntry{regEntry("pipelines/alpha/iris-declare.yaml", "first catalog\n")})
	loser := packTarGz(t, []tarEntry{regEntry("pipelines/alpha/iris-declare.yaml", "second catalog\n")})
	a := catalogServer(t, []IndexEntry{demoEntry("alpha", winner)}, map[string][]byte{"packs/alpha.tar.gz": winner})
	b := catalogServer(t, []IndexEntry{demoEntry("alpha", loser)}, map[string][]byte{"packs/alpha.tar.gz": loser})
	res := Resolver{Catalogs: []Remote{a, b}}

	t.Run("embedded wins without fetching", func(t *testing.T) {
		var fetched bool
		spy := Remote{URL: "https://catalog.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) {
			fetched = true
			return nil, errors.New("unreachable")
		}}
		p, ok, err := Resolver{Catalogs: []Remote{spy}}.Resolve(context.Background(), StarterPack)
		if err != nil || !ok {
			t.Fatalf("Resolve(%s) = ok=%v, err=%v", StarterPack, ok, err)
		}
		if p.Source != SourceEmbedded {
			t.Errorf("Source = %q, want %q", p.Source, SourceEmbedded)
		}
		if fetched {
			t.Error("remote fetched although the embedded set owns the name")
		}
	})

	t.Run("first catalog owns the name", func(t *testing.T) {
		p, ok, err := res.Resolve(context.Background(), "alpha")
		if err != nil || !ok {
			t.Fatalf("Resolve(alpha) = ok=%v, err=%v", ok, err)
		}
		if p.Source != a.URL {
			t.Errorf("Source = %q, want the first catalog %q", p.Source, a.URL)
		}
		if len(p.Files) != 1 || string(p.Files[0].Data) != "first catalog\n" {
			t.Errorf("Files = %v, want the first catalog's content", p.Files)
		}
	})

	t.Run("not found is clean", func(t *testing.T) {
		p, ok, err := res.Resolve(context.Background(), "no-such-pack")
		if err != nil || ok || p.Name != "" {
			t.Errorf("Resolve(no-such-pack) = %+v, ok=%v, err=%v; want zero, false, nil", p, ok, err)
		}
	})

	t.Run("unreachable earlier catalog fails resolution", func(t *testing.T) {
		down := Remote{URL: "https://catalog.example/catalog.json", Fetch: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("unreachable")
		}}
		if _, _, err := (Resolver{Catalogs: []Remote{down, b}}).Resolve(context.Background(), "alpha"); err == nil {
			t.Error("Resolve: nil error although an earlier catalog is unreachable; a shadowed pack must not win")
		}
	})
}
