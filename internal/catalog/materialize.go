package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Materialize writes the pack's files under root, refusing when any target exists unless force overwrites.
func Materialize(root string, p Pack, force bool) ([]string, error) {
	for _, f := range p.Files {
		if err := safeRel(f.Path); err != nil {
			return nil, fmt.Errorf("catalog: install %q: %w", p.Name, err)
		}
	}
	if !force {
		var clash []string
		for _, f := range p.Files {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f.Path))); err == nil {
				clash = append(clash, f.Path)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("catalog: install %q: stat %s: %w", p.Name, f.Path, err)
			}
		}
		if len(clash) > 0 {
			sort.Strings(clash)
			return nil, fmt.Errorf("catalog: install %q refused: existing path(s) %s; pass force to overwrite", p.Name, strings.Join(clash, ", "))
		}
	}
	written := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		dst := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("catalog: install %q: %w", p.Name, err)
		}
		if err := os.WriteFile(dst, f.Data, 0o644); err != nil {
			return nil, fmt.Errorf("catalog: install %q: %w", p.Name, err)
		}
		written = append(written, f.Path)
	}
	sort.Strings(written)
	return written, nil
}

// safeRel refuses absolute or parent-escaping pack paths (remote packs are untrusted input).
func safeRel(p string) error {
	if p == "" || path.IsAbs(p) || strings.Contains(p, "\\") {
		return fmt.Errorf("unsafe pack path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("unsafe pack path %q", p)
		}
	}
	return nil
}
