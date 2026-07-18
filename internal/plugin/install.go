package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// maxFetchBytes bounds one manifest or binary download. Plugins ship real
// binaries (a headless browser is hundreds of megabytes) but never gigabytes.
const maxFetchBytes = 512 << 20

// Installed is one resolved installed plugin: its identity, the digest its
// binary hashes to on disk, and the manifest recorded beside it. The digest is
// what the run ledger pins.
type Installed struct {
	// Ref is the plugin's name@version identity.
	Ref Ref
	// Kind is the plugin kind from the installed manifest.
	Kind Kind
	// Digest is the hex sha256 the installed binary hashes to.
	Digest string
	// Path is the installed binary.
	Path string
	// Manifest is the manifest recorded at install time.
	Manifest *Manifest
}

// Installer installs, verifies and resolves plugins under one engine home. The
// zero value is not usable; NewInstaller supplies the platform and HTTP
// defaults, and tests override them.
type Installer struct {
	// Home is the engine home directory (~/.iris).
	Home string
	// Client fetches http(s) manifests and binaries.
	Client *http.Client
	// GOOS and GOARCH select the binaries entry to install.
	GOOS, GOARCH string
}

// NewInstaller returns an Installer for home targeting the running platform.
func NewInstaller(home string) *Installer {
	return &Installer{
		Home:   home,
		Client: &http.Client{Timeout: 10 * time.Minute},
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
	}
}

// Install fetches a manifest from source (an http(s) URL or a local file),
// downloads the platform's pinned binary, verifies its sha256 against the
// manifest, and installs both under <home>/plugins/<name>/<version>/. A digest
// mismatch refuses the install and leaves nothing behind. Reinstalling an
// existing version is an atomic repair, not an error.
func (i *Installer) Install(ctx context.Context, source string) (Installed, error) {
	data, base, err := i.fetchManifest(ctx, source)
	if err != nil {
		return Installed{}, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return Installed{}, err
	}
	if m.Kind != KindTool {
		return Installed{}, fmt.Errorf("plugin: %s is a %s plugin; only tool plugins are installable today", m.Ref(), m.Kind)
	}

	platform := i.GOOS + "/" + i.GOARCH
	bin, ok := m.Binaries[platform]
	if !ok {
		return Installed{}, fmt.Errorf("plugin: manifest %s pins no binary for %s (available: %s)",
			m.Ref(), platform, strings.Join(sortedKeys(m.Binaries), ", "))
	}
	binary, err := i.fetchBinary(ctx, bin.URL, base)
	if err != nil {
		return Installed{}, fmt.Errorf("plugin: fetch %s binary: %w", m.Ref(), err)
	}
	digest := digestOf(binary)
	if !strings.EqualFold(digest, bin.SHA256) {
		return Installed{}, fmt.Errorf("plugin: %s binary for %s: checksum mismatch: computed %s, manifest pins %s; refusing to install",
			m.Ref(), platform, digest, strings.ToLower(bin.SHA256))
	}

	ref := m.Ref()
	dir := Dir(i.Home, ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Installed{}, fmt.Errorf("plugin: create %s: %w", dir, err)
	}
	if err := writeAtomic(BinaryPath(i.Home, ref), binary, 0o755); err != nil {
		return Installed{}, fmt.Errorf("plugin: install %s binary: %w", ref, err)
	}
	if err := writeAtomic(manifestPath(i.Home, ref), data, 0o644); err != nil {
		return Installed{}, fmt.Errorf("plugin: record %s manifest: %w", ref, err)
	}
	return Installed{Ref: ref, Kind: m.Kind, Digest: digest, Path: BinaryPath(i.Home, ref), Manifest: m}, nil
}

// Verify re-checks one installed plugin: the recorded manifest still parses,
// and the binary on disk still hashes to the manifest's pin for this platform.
func (i *Installer) Verify(ref Ref) (Installed, error) {
	m, err := i.installedManifest(ref)
	if err != nil {
		return Installed{}, err
	}
	platform := i.GOOS + "/" + i.GOARCH
	bin, ok := m.Binaries[platform]
	if !ok {
		return Installed{}, fmt.Errorf("plugin: %s manifest pins no binary for %s", ref, platform)
	}
	path := BinaryPath(i.Home, ref)
	binary, err := os.ReadFile(path) //nolint:gosec // G304: the engine-home plugins tree is this package's own layout.
	if err != nil {
		return Installed{}, fmt.Errorf("plugin: read %s binary: %w", ref, err)
	}
	digest := digestOf(binary)
	if !strings.EqualFold(digest, bin.SHA256) {
		return Installed{}, fmt.Errorf("plugin: %s binary drifted: computed %s, manifest pins %s",
			ref, digest, strings.ToLower(bin.SHA256))
	}
	return Installed{Ref: ref, Kind: m.Kind, Digest: digest, Path: path, Manifest: m}, nil
}

