package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Fetcher fetches one URL's bytes; tests inject a fake, production uses HTTPFetch.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// maxPackBytes bounds one fetched document (index or pack tarball).
const maxPackBytes = 64 << 20

// maxPackFileBytes bounds one extracted pack file.
const maxPackFileBytes = 8 << 20

// maxPackTotalBytes bounds a pack's total decompressed size (gzip-bomb guard).
const maxPackTotalBytes = 64 << 20

// maxPackFiles bounds a pack's file count (entry-flood guard).
const maxPackFiles = 512

// indexFetchTimeout bounds one index fetch; the 5-minute HTTPFetch ceiling is for tarballs, and a listing must not hang behind a black-holed catalog.
const indexFetchTimeout = 15 * time.Second

// HTTPFetch is the production Fetcher: plain GET, timeout, 200-only, size-bounded.
func HTTPFetch(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch %s: %w", rawURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: fetch %s: unexpected status %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPackBytes+1))
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch %s: read body: %w", rawURL, err)
	}
	if len(data) > maxPackBytes {
		return nil, fmt.Errorf("catalog: fetch %s: body exceeds %d bytes", rawURL, maxPackBytes)
	}
	return data, nil
}

// Remote is one configured remote catalog: its index URL and fetch seam.
type Remote struct {
	// URL locates the catalog's index document (catalog.json).
	URL string
	// Fetch overrides HTTPFetch when non-nil, mainly for tests.
	Fetch Fetcher
}

// fetch returns the configured fetcher, defaulting to HTTPFetch.
func (r Remote) fetch() Fetcher {
	if r.Fetch != nil {
		return r.Fetch
	}
	return HTTPFetch
}

// Index fetches and parses the catalog's index, under the short index deadline.
func (r Remote) Index(ctx context.Context) (Index, error) {
	ctx, cancel := context.WithTimeout(ctx, indexFetchTimeout)
	defer cancel()
	data, err := r.fetch()(ctx, r.URL)
	if err != nil {
		return Index{}, err
	}
	idx, err := ParseIndex(data)
	if err != nil {
		return Index{}, fmt.Errorf("catalog: index %s: %w", r.URL, err)
	}
	return idx, nil
}

// Pack fetches entry e's tarball, verifies its pinned sha256 before parsing, and extracts the pack.
func (r Remote) Pack(ctx context.Context, e IndexEntry) (Pack, error) {
	u, err := r.packURL(e)
	if err != nil {
		return Pack{}, err
	}
	blob, err := r.fetch()(ctx, u)
	if err != nil {
		return Pack{}, err
	}
	sum := sha256.Sum256(blob)
	digest := hex.EncodeToString(sum[:])
	if !strings.EqualFold(digest, e.SHA256) {
		return Pack{}, fmt.Errorf("catalog: pack %q tarball digest mismatch: index pins %s, fetched %s", e.Name, strings.ToLower(e.SHA256), digest)
	}
	files, readme, err := extractPack(e.Name, blob)
	if err != nil {
		return Pack{}, err
	}
	return Pack{IndexEntry: e, Source: r.URL, README: readme, Files: files}, nil
}

// packURL resolves e.Path against the index URL, refusing missing pins and non-http(s) escapes.
func (r Remote) packURL(e IndexEntry) (string, error) {
	if e.Path == "" {
		return "", fmt.Errorf("catalog: pack %q: index entry carries no path", e.Name)
	}
	if e.SHA256 == "" {
		return "", fmt.Errorf("catalog: pack %q: index entry pins no sha256", e.Name)
	}
	base, err := url.Parse(r.URL)
	if err != nil {
		return "", fmt.Errorf("catalog: parse catalog url %s: %w", r.URL, err)
	}
	ref, err := url.Parse(e.Path)
	if err != nil {
		return "", fmt.Errorf("catalog: pack %q: parse path %q: %w", e.Name, e.Path, err)
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("catalog: pack %q: path %q resolves to non-http(s) url %s", e.Name, e.Path, resolved)
	}
	return resolved.String(), nil
}

// extractPack unpacks a gzipped pack tarball: regular files only, root README captured, unsafe entries refused.
func extractPack(name string, blob []byte) ([]File, string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, "", fmt.Errorf("catalog: pack %q: open gzip: %w", name, err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var files []File
	var readme string
	total := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("catalog: pack %q: read tar: %w", name, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		case tar.TypeReg:
		default:
			return nil, "", fmt.Errorf("catalog: pack %q: refusing non-regular tar entry %q (type %q)", name, hdr.Name, hdr.Typeflag)
		}
		if err := safeRel(hdr.Name); err != nil {
			return nil, "", fmt.Errorf("catalog: pack %q: %w", name, err)
		}
		if hdr.Size > maxPackFileBytes {
			return nil, "", fmt.Errorf("catalog: pack %q: file %s exceeds %d bytes", name, hdr.Name, maxPackFileBytes)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxPackFileBytes+1))
		if err != nil {
			return nil, "", fmt.Errorf("catalog: pack %q: read %s: %w", name, hdr.Name, err)
		}
		if len(data) > maxPackFileBytes {
			return nil, "", fmt.Errorf("catalog: pack %q: file %s exceeds %d bytes", name, hdr.Name, maxPackFileBytes)
		}
		if total += len(data); total > maxPackTotalBytes {
			return nil, "", fmt.Errorf("catalog: pack %q: decompressed size exceeds %d bytes", name, maxPackTotalBytes)
		}
		if len(files) >= maxPackFiles {
			return nil, "", fmt.Errorf("catalog: pack %q: more than %d files", name, maxPackFiles)
		}
		if hdr.Name == ReadmeName {
			readme = string(data)
			continue
		}
		files = append(files, File{Path: hdr.Name, Data: data})
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("catalog: pack %q tarball carries no files", name)
	}
	return files, readme, nil
}

// Listing is one pack visible across all configured sources.
type Listing struct {
	IndexEntry
	// Source is the pack's origin: a catalog URL.
	Source string
	// Shadowed marks a name already owned by an earlier source.
	Shadowed bool
}

// Resolver resolves packs across the configured remote catalogs, in order.
type Resolver struct {
	// Catalogs are the configured remote catalogs; the earliest wins a name clash.
	Catalogs []Remote
}

// List returns every visible pack from configured catalogs; an unreachable catalog
// is skipped and its error joined into the returned error beside the partial listing.
func (r Resolver) List(ctx context.Context) ([]Listing, error) {
	var out []Listing
	seen := map[string]bool{}
	var errs []error
	for _, c := range r.Catalogs {
		idx, err := c.Index(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("catalog %s unreachable: %w", c.URL, err))
			continue
		}
		for _, e := range idx.Packs {
			out = append(out, Listing{IndexEntry: e, Source: c.URL, Shadowed: seen[e.Name]})
			seen[e.Name] = true
		}
	}
	return out, errors.Join(errs...)
}

// Resolve returns the named pack from its first owning catalog, sha-verifying the
// tarball; an unreachable earlier catalog fails resolution so a shadowed pack can
// never win. ok is false when no configured catalog lists the name.
func (r Resolver) Resolve(ctx context.Context, name string) (Pack, bool, error) {
	for _, c := range r.Catalogs {
		idx, err := c.Index(ctx)
		if err != nil {
			return Pack{}, false, err
		}
		for _, e := range idx.Packs {
			if e.Name != name {
				continue
			}
			pk, err := c.Pack(ctx, e)
			if err != nil {
				return Pack{}, false, err
			}
			return pk, true, nil
		}
	}
	return Pack{}, false, nil
}
