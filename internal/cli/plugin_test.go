package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writePluginFixture writes a plugin binary and a manifest pinning its real
// sha256 into a fresh directory, returning the manifest path.
func writePluginFixture(t *testing.T, name, version string, binary []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin"), binary, 0o755); err != nil { //nolint:gosec // test fixture is executable by design
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	doc := fmt.Sprintf(`name: %s
version: %q
kind: tool
verbs:
  send: {}
binaries:
  %s/%s:
    url: bin
    sha256: %s
`, name, version, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPluginLifecycle proves the `iris plugin` verbs against a real engine
// home: install from a local manifest (sha256 verified), list and verify see
// the installed version, remove deletes it, and a second remove is
// operation-failed.
func TestPluginLifecycle(t *testing.T) {
	clearTargetEnv(t)
	manifest := writePluginFixture(t, "smtp-send", "1.0", []byte("plugin bytes"))

	var out, errb bytes.Buffer
	if code := newApp(&out, &errb).run([]string{"plugin", "install", manifest}); code != exitOK {
		t.Fatalf("install exit = %d\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "installed smtp-send@1.0") {
		t.Errorf("install output missing outcome line:\n%s", out.String())
	}

	out.Reset()
	if code := newApp(&out, &errb).run([]string{"plugin", "list", "--json"}); code != exitOK {
		t.Fatalf("list exit = %d\nstderr: %s", code, errb.String())
	}
	var envelope struct {
		Data []struct {
			Name    string   `json:"name"`
			Version string   `json:"version"`
			Kind    string   `json:"kind"`
			Digest  string   `json:"digest"`
			Verbs   []string `json:"verbs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("list --json is not a JSON envelope: %v\n%s", err, out.String())
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Name != "smtp-send" || envelope.Data[0].Kind != "tool" {
		t.Fatalf("list --json data = %+v, want one smtp-send tool", envelope.Data)
	}
	if len(envelope.Data[0].Verbs) != 1 || envelope.Data[0].Verbs[0] != "send" {
		t.Errorf("list --json verbs = %v, want [send]", envelope.Data[0].Verbs)
	}

	out.Reset()
	if code := newApp(&out, &errb).run([]string{"plugin", "verify", "smtp-send@1.0"}); code != exitOK {
		t.Fatalf("verify exit = %d\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "smtp-send@1.0 OK") {
		t.Errorf("verify output missing OK line:\n%s", out.String())
	}

	out.Reset()
	if code := newApp(&out, &errb).run([]string{"plugin", "remove", "smtp-send@1.0"}); code != exitOK {
		t.Fatalf("remove exit = %d\nstderr: %s", code, errb.String())
	}
	errb.Reset()
	if code := newApp(&out, &errb).run([]string{"plugin", "remove", "smtp-send@1.0"}); code != exitOpFailed {
		t.Fatalf("second remove exit = %d, want %d\nstderr: %s", code, exitOpFailed, errb.String())
	}
	if !strings.Contains(errb.String(), "not installed") {
		t.Errorf("second remove missing not-installed message:\n%s", errb.String())
	}
}

// TestPluginInstallRefusesMismatch proves a manifest whose sha256 does not
// match the fetched binary refuses the install with exit 4 and a checksum
// message, leaving nothing under the engine home.
func TestPluginInstallRefusesMismatch(t *testing.T) {
	home := clearTargetEnv(t)
	manifest := writePluginFixture(t, "smtp-send", "1.0", []byte("real"))
	// Corrupt the pinned digest.
	data, err := os.ReadFile(manifest) //nolint:gosec // G304: a t.TempDir fixture path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("other bytes"))
	idx := bytes.LastIndexByte(data, ':')
	data = append(data[:idx], []byte(": "+hex.EncodeToString(sum[:])+"\n")...)
	if err := os.WriteFile(manifest, data, 0o644); err != nil { //nolint:gosec // G703: a t.TempDir fixture path
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := newApp(&out, &errb).run([]string{"plugin", "install", manifest}); code != exitOpFailed {
		t.Fatalf("install exit = %d, want %d\nstderr: %s", code, exitOpFailed, errb.String())
	}
	if !strings.Contains(errb.String(), "checksum mismatch") {
		t.Errorf("refusal missing checksum message:\n%s", errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "plugins", "smtp-send")); !os.IsNotExist(err) {
		t.Error("refused install left files under the engine home")
	}
}

// TestPluginBadRefIsUsage proves a malformed name@version is a usage error
// (exit 2), not an operation failure.
func TestPluginBadRefIsUsage(t *testing.T) {
	clearTargetEnv(t)
	var out, errb bytes.Buffer
	if code := newApp(&out, &errb).run([]string{"plugin", "verify", "not-a-ref"}); code != exitUsage {
		t.Fatalf("verify exit = %d, want %d\nstderr: %s", code, exitUsage, errb.String())
	}
}
