package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// PipelineNames returns the pack's declared pipeline names, sorted.
func PipelineNames(p Pack) ([]string, error) {
	var names []string
	for _, f := range p.Files {
		if path.Base(f.Path) != declFile {
			continue
		}
		decl, err := declare.ParseDeclaration(f.Data)
		if err != nil {
			return nil, fmt.Errorf("catalog: pack %q: parse %s: %w", p.Name, f.Path, err)
		}
		if decl.Kind == declare.KindPipeline {
			names = append(names, decl.Pipeline.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// PreflightRegistry refuses when a pack pipeline name is already registered. Force
// allows only the same-pack reinstall: the workspace must already carry this pack's
// declaration at the same path, so force can never repoint an unrelated pipeline.
func PreflightRegistry(root string, p Pack, registered []string, force bool) error {
	members, _, err := indexPack(p)
	if err != nil {
		return err
	}
	taken := make(map[string]bool, len(registered))
	for _, r := range registered {
		taken[r] = true
	}
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !taken[n] {
			continue
		}
		if !force {
			return fmt.Errorf("catalog: install %q refused: pipeline %q is already registered (a pipeline belongs to exactly one lane); pass force to reinstall", p.Name, n)
		}
		if !workspaceCarries(root, members[n]) {
			return fmt.Errorf("catalog: install %q refused: pipeline %q is registered but the workspace does not carry this pack's copy at %s; force reinstalls only the pack's own pipelines", p.Name, n, members[n].path)
		}
	}
	return nil
}

// workspaceCarries reports whether the workspace already holds the member's declaration at the pack's own path, naming the same pipeline.
func workspaceCarries(root string, m *packMember) bool {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.path))) //nolint:gosec // G304: pack paths pass safeRel and root is the leader's own workspace.
	if err != nil {
		return false
	}
	decl, err := declare.ParseDeclaration(data)
	if err != nil || decl.Kind != declare.KindPipeline {
		return false
	}
	return decl.Pipeline.Name == m.name
}

// PreflightSchemas refuses when a pack table.yaml differs from an existing declared copy, before any write.
func PreflightSchemas(root string, p Pack) error {
	for _, f := range p.Files {
		if !strings.HasPrefix(f.Path, "schemas/") || path.Base(f.Path) != "table.yaml" {
			continue
		}
		existing, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.Path))) //nolint:gosec // G304: pack paths pass safeRel and root is the leader's own workspace.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("catalog: install %q: read %s: %w", p.Name, f.Path, err)
		}
		want, err := declare.ParseTable(f.Data)
		if err != nil {
			return fmt.Errorf("catalog: pack %q: parse %s: %w", p.Name, f.Path, err)
		}
		got, err := declare.ParseTable(existing)
		if err != nil {
			return fmt.Errorf("catalog: install %q refused: existing %s does not parse: %v", p.Name, f.Path, err)
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("catalog: install %q refused: declared table %s.%s already exists with differing columns", p.Name, want.Schema, want.Table)
		}
	}
	return nil
}
