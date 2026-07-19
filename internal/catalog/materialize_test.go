package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testPack returns a two-file pack for materialize tests.
func testPack() Pack {
	return Pack{IndexEntry: IndexEntry{Name: "t"}, Files: []File{
		{Path: "pipelines/l/a/iris-declare.yaml", Data: []byte("name: a\nrun: [sh, x]\n")},
		{Path: "schemas/s/t/table.yaml", Data: []byte("schema: s\ntable: t\ncolumns:\n  - name: id\n    type: text\n    primary_key: true\n")},
	}}
}

// TestMaterialize proves the write, the existing-path refusal, and the force overwrite.
func TestMaterialize(t *testing.T) {
	root := t.TempDir()
	files, err := Materialize(root, testPack(), false)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("Materialize wrote %v, want 2 files", files)
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			t.Errorf("written file %s missing: %v", f, err)
		}
	}

	if _, err := Materialize(root, testPack(), false); err == nil || !strings.Contains(err.Error(), "existing path") {
		t.Fatalf("re-materialize = %v, want the existing-path refusal", err)
	}

	changed := testPack()
	changed.Files[0].Data = []byte("name: a\nrun: [sh, y]\n")
	if _, err := Materialize(root, changed, true); err != nil {
		t.Fatalf("force materialize: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "pipelines/l/a/iris-declare.yaml")) //nolint:gosec // G304: the test reads back its own TempDir write.
	if !strings.Contains(string(got), "sh, y") {
		t.Error("force did not overwrite the existing file")
	}
}

// TestMaterializeUnsafePaths proves traversal and absolute paths are refused before any write.
func TestMaterializeUnsafePaths(t *testing.T) {
	for _, bad := range []string{"../escape", "/abs", "a/../b", "a//b", "", "a\\b"} {
		p := Pack{IndexEntry: IndexEntry{Name: "evil"}, Files: []File{{Path: bad, Data: []byte("x")}}}
		if _, err := Materialize(t.TempDir(), p, false); err == nil {
			t.Errorf("path %q accepted, want refusal", bad)
		}
	}
}

// TestPreflightRegistry proves the name-collision refusal and the same-pack-only force bypass.
func TestPreflightRegistry(t *testing.T) {
	p := testPack()
	root := t.TempDir()
	if err := PreflightRegistry(root, p, []string{"other"}, false); err != nil {
		t.Errorf("no collision refused: %v", err)
	}
	if err := PreflightRegistry(root, p, []string{"a"}, false); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf("collision = %v, want the already-registered refusal", err)
	}
	// Force against a bare workspace: the registered "a" is someone else's, refuse.
	if err := PreflightRegistry(root, p, []string{"a"}, true); err == nil || !strings.Contains(err.Error(), "does not carry this pack's copy") {
		t.Errorf("foreign force = %v, want the not-this-pack refusal", err)
	}
	// Force after the pack's own copy is on disk: the idempotent reinstall passes.
	if _, err := Materialize(root, p, false); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := PreflightRegistry(root, p, []string{"a"}, true); err != nil {
		t.Errorf("same-pack force reinstall refused: %v", err)
	}
}

// TestPreflightSchemas proves identical tables pass and differing columns refuse.
func TestPreflightSchemas(t *testing.T) {
	p := testPack()
	root := t.TempDir()
	if err := PreflightSchemas(root, p); err != nil {
		t.Errorf("empty workspace refused: %v", err)
	}
	dir := filepath.Join(root, "schemas/s/t")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "table.yaml"), p.Files[1].Data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PreflightSchemas(root, p); err != nil {
		t.Errorf("identical table refused: %v", err)
	}
	drift := []byte("schema: s\ntable: t\ncolumns:\n  - name: id\n    type: bigint\n    primary_key: true\n")
	if err := os.WriteFile(filepath.Join(dir, "table.yaml"), drift, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PreflightSchemas(root, p); err == nil || !strings.Contains(err.Error(), "differing columns") {
		t.Errorf("drifted table = %v, want the differing-columns refusal", err)
	}
}
