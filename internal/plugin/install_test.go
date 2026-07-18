package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testInstaller returns an Installer rooted in a fresh temp home, pinned to a
// fixed platform so manifests in tests are platform-independent.
func testInstaller(t *testing.T) *Installer {
	t.Helper()
	return &Installer{
		Home:   t.TempDir(),
		Client: http.DefaultClient,
		GOOS:   "linux",
		GOARCH: "amd64",
	}
}

// writeLocalPlugin writes a binary and a manifest referencing it by relative
// path into dir, returning the manifest path. The manifest pins the binary's
// real digest unless digest overrides it.
func writeLocalPlugin(t *testing.T, dir string, binary []byte, digest string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "bin"), binary, 0o755); err != nil { //nolint:gosec // test fixture is executable by design
		t.Fatal(err)
	}
	if digest == "" {
		digest = digestOf(binary)
	}
	doc := fmt.Sprintf(`name: smtp-send
version: "1.0"
kind: tool
verbs:
  send: {}
binaries:
  linux/amd64:
    url: bin
    sha256: %s
`, digest)
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallFromLocalManifest(t *testing.T) {
	inst := testInstaller(t)
	binary := []byte("#!/bin/sh\necho ok\n")
	manifest := writeLocalPlugin(t, t.TempDir(), binary, "")

	got, err := inst.Install(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Ref != (Ref{Name: "smtp-send", Version: "1.0"}) {
		t.Fatalf("installed ref = %v", got.Ref)
	}
	if got.Digest != digestOf(binary) {
		t.Fatalf("installed digest = %s, want %s", got.Digest, digestOf(binary))
	}
	onDisk, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(onDisk) != string(binary) {
		t.Fatal("installed binary differs from source")
	}
	info, err := os.Stat(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary mode = %v, want 0755", info.Mode().Perm())
	}

	// Verify and Resolve agree with the install record.
	verified, err := inst.Verify(got.Ref)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.Digest != got.Digest {
		t.Fatalf("Verify digest = %s, want %s", verified.Digest, got.Digest)
	}
	if _, err := inst.Resolve(got.Ref); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestInstallRefusesChecksumMismatch(t *testing.T) {
	inst := testInstaller(t)
	manifest := writeLocalPlugin(t, t.TempDir(), []byte("real bytes"), testDigest)

	_, err := inst.Install(context.Background(), manifest)
	if err == nil {
		t.Fatal("Install succeeded despite checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install error = %q, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(Dir(inst.Home, Ref{Name: "smtp-send", Version: "1.0"})); !os.IsNotExist(statErr) {
		t.Fatal("refused install left files behind")
	}
}

func TestInstallRefusesServiceKind(t *testing.T) {
	inst := testInstaller(t)
	doc := `name: lightpanda
version: "0.4"
kind: service
binaries:
  linux/amd64:
    url: https://example.com/lightpanda
    sha256: ` + testDigest + "\n"
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := inst.Install(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "only tool plugins") {
		t.Fatalf("Install error = %v, want only-tool refusal", err)
	}
}

func TestInstallRefusesMissingPlatform(t *testing.T) {
	inst := testInstaller(t)
	inst.GOARCH = "arm64"
	manifest := writeLocalPlugin(t, t.TempDir(), []byte("bytes"), "")

	_, err := inst.Install(context.Background(), manifest)
	if err == nil || !strings.Contains(err.Error(), "pins no binary for linux/arm64") {
		t.Fatalf("Install error = %v, want missing-platform refusal", err)
	}
}

func TestInstallFromHTTP(t *testing.T) {
	binary := []byte("http-delivered binary")
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	doc := fmt.Sprintf(`name: smtp-send
version: "2.0"
kind: tool
verbs:
  send: {}
binaries:
  linux/amd64:
    url: %s/bin
    sha256: %s
`, srv.URL, digestOf(binary))
	mux.HandleFunc("/manifest.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(binary)
	})

	inst := testInstaller(t)
	got, err := inst.Install(context.Background(), srv.URL+"/manifest.yaml")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Digest != digestOf(binary) {
		t.Fatalf("digest = %s, want %s", got.Digest, digestOf(binary))
	}
}

func TestURLManifestRefusesRelativeBinary(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	doc := `name: smtp-send
version: "1.0"
kind: tool
verbs:
  send: {}
binaries:
  linux/amd64:
    url: bin
    sha256: ` + testDigest + "\n"
	mux.HandleFunc("/manifest.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	})

	inst := testInstaller(t)
	_, err := inst.Install(context.Background(), srv.URL+"/manifest.yaml")
	if err == nil || !strings.Contains(err.Error(), "must pin http(s) binary URLs") {
		t.Fatalf("Install error = %v, want relative-binary refusal", err)
	}
}

func TestVerifyDetectsDrift(t *testing.T) {
	inst := testInstaller(t)
	manifest := writeLocalPlugin(t, t.TempDir(), []byte("original"), "")
	got, err := inst.Install(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.WriteFile(got.Path, []byte("tampered"), 0o755); err != nil { //nolint:gosec // test fixture is executable by design
		t.Fatal(err)
	}
	if _, err := inst.Verify(got.Ref); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("Verify error = %v, want drift", err)
	}
	if _, err := inst.Resolve(got.Ref); err == nil || !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("Resolve error = %v, want resolve refusal", err)
	}
}

func TestVerifyMissingInstall(t *testing.T) {
	inst := testInstaller(t)
	_, err := inst.Verify(Ref{Name: "ghost", Version: "1.0"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("Verify error = %v, want not-installed", err)
	}
}

func TestListAndRemove(t *testing.T) {
	inst := testInstaller(t)
	manifest := writeLocalPlugin(t, t.TempDir(), []byte("v1"), "")
	got, err := inst.Install(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	list, err := inst.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Ref != got.Ref {
		t.Fatalf("List = %+v, want one entry for %v", list, got.Ref)
	}

	if err := inst.Remove(got.Ref); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	list, err = inst.List()
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List after remove = %+v, want empty", list)
	}
	if _, err := os.Stat(filepath.Join(Root(inst.Home), "smtp-send")); !os.IsNotExist(err) {
		t.Fatal("Remove left an empty name directory behind")
	}

	if err := inst.Remove(got.Ref); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("second Remove error = %v, want not-installed", err)
	}
}
