package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed packs
var packsFS embed.FS

// Embedded returns the packs compiled into the binary, in index order.
func Embedded() ([]Pack, error) {
	raw, err := packsFS.ReadFile("packs/catalog.json")
	if err != nil {
		return nil, fmt.Errorf("catalog: read embedded index: %w", err)
	}
	idx, err := ParseIndex(raw)
	if err != nil {
		return nil, err
	}
	packs := make([]Pack, 0, len(idx.Packs))
	for _, e := range idx.Packs {
		p, err := loadEmbedded(e)
		if err != nil {
			return nil, err
		}
		packs = append(packs, p)
	}
	return packs, nil
}

// EmbeddedPack returns the named embedded pack, reporting whether it exists.
func EmbeddedPack(name string) (Pack, bool, error) {
	packs, err := Embedded()
	if err != nil {
		return Pack{}, false, err
	}
	for _, p := range packs {
		if p.Name == name {
			return p, true, nil
		}
	}
	return Pack{}, false, nil
}

// loadEmbedded reads one pack directory out of the embedded filesystem.
func loadEmbedded(e IndexEntry) (Pack, error) {
	root := "packs/" + e.Name
	p := Pack{IndexEntry: e, Source: SourceEmbedded}
	err := fs.WalkDir(packsFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := packsFS.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel := strings.TrimPrefix(path, root+"/")
		if rel == ReadmeName {
			p.README = string(data)
			return nil
		}
		p.Files = append(p.Files, File{Path: rel, Data: data})
		return nil
	})
	if err != nil {
		return Pack{}, fmt.Errorf("catalog: load embedded pack %q: %w", e.Name, err)
	}
	if len(p.Files) == 0 {
		return Pack{}, fmt.Errorf("catalog: embedded pack %q is empty", e.Name)
	}
	return p, nil
}
