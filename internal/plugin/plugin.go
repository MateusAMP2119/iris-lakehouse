// Package plugin models Iris plugins: external capabilities (a mail sender, a
// headless browser) declared in iris-declare.yaml, installed as digest-pinned
// binaries under the engine home, and invoked mid-run through the turn
// protocol. It is a leaf package with no Iris dependencies of its own: the
// manifest parse core is pure over bytes, and the installer owns the
// ~/.iris/plugins/<name>/<version>/ layout plus the sha256 verification that
// makes every installed binary attributable to its manifest.
//
// Two plugin kinds share the manifest format: a tool is a run-to-completion
// subprocess the engine execs per call; a service is a long-running sidecar the
// daemon supervises. Only tools are installable today; the manifest parses both
// so the service epic has a model to build on.
package plugin

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// ManifestFile is the canonical filename of the manifest copy kept beside an
// installed plugin binary, the record verify and resolve re-check against.
const ManifestFile = "manifest.yaml"

// DefaultVerbTimeout bounds a verb call when its manifest entry declares no
// timeout of its own. The engine always replies to a call; this is the ceiling
// after which a hung tool is killed and the caller gets an err reply.
const DefaultVerbTimeout = 60 * time.Second

// Kind discriminates the two plugin kinds sharing the manifest format.
type Kind string

// The plugin kinds.
const (
	// KindTool is a run-to-completion subprocess the engine execs per call.
	KindTool Kind = "tool"
	// KindService is a long-running sidecar the daemon supervises. Parsed but
	// not yet installable; the service epic owns its lifecycle.
	KindService Kind = "service"
)

// Verb is one callable verb a plugin exposes. Verbs are the only surface a
// pipeline can reach: high-level operations with JSON arguments and results,
// never the plugin's raw wire protocol.
type Verb struct {
	// Timeout bounds one call to this verb, as a Go duration string
	// ("30s", "2m"). Empty means DefaultVerbTimeout.
	Timeout string `yaml:"timeout"`
}

// Duration returns the verb's timeout, or DefaultVerbTimeout when the manifest
// declares none. Validate has already rejected unparseable values.
func (v Verb) Duration() time.Duration {
	if v.Timeout == "" {
		return DefaultVerbTimeout
	}
	d, err := time.ParseDuration(v.Timeout)
	if err != nil || d <= 0 {
		return DefaultVerbTimeout
	}
	return d
}

// Binary is one per-platform binary entry: where to fetch it and the sha256
// that pins it. The digest is the trust anchor; install refuses a mismatch.
type Binary struct {
	// URL locates the binary: an http(s) URL, or a local path (absolute, or
	// relative to the manifest file) for manifests loaded from disk.
	URL string `yaml:"url"`
	// SHA256 is the hex digest the fetched bytes must hash to.
	SHA256 string `yaml:"sha256"`
}

// Manifest is a parsed plugin manifest: identity, kind, the verb surface, and
// the per-platform pinned binaries.
type Manifest struct {
	// Name is the plugin name; required, a lowercase dashed identifier.
	Name string `yaml:"name"`
	// Version is the plugin version; required, path-safe.
	Version string `yaml:"version"`
	// Kind is tool or service; required.
	Kind Kind `yaml:"kind"`
	// Verbs is the callable surface, keyed by verb name; a tool needs at least one.
	Verbs map[string]Verb `yaml:"verbs"`
	// Binaries is the per-platform binary map, keyed "goos/goarch".
	Binaries map[string]Binary `yaml:"binaries"`
}

// Ref names one plugin at one version, the name@version form used by
// iris plugin commands and the ref field of a plugins declaration.
type Ref struct {
	// Name is the plugin name.
	Name string
	// Version is the plugin version.
	Version string
}

// String renders the ref in its canonical name@version form.
func (r Ref) String() string { return r.Name + "@" + r.Version }

// Ref returns the manifest's identity as a Ref.
func (m *Manifest) Ref() Ref { return Ref{Name: m.Name, Version: m.Version} }

// nameRE is the plugin-name shape: lowercase, dash-separated, leading letter.
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// versionRE is the version shape: path-safe, no separators or whitespace.
var versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// verbRE is the verb-name shape: lowercase, underscore- or dash-separated.
var verbRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// platformRE is the binaries key shape: "goos/goarch".
var platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

