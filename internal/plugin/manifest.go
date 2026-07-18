// Package plugin (#215): manifests, digest-pinned installs under the engine
// home, and verified resolution of installed plugin binaries. Leaf package.
package plugin

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// ManifestFile is the canonical manifest filename.
const ManifestFile = "manifest.yaml"

// DirName is the plugins directory name under the engine home (~/.iris/plugins).
const DirName = "plugins"

// DefaultVerbTimeoutSeconds bounds a verb call whose manifest sets no timeout.
const DefaultVerbTimeoutSeconds int64 = 60

// Kind discriminates tool (exec per call) from service (supervised sidecar, later stage).
type Kind string

// The plugin kinds.
const (
	// KindTool is a run-to-completion subprocess, exec'd per call.
	KindTool Kind = "tool"
	// KindService is a daemon-supervised sidecar (not yet supported).
	KindService Kind = "service"
)

// Verb is one declared engine-mediated operation with its per-call timeout.
type Verb struct {
	// TimeoutSeconds bounds one call to this verb; zero means the default.
	TimeoutSeconds int64 `yaml:"timeout_seconds"`
}

// Timeout returns the verb's effective timeout in seconds.
func (v Verb) Timeout() int64 {
	if v.TimeoutSeconds > 0 {
		return v.TimeoutSeconds
	}
	return DefaultVerbTimeoutSeconds
}

// Binary is one per-platform entry: fetch location plus pinned sha256.
type Binary struct {
	// URL locates the binary: http(s), or a path relative to the manifest.
	URL string `yaml:"url"`
	// SHA256 is the hex digest the fetched bytes must match.
	SHA256 string `yaml:"sha256"`
}

// Manifest is a parsed plugin manifest: identity, kind, verbs, pinned binaries.
type Manifest struct {
	// Name is the plugin name (a lowercase slug; the install directory key).
	Name string `yaml:"name"`
	// Version is the plugin version (a path-safe token; the second directory key).
	Version string `yaml:"version"`
	// Kind is the plugin kind: tool (exec per call) or service (later epic stage).
	Kind Kind `yaml:"kind"`
	// Verbs are the declared verbs by name.
	Verbs map[string]Verb `yaml:"verbs"`
	// Binaries are the per-platform entries keyed "goos/goarch" (e.g. "darwin/arm64").
	Binaries map[string]Binary `yaml:"binaries"`
}

// Field whitelists, enforced like declare's: unknown keys error by name.
var (
	manifestFields    = map[string]bool{"name": true, "version": true, "kind": true, "verbs": true, "binaries": true}
	verbFields        = map[string]bool{"timeout_seconds": true}
	binaryFields      = map[string]bool{"url": true, "sha256": true}
	manifestFieldList = "name, version, kind, verbs, binaries"
)

// nameRe admits plugin/verb names: path-safe lowercase slugs (they key the install dir).
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// versionRe admits path-safe version tokens (no leading dot, so no ".." and no hidden entries).
var versionRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// sha256Re admits a 64-hex-digit digest.
var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// platformRe admits binary platform keys: "goos/goarch".
var platformRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// ParseManifest parses and validates a manifest: whitelisted fields, path-safe
// name/version/verbs, kind value set, ≥1 verb and binary, well-formed digests.
func ParseManifest(data []byte) (*Manifest, error) {
	raw := map[string]any{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("plugin: parse %s: %w", ManifestFile, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("plugin: empty manifest: %s needs %s", ManifestFile, manifestFieldList)
	}
	if err := checkKeys("", raw, manifestFields, manifestFieldList); err != nil {
		return nil, err
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("plugin: parse %s: %w", ManifestFile, err)
	}
	if !nameRe.MatchString(m.Name) {
		return nil, fmt.Errorf("plugin: manifest name %q is not a lowercase slug ([a-z0-9_-], no leading separator)", m.Name)
	}
	if !versionRe.MatchString(m.Version) {
		return nil, fmt.Errorf("plugin: manifest version %q is not a path-safe token", m.Version)
	}
	if m.Kind != KindTool && m.Kind != KindService {
		return nil, fmt.Errorf("plugin: manifest kind %q is neither %q nor %q", m.Kind, KindTool, KindService)
	}
	if len(m.Verbs) == 0 {
		return nil, fmt.Errorf("plugin: manifest %s@%s declares no verbs", m.Name, m.Version)
	}
	if err := checkShapes(raw, "verbs", verbFields, "timeout_seconds"); err != nil {
		return nil, err
	}
	for verb, v := range m.Verbs {
		if !nameRe.MatchString(verb) {
			return nil, fmt.Errorf("plugin: verb name %q is not a lowercase slug", verb)
		}
		if v.TimeoutSeconds < 0 {
			return nil, fmt.Errorf("plugin: verb %q timeout_seconds must not be negative", verb)
		}
	}
	if len(m.Binaries) == 0 {
		return nil, fmt.Errorf("plugin: manifest %s@%s carries no binaries", m.Name, m.Version)
	}
	if err := checkShapes(raw, "binaries", binaryFields, "url, sha256"); err != nil {
		return nil, err
	}
	for platform, b := range m.Binaries {
		if !platformRe.MatchString(platform) {
			return nil, fmt.Errorf("plugin: binary platform key %q is not goos/goarch", platform)
		}
		if strings.TrimSpace(b.URL) == "" {
			return nil, fmt.Errorf("plugin: binary %q has no url", platform)
		}
		if !sha256Re.MatchString(b.SHA256) {
			return nil, fmt.Errorf("plugin: binary %q sha256 %q is not a 64-hex-digit digest", platform, b.SHA256)
		}
	}
	return &m, nil
}

// checkShapes validates one map-of-maps section (verbs, binaries) against a whitelist.
func checkShapes(raw map[string]any, section string, allowed map[string]bool, allowedList string) error {
	v, ok := raw[section]
	if !ok {
		return nil
	}
	block, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("plugin: field %q must be a mapping", section)
	}
	for name, entry := range block {
		if entry == nil {
			continue // an empty verb entry means all defaults
		}
		inner, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("plugin: %s entry %q must be a mapping of %s", section, name, allowedList)
		}
		if err := checkKeys(section+" entry "+name, inner, allowed, allowedList); err != nil {
			return err
		}
	}
	return nil
}

// checkKeys names the first non-whitelisted key of raw, in sorted order.
func checkKeys(where string, raw map[string]any, allowed map[string]bool, allowedList string) error {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			if where != "" {
				return fmt.Errorf("plugin: %s: unknown field %q; allowed fields are %s", where, k, allowedList)
			}
			return fmt.Errorf("plugin: unknown field %q; allowed fields are %s", k, allowedList)
		}
	}
	return nil
}

// Ref is a parsed "name@version" plugin reference.
type Ref struct {
	// Name is the referenced plugin name.
	Name string
	// Version is the referenced plugin version.
	Version string
}

// String renders the reference back to its declared "name@version" form.
func (r Ref) String() string { return r.Name + "@" + r.Version }

// ParseRef parses "name@version" under the manifest's own name/version shapes.
func ParseRef(ref string) (Ref, error) {
	name, version, ok := strings.Cut(ref, "@")
	if !ok {
		return Ref{}, fmt.Errorf("plugin: ref %q is not name@version", ref)
	}
	if !nameRe.MatchString(name) {
		return Ref{}, fmt.Errorf("plugin: ref name %q is not a lowercase slug", name)
	}
	if !versionRe.MatchString(version) {
		return Ref{}, fmt.Errorf("plugin: ref version %q is not a path-safe token", version)
	}
	return Ref{Name: name, Version: version}, nil
}
