// Package catalog resolves pipeline packs and materializes them into a workspace (#217).
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// IndexFormat is the newest catalog.json format version this engine
// understands; format 1 (tarball-only entries) stays accepted.
const IndexFormat = 2

// StarterPack is the pack `iris catalog init` installs (resolved from configured catalogs).
const StarterPack = "quake-monitor"

// PublicCatalogURL is the public iris-catalog index setup pins by default.
// Packs ship from that repo, not the engine binary.
const PublicCatalogURL = "https://raw.githubusercontent.com/MateusAMP2119/iris-catalog/main/catalog.json"

// ReadmeName is the pack's documentation file, shown by `show` and never materialized.
const ReadmeName = "README.md"

// Index is a catalog's catalog.json: a format version plus its pack entries.
type Index struct {
	// Format is the index format version; unknown versions are refused.
	Format int `json:"format"`
	// Packs are the catalog's pack entries.
	Packs []IndexEntry `json:"packs"`
}

// IndexEntry describes one pack in a catalog index.
type IndexEntry struct {
	// Name is the pack name a client installs by; never a URL.
	Name string `json:"name"`
	// Description is the one-line pack summary.
	Description string `json:"description,omitempty"`
	// Tags are free-form labels for browsing.
	Tags []string `json:"tags,omitempty"`
	// Path locates the pack tarball relative to the index URL (remote catalogs only).
	Path string `json:"path,omitempty"`
	// SHA256 is the hex digest of the pack tarball (remote catalogs only).
	SHA256 string `json:"sha256,omitempty"`
	// Dir locates a plain-file pack's source directory relative to the index
	// URL (format 2); the pack's files are fetched raw from under it.
	Dir string `json:"dir,omitempty"`
	// Files pins a plain-file pack's members (format 2): each path is both the
	// fetch location under Dir and the workspace-relative destination.
	Files []IndexFile `json:"files,omitempty"`
	// Requires is the minimum engine version the pack needs.
	Requires string `json:"requires,omitempty"`
}

// IndexFile pins one plain-file pack member.
type IndexFile struct {
	// Path is the file's location under the entry's Dir and its
	// workspace-relative destination, always slash-separated.
	Path string `json:"path"`
	// SHA256 is the hex digest of the file's bytes.
	SHA256 string `json:"sha256"`
}

// ParseIndex decodes a catalog.json, refusing unknown formats and nameless entries.
func ParseIndex(data []byte) (Index, error) {
	var idx Index
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&idx); err != nil {
		return Index{}, fmt.Errorf("catalog: parse index: %w", err)
	}
	if idx.Format < 1 || idx.Format > IndexFormat {
		return Index{}, fmt.Errorf("catalog: unsupported index format %d (engine understands up to %d)", idx.Format, IndexFormat)
	}
	for _, e := range idx.Packs {
		if e.Name == "" {
			return Index{}, errors.New("catalog: index entry with empty pack name")
		}
		if e.Path != "" && len(e.Files) > 0 {
			return Index{}, fmt.Errorf("catalog: pack %q carries both a tarball path and a files list", e.Name)
		}
		if len(e.Files) > 0 {
			if idx.Format < 2 {
				return Index{}, fmt.Errorf("catalog: pack %q lists files but the index declares format %d (files need format 2)", e.Name, idx.Format)
			}
			if e.Dir != "" {
				if err := safeRel(e.Dir); err != nil {
					return Index{}, fmt.Errorf("catalog: pack %q: %w", e.Name, err)
				}
			}
			for _, f := range e.Files {
				if err := safeRel(f.Path); err != nil {
					return Index{}, fmt.Errorf("catalog: pack %q: %w", e.Name, err)
				}
				if f.SHA256 == "" {
					return Index{}, fmt.Errorf("catalog: pack %q: file %s pins no sha256", e.Name, f.Path)
				}
			}
		}
	}
	return idx, nil
}

// File is one pack file: a slash-separated workspace-relative path and its bytes.
type File struct {
	// Path is the workspace-relative destination, always slash-separated.
	Path string
	// Data is the file content.
	Data []byte
}

// Pack is a resolved pack: its index entry, source, README, and workspace files.
type Pack struct {
	IndexEntry
	// Source names where the pack came from (a catalog index URL).
	Source string
	// README is the pack's documentation, empty when the pack carries none.
	README string
	// Files are the workspace files the pack materializes.
	Files []File
}