// sha256RE is a hex-encoded sha256 digest.
var sha256RE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ParseRef parses the name@version form into a Ref, validating both halves
// against the manifest shapes.
func ParseRef(s string) (Ref, error) {
	name, version, ok := strings.Cut(s, "@")
	if !ok {
		return Ref{}, fmt.Errorf("plugin: ref %q is not name@version", s)
	}
	if !nameRE.MatchString(name) {
		return Ref{}, fmt.Errorf("plugin: ref %q has an invalid name; a name is lowercase letters, digits and dashes", s)
	}
	if !versionRE.MatchString(version) {
		return Ref{}, fmt.Errorf("plugin: ref %q has an invalid version", s)
	}
	return Ref{Name: name, Version: version}, nil
}

// ParseManifest parses and validates a plugin manifest document. Unknown fields
// are rejected: the manifest is the trust record for what a plugin is allowed
// to be, so it admits nothing it does not model.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.UnmarshalWithOptions(data, &m, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("plugin: parse manifest: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// validate enforces the manifest rules: identity shapes, a known kind, a
// non-empty verb surface for a tool, and pinned well-formed binaries.
func (m *Manifest) validate() error {
	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("plugin: manifest name %q is invalid; a name is lowercase letters, digits and dashes", m.Name)
	}
	if !versionRE.MatchString(m.Version) {
		return fmt.Errorf("plugin: manifest version %q is invalid; a version is letters, digits, dots, dashes and underscores", m.Version)
	}
	switch m.Kind {
	case KindTool, KindService:
	case "":
		return fmt.Errorf("plugin: manifest %s is missing required field %q (tool or service)", m.Ref(), "kind")
	default:
		return fmt.Errorf("plugin: manifest %s has unknown kind %q; a plugin is a tool or a service", m.Ref(), m.Kind)
	}
	if m.Kind == KindTool && len(m.Verbs) == 0 {
		return fmt.Errorf("plugin: manifest %s declares no verbs; a tool's verbs are its only callable surface", m.Ref())
	}
	for _, name := range sortedKeys(m.Verbs) {
		v := m.Verbs[name]
		if !verbRE.MatchString(name) {
			return fmt.Errorf("plugin: manifest %s verb %q is invalid; a verb is lowercase letters, digits, underscores and dashes", m.Ref(), name)
		}
		if v.Timeout != "" {
			d, err := time.ParseDuration(v.Timeout)
			if err != nil {
				return fmt.Errorf("plugin: manifest %s verb %q timeout %q is not a duration: %w", m.Ref(), name, v.Timeout, err)
			}
			if d <= 0 {
				return fmt.Errorf("plugin: manifest %s verb %q timeout %q must be positive", m.Ref(), name, v.Timeout)
			}
		}
	}
	if len(m.Binaries) == 0 {
		return fmt.Errorf("plugin: manifest %s pins no binaries; at least one goos/goarch entry is required", m.Ref())
	}
	for _, platform := range sortedKeys(m.Binaries) {
		b := m.Binaries[platform]
		if !platformRE.MatchString(platform) {
			return fmt.Errorf("plugin: manifest %s binaries key %q is not goos/goarch", m.Ref(), platform)
		}
		if strings.TrimSpace(b.URL) == "" {
			return fmt.Errorf("plugin: manifest %s binary %s has no url", m.Ref(), platform)
		}
		if !sha256RE.MatchString(b.SHA256) {
			return fmt.Errorf("plugin: manifest %s binary %s sha256 %q is not a hex sha256 digest", m.Ref(), platform, b.SHA256)
		}
	}
	return nil
}

// VerbNames returns the manifest's verb names in sorted order, for listings and
// error messages.
func (m *Manifest) VerbNames() []string { return sortedKeys(m.Verbs) }

// Root is the plugins directory under the engine home:
// <home>/plugins.
func Root(home string) string { return filepath.Join(home, "plugins") }

// Dir is one installed plugin version's directory:
// <home>/plugins/<name>/<version>.
func Dir(home string, r Ref) string {
	return filepath.Join(Root(home), r.Name, r.Version)
}

// BinaryPath is the installed binary inside Dir; the binary is named after the
// plugin.
func BinaryPath(home string, r Ref) string {
	return filepath.Join(Dir(home, r), r.Name)
}

// manifestPath is the installed manifest copy inside Dir.
func manifestPath(home string, r Ref) string {
	return filepath.Join(Dir(home, r), ManifestFile)
}

// sortedKeys returns m's keys in sorted order, for deterministic walks and
// error messages.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
