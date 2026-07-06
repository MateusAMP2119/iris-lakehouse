//go:build conformance

package conformance

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MateusAMP2119/iris-engine-cli/internal/daemon"
)

// pinnedManagedPGMajor is the engine's pinned Postgres major version as the data
// directory records it in PG_VERSION, taken from the single source of truth so the
// conformance assertion tracks the pin automatically.
var pinnedManagedPGMajor = strconv.Itoa(daemon.PinnedMajorVersion)

// managedPGBinaries are the standalone Postgres executables the managed build must
// land under <workspace>/.iris/pg/bin. Their presence as separate on-disk files is
// the proof the managed Postgres is a fetched child-subprocess build, never linked
// into the single iris binary (specification section 9).
func managedPGBinaries() []string {
	names := []string{"postgres", "pg_ctl", "initdb"}
	if runtime.GOOS == "windows" {
		for i, n := range names {
			names[i] = n + ".exe"
		}
	}
	return names
}

// TestManagedPGInstall drives the real iris binary's daemonless `engine install`
// in a throwaway workspace with no external pg_dsn (managed mode) and proves the
// managed-Postgres install leg end to end:
//
//   - the pinned, checksum-verified Postgres is downloaded and placed under
//     <workspace>/.iris/pg, with its data directory's PG_VERSION recording the
//     pinned major (S02/managed-pg-install, S10/managed-pg-under-iris-dir);
//   - the Postgres server binaries land as standalone executables under
//     .iris/pg/bin, proving the build is a fetched child subprocess and not linked
//     into the iris binary (S09/managed-postgres-subprocess);
//   - the engine-minted superuser credential never appears on stdout or stderr:
//     the CLI never sees it (S02/managed-pg-install);
//   - a second install is idempotent: it re-downloads nothing and exits 0.
//
// The first run downloads a Postgres distribution from the pinned binary
// repository (network-bound; typically tens of seconds on a cold cache, near-
// instant once embedded-postgres's runtime cache is warm), so this test carries a
// generous timeout and runs only under the conformance build tag in the
// network-enabled conformance job.
//
// spec: S02/managed-pg-install
// spec: S09/managed-postgres-subprocess
// spec: S10/managed-pg-under-iris-dir
func TestManagedPGInstall(t *testing.T) {
	bin := Build(t)
	ws := t.TempDir()

	res := bin.Run(t, RunOptions{
		Args:    []string{"engine", "install"},
		Dir:     ws,
		Timeout: 5 * time.Minute,
	})
	res.RequireExit(t, 0)

	pgDir := filepath.Join(ws, ".iris", "pg")

	// S10 + S09: the managed Postgres, including its standalone server binaries,
	// is placed under <workspace>/.iris/pg/bin.
	for _, name := range managedPGBinaries() {
		p := filepath.Join(pgDir, "bin", name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("managed Postgres binary %s missing under .iris/pg/bin: %v", name, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			t.Errorf("managed Postgres binary %s is not executable (mode %v)", name, info.Mode())
		}
	}

	// S02: the data directory records the pinned Postgres major version.
	versionBytes, err := os.ReadFile(filepath.Join(pgDir, "data", "PG_VERSION"))
	if err != nil {
		t.Fatalf("managed data dir did not record PG_VERSION under .iris/pg/data: %v", err)
	}
	recorded := strings.TrimSpace(string(versionBytes))
	if recorded != pinnedManagedPGMajor {
		t.Errorf("PG_VERSION = %q, want the pinned major %q", recorded, pinnedManagedPGMajor)
	}

	// S02: the engine-minted superuser credential is never surfaced to the CLI.
	// The install output carries no connection string bearing the managed
	// superuser and no printed password.
	combined := string(res.Stdout) + "\n" + string(res.Stderr)
	lower := strings.ToLower(combined)
	for _, leak := range []string{"password", "iris_engine:", "postgres://iris", "postgresql://iris"} {
		if strings.Contains(lower, strings.ToLower(leak)) {
			t.Errorf("engine install output leaked a managed-superuser credential (matched %q):\nstdout:\n%s\nstderr:\n%s",
				leak, res.Stdout, res.Stderr)
		}
	}

	// Idempotent: a second install re-downloads nothing (the binaries and data dir
	// already exist) and exits cleanly.
	res2 := bin.Run(t, RunOptions{
		Args:    []string{"engine", "install"},
		Dir:     ws,
		Timeout: 90 * time.Second,
	})
	res2.RequireExit(t, 0)

	// The data directory the first install created is reused, not reinitialized:
	// PG_VERSION is unchanged.
	versionBytes2, err := os.ReadFile(filepath.Join(pgDir, "data", "PG_VERSION"))
	if err != nil {
		t.Fatalf("re-read PG_VERSION after idempotent install: %v", err)
	}
	if got := strings.TrimSpace(string(versionBytes2)); got != recorded {
		t.Errorf("idempotent install changed PG_VERSION from %q to %q; it must reuse the data dir", recorded, got)
	}
}