// Resolve is Verify by another name at the run seam: the engine resolves every
// declared plugin ref before a run starts, and a missing install or a drifted
// digest refuses the run.
func (i *Installer) Resolve(ref Ref) (Installed, error) {
	inst, err := i.Verify(ref)
	if err != nil {
		return Installed{}, fmt.Errorf("plugin: resolve %s: %w", ref, err)
	}
	return inst, nil
}

// List returns every installed plugin version, sorted by name then version.
// Entries whose manifest no longer parses or whose binary is missing are
// reported as errors in place of being silently skipped.
func (i *Installer) List() ([]Installed, error) {
	root := Root(i.Home)
	names, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: read %s: %w", root, err)
	}
	var out []Installed
	for _, nameEntry := range names {
		if !nameEntry.IsDir() || strings.HasPrefix(nameEntry.Name(), ".") {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(root, nameEntry.Name()))
		if err != nil {
			return nil, fmt.Errorf("plugin: read %s versions: %w", nameEntry.Name(), err)
		}
		for _, versionEntry := range versions {
			if !versionEntry.IsDir() || strings.HasPrefix(versionEntry.Name(), ".") {
				continue
			}
			ref := Ref{Name: nameEntry.Name(), Version: versionEntry.Name()}
			inst, err := i.Verify(ref)
			if err != nil {
				return nil, err
			}
			out = append(out, inst)
		}
	}
	return out, nil
}

// Remove deletes one installed plugin version, pruning the plugin's name
// directory when no versions remain. Removing what is not installed is an
// error: the caller named something this home does not hold.
func (i *Installer) Remove(ref Ref) error {
	dir := Dir(i.Home, ref)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("plugin: %s is not installed", ref)
	} else if err != nil {
		return fmt.Errorf("plugin: stat %s: %w", dir, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("plugin: remove %s: %w", ref, err)
	}
	// Prune the now-possibly-empty name directory; a non-empty one stays.
	_ = os.Remove(filepath.Dir(dir))
	return nil
}

// installedManifest loads and parses the manifest recorded beside an installed
// plugin.
func (i *Installer) installedManifest(ref Ref) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath(i.Home, ref)) //nolint:gosec // G304: engine-home layout.
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("plugin: %s is not installed (no manifest at %s)", ref, manifestPath(i.Home, ref))
	}
	if err != nil {
		return nil, fmt.Errorf("plugin: read %s manifest: %w", ref, err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("plugin: installed manifest for %s: %w", ref, err)
	}
	if m.Ref() != ref {
		return nil, fmt.Errorf("plugin: installed manifest at %s identifies as %s, not %s", manifestPath(i.Home, ref), m.Ref(), ref)
	}
	return m, nil
}

// fetchManifest returns the manifest bytes for source plus the base directory
// for resolving relative binary paths: the manifest's own directory for a local
// file, empty for a URL (a URL-sourced manifest must pin absolute binary URLs).
func (i *Installer) fetchManifest(ctx context.Context, source string) (data []byte, base string, err error) {
	if isHTTP(source) {
		data, err := i.download(ctx, source)
		if err != nil {
			return nil, "", fmt.Errorf("plugin: fetch manifest: %w", err)
		}
		return data, "", nil
	}
	data, err = os.ReadFile(source) //nolint:gosec // G304: the manifest path is this command's user-supplied input by design.
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read manifest: %w", err)
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, "", fmt.Errorf("plugin: resolve manifest path: %w", err)
	}
	return data, filepath.Dir(abs), nil
}

// fetchBinary returns the binary bytes for url: an http(s) download, or a local
// file read for manifests loaded from disk (relative paths resolve against the
// manifest's directory).
func (i *Installer) fetchBinary(ctx context.Context, url, base string) ([]byte, error) {
	if isHTTP(url) {
		return i.download(ctx, url)
	}
	if base == "" {
		return nil, fmt.Errorf("a URL-sourced manifest must pin http(s) binary URLs, not %q", url)
	}
	path := url
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: resolved against the user-supplied manifest by design.
	if err != nil {
		return nil, err
	}
	return data, nil
}

// download GETs url and returns its body, bounded by maxFetchBytes. A non-200
// status is an error.
func (i *Installer) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := i.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxFetchBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxFetchBytes)
	}
	return body, nil
}

// digestOf returns the hex sha256 of data, the digest form the ledger pins.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeAtomic writes data to path via a temp file in the same directory and a
// rename, so a crashed install never leaves a half-written binary in place.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(tmp.Name())
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// isHTTP reports whether s is an http or https URL.
func isHTTP(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
