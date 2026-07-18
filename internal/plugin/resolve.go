package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Resolved is one installed, digest-verified plugin version.
type Resolved struct {
	// Manifest is the manifest recorded at install, parsed.
	Manifest Manifest
	// Binary is the installed binary's path.
	Binary string
	// Digest is the installed binary's lowercase hex sha256, recomputed at
	// resolve and verified against the manifest's platform pin.
	Digest string
}

// Resolve loads one installed version, recomputing the binary digest and
// refusing any deviation from the manifest pin (the run-start verification).
func Resolve(root string, ref Ref) (*Resolved, error) {
	data, err := os.ReadFile(filepath.Join(Dir(root, ref.Name, ref.Version), ManifestFile)) //nolint:gosec // G304: the layout under root is this package's own.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("plugin: %s is not installed (iris plugin install <manifest>): %w", ref, err)
		}
		return nil, fmt.Errorf("plugin: resolve %s: %w", ref, err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("plugin: resolve %s: %w", ref, err)
	}
	if m.Name != ref.Name || m.Version != ref.Version {
		return nil, fmt.Errorf("plugin: resolve %s: installed manifest says %s@%s", ref, m.Name, m.Version)
	}
	bin, ok := m.Binaries[Platform()]
	if !ok {
		return nil, fmt.Errorf("plugin: resolve %s: manifest has no binary for platform %s", ref, Platform())
	}
	path := BinaryPath(root, ref.Name, ref.Version)
	blob, err := os.ReadFile(path) //nolint:gosec // G304: the layout under root is this package's own.
	if err != nil {
		return nil, fmt.Errorf("plugin: resolve %s: read binary: %w", ref, err)
	}
	digest := Digest(blob)
	if !strings.EqualFold(digest, bin.SHA256) {
		return nil, fmt.Errorf("plugin: resolve %s: installed binary digest %s deviates from the manifest pin %s; reinstall it", ref, digest, strings.ToLower(bin.SHA256))
	}
	return &Resolved{Manifest: *m, Binary: path, Digest: digest}, nil
}

// Entry is one installed version as list/verify report it.
type Entry struct {
	// Name is the installed plugin's name (the first directory key).
	Name string
	// Version is the installed version (the second directory key).
	Version string
	// Kind is the manifest's kind, empty when the manifest is unreadable.
	Kind Kind
	// Verbs are the manifest's verb names, sorted.
	Verbs []string
	// Digest is the installed binary's recomputed sha256 when verified.
	Digest string
	// Err is the resolve failure (unreadable manifest, digest deviation), nil
	// for a healthy entry.
	Err error
}

// List resolves every installed version, sorted by name then version; a broken
// entry reports its error rather than being omitted.
func List(root string) ([]Entry, error) {
	names, err := readDirNames(root)
	if err != nil {
		return nil, fmt.Errorf("plugin: list %s: %w", root, err)
	}
	var out []Entry
	for _, name := range names {
		versions, err := readDirNames(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("plugin: list %s: %w", name, err)
		}
		for _, version := range versions {
			out = append(out, entryFor(root, name, version))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// entryFor resolves one installed version into its list entry.
func entryFor(root, name, version string) Entry {
	e := Entry{Name: name, Version: version}
	res, err := Resolve(root, Ref{Name: name, Version: version})
	if err != nil {
		e.Err = err
		return e
	}
	e.Kind = res.Manifest.Kind
	e.Digest = res.Digest
	for verb := range res.Manifest.Verbs {
		e.Verbs = append(e.Verbs, verb)
	}
	sort.Strings(e.Verbs)
	return e
}

// Remove deletes one version (or all, when version is empty); a missing target errors.
func Remove(root, name, version string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("plugin: remove: name %q is not a lowercase slug", name)
	}
	target := filepath.Join(root, name)
	if version != "" {
		if !versionRe.MatchString(version) {
			return fmt.Errorf("plugin: remove: version %q is not a path-safe token", version)
		}
		target = Dir(root, name, version)
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("plugin: %s is not installed", refLabel(name, version))
		}
		return fmt.Errorf("plugin: remove %s: %w", refLabel(name, version), err)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("plugin: remove %s: %w", refLabel(name, version), err)
	}
	if version != "" {
		// Prune the name dir when this was the last version (no-op while others remain).
		_ = os.Remove(filepath.Join(root, name))
	}
	return nil
}

// refLabel renders a name with an optional version for messages.
func refLabel(name, version string) string {
	if version == "" {
		return name
	}
	return name + "@" + version
}

// readDirNames returns root's subdirectory names; absent root reads as empty.
func readDirNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}
