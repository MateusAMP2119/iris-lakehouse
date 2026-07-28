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
	return fetchURL(ctx, rawURL, nil)
}

// fetchURL is the one fetch path, authenticated or not: HTTPFetch holds no
// tokens, HostTokens.Fetch holds some, and everything else about the request
// is identical either way.
func fetchURL(ctx context.Context, rawURL string, tokens HostTokens) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch %s: %w", rawURL, err)
	}
	client := http.DefaultClient
	if token := tokens.tokenFor(req.URL); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		client = authorizedClient(req.URL.Hostname())
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// A private catalog answers an unauthenticated reader with 404 as readily
		// as with 401, so a bare status reads as "gone" when it means "not yours".
		// Say so whenever no token is held for the host.
		if authIsMissing(resp.StatusCode, tokens, req.URL) {
			return nil, fmt.Errorf("catalog: fetch %s: unexpected status %s (no token configured for %s: a private catalog answers this way; add %s=<token> to catalog_tokens)", rawURL, resp.Status, req.URL.Hostname(), req.URL.Hostname())
		}
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

// Pack fetches entry e's content, verifying every pinned sha256: a files entry
// (format 2) fetches each plain file raw, a path entry the pack tarball.
func (r Remote) Pack(ctx context.Context, e IndexEntry) (Pack, error) {
	if len(e.Files) > 0 {
		return r.filePack(ctx, e)
	}
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

// filePack fetches a files entry's members one by one, verifying each pinned
// sha256, capturing the root README, and enforcing the same bounds as a
// tarball extraction.
func (r Remote) filePack(ctx context.Context, e IndexEntry) (Pack, error) {
	if len(e.Files) > maxPackFiles {
		return Pack{}, fmt.Errorf("catalog: pack %q: more than %d files", e.Name, maxPackFiles)
	}
	var files []File
	var readme string
	total := 0
	for _, pin := range e.Files {
		if err := safeRel(pin.Path); err != nil {
			return Pack{}, fmt.Errorf("catalog: pack %q: %w", e.Name, err)
		}
		rel := pin.Path
		if e.Dir != "" {
			rel = e.Dir + "/" + pin.Path
		}
		u, err := r.resolveURL(e.Name, rel)
		if err != nil {
			return Pack{}, err
		}
		data, err := r.fetch()(ctx, u)
		if err != nil {
			return Pack{}, err
		}
		if len(data) > maxPackFileBytes {
			return Pack{}, fmt.Errorf("catalog: pack %q: file %s exceeds %d bytes", e.Name, pin.Path, maxPackFileBytes)
		}
		sum := sha256.Sum256(data)
		if digest := hex.EncodeToString(sum[:]); !strings.EqualFold(digest, pin.SHA256) {
			return Pack{}, fmt.Errorf("catalog: pack %q: file %s digest mismatch: index pins %s, fetched %s", e.Name, pin.Path, strings.ToLower(pin.SHA256), digest)
		}
		if total += len(data); total > maxPackTotalBytes {
			return Pack{}, fmt.Errorf("catalog: pack %q: total size exceeds %d bytes", e.Name, maxPackTotalBytes)
		}
		if pin.Path == ReadmeName {
			readme = string(data)
			continue
		}
		files = append(files, File{Path: pin.Path, Data: data})
	}
	if len(files) == 0 {
		return Pack{}, fmt.Errorf("catalog: pack %q lists no files beyond its README", e.Name)
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
	return r.resolveURL(e.Name, e.Path)
}

// resolveURL resolves one pack-relative reference against the index URL,
// refusing non-http(s) escapes.
func (r Remote) resolveURL(name, rel string) (string, error) {
	base, err := url.Parse(r.URL)
	if err != nil {
		return "", fmt.Errorf("catalog: parse catalog url %s: %w", r.URL, err)
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return "", fmt.Errorf("catalog: pack %q: parse path %q: %w", name, rel, err)
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("catalog: pack %q: path %q resolves to non-http(s) url %s", name, rel, resolved)
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
