package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-engine-cli/internal/config"
	"github.com/MateusAMP2119/iris-engine-cli/internal/daemon"
)

// engineVersion is the engine's own version string reported by `iris engine info`.
// It is a placeholder until the release-stamping build wiring lands; `info` also
// reports the Go runtime version, which is real.
const engineVersion = "0.0.0-dev"

// installResult is the machine-readable outcome of `iris engine install`, the
// payload of its --json data envelope. It names the mode and, in managed mode, the
// directory the managed Postgres was placed under -- never any credential.
type installResult struct {
	Mode  string `json:"mode"`
	PgDir string `json:"pg_dir,omitempty"`
}

// engineInstall is the handler for `iris engine install`: a daemonless lifecycle
// command (it dials no daemon) that performs the managed-Postgres install leg and
// sets up the control socket. In managed mode it downloads and places the pinned,
// checksum-verified Postgres under <workspace>/.iris/pg through the daemon's
// managed-Postgres supervisor; in external mode there is no local instance to
// install. It then prepares the control socket directory. It fails fast (operation
// failed, exit 4) on any install error.
//
// The socket setup runs here for real. The meta bootstrap and engine-key legs are
// orchestrated by daemon.BootstrapEngine (proven at the integration tier); they
// run against the cluster once the daemon's live admin connection is wired, and
// the CLI drives BootstrapEngine at that point.
func (a *app) engineInstall() runE {
	return func(cmd *cobra.Command, _ []string) error {
		settings := a.resolveTarget(cmd)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		mgr := daemon.NewManager(settings, daemon.EmbeddedSupervisor)
		if err := mgr.Install(ctx); err != nil {
			a.logger.Error("engine install failed", "err", err)
			return &fault{
				code:    exitOpFailed,
				codeStr: "install_failed",
				message: fmt.Sprintf("engine install failed: %v", err),
			}
		}
		if err := daemon.PrepareSocketDir(settings); err != nil {
			a.logger.Error("engine install failed", "err", err)
			return &fault{
				code:    exitOpFailed,
				codeStr: "install_failed",
				message: fmt.Sprintf("engine install failed: %v", err),
			}
		}
		return a.emitInstallResult(cmd, settings)
	}
}

// engineInfoResult is the machine-readable payload of `iris engine info`, the
// --json data envelope. It reports the engine and Go version, the control socket
// path, the Postgres mode, the object store path, and the engine key's PUBLIC half
// -- never any private key material or credential. Leadership role, listeners, and
// uptime are reported once their daemon/liveness legs land (E02.5/E02.6).
type engineInfoResult struct {
	Version         string `json:"version"`
	Go              string `json:"go"`
	Socket          string `json:"socket"`
	Mode            string `json:"mode"`
	ObjectsPath     string `json:"objects_path"`
	EngineKeyPublic string `json:"engine_key_public"`
}

// engineInfo is the handler for `iris engine info`: a minimal, real readout of the
// engine's local configuration plus the engine key's public half (specification
// sections 4 and 11). It reads the key through an EngineKeyReader seam (a live meta
// read once wired; unreadable today in production), derives the public half, and
// shows only that -- the private half stays in meta and never reaches an output
// stream. When the key cannot be read the engine is not installed or unreachable,
// reported as operation-failed (exit 4) with a clear message. Role/listeners/uptime
// are deferred seams (E02.5/E02.6).
func (a *app) engineInfo() runE {
	return func(cmd *cobra.Command, _ []string) error {
		settings := a.resolveTarget(cmd)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		mk := a.newKeyReader
		if mk == nil {
			mk = daemon.NewEngineKeyReader
		}
		key, err := mk(settings).ReadEngineKey(ctx)
		if err != nil {
			a.logger.Debug("engine info: engine key unavailable", "err", err)
			return &fault{
				code:    exitOpFailed,
				codeStr: "engine_unavailable",
				message: `iris engine is not installed or not reachable; run "iris engine install" or target a running engine`,
			}
		}

		res := engineInfoResult{
			Version:         engineVersion,
			Go:              runtime.Version(),
			Socket:          settings.Socket,
			Mode:            modeName(settings),
			ObjectsPath:     settings.ObjectsPath,
			EngineKeyPublic: key.PublicBase64(),
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: res})
		}
		fmt.Fprintf(a.out, "engine version:    %s\n", res.Version)
		fmt.Fprintf(a.out, "go version:        %s\n", res.Go)
		fmt.Fprintf(a.out, "control socket:    %s\n", res.Socket)
		fmt.Fprintf(a.out, "postgres mode:     %s\n", res.Mode)
		fmt.Fprintf(a.out, "object store:      %s\n", res.ObjectsPath)
		fmt.Fprintf(a.out, "engine key (pub):  %s\n", res.EngineKeyPublic)
		return nil
	}
}

// uninstallResult is the machine-readable payload of `iris engine uninstall`, the
// --json data envelope: the on-disk paths the teardown removed. It carries no
// secret.
type uninstallResult struct {
	Removed []string `json:"removed"`
}

// engineUninstall is the handler for `iris engine uninstall`: the gated, daemonless,
// local-machine-only engine teardown (specification sections 4 and 12). It refuses
// without --yes (operation failed, exit 4, with guidance), and otherwise removes
// the engine's on-disk state -- the object store under objects_path (artifact bytes
// and archived partitions), the control socket, and the service unit. The meta and
// journal drops are orchestrated by daemon.UninstallEngine (proven at the
// integration tier) and run against the cluster once the daemon's live admin
// connection is wired; the on-disk teardown is real from now.
func (a *app) engineUninstall() runE {
	return func(cmd *cobra.Command, _ []string) error {
		yes, _ := cmd.Flags().GetBool("yes")
		force, _ := cmd.Flags().GetBool("force")
		if !yes && !force {
			return &fault{
				code:    exitOpFailed,
				codeStr: "confirmation_required",
				message: `iris engine uninstall is an irreversible teardown; re-run with --yes to confirm (it drops meta, the journal, the object store, the socket, and the service unit)`,
			}
		}

		settings := a.resolveTarget(cmd)
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		// Refuse while a daemon candidate holds meta (shared meta is never dropped
		// under a live candidate). The predicate is a seam that proceeds by default
		// until the leadership/liveness wiring (E02.5+) fills it.
		held, err := daemon.ProceedWithoutLiveCheck().LiveCandidateHoldsMeta(ctx)
		if err == nil && held {
			err = daemon.ErrLiveCandidate
		}
		if err != nil {
			return &fault{code: exitOpFailed, codeStr: "uninstall_refused", message: err.Error()}
		}

		removed, err := daemon.RemoveEngineArtifacts(settings)
		if err != nil {
			a.logger.Error("engine uninstall failed", "err", err)
			return &fault{
				code:    exitOpFailed,
				codeStr: "uninstall_failed",
				message: fmt.Sprintf("engine uninstall failed: %v", err),
			}
		}
		a.logger.Info("engine uninstall: removed on-disk engine state", "count", len(removed))

		res := uninstallResult{Removed: removed}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: res})
		}
		if len(removed) == 0 {
			fmt.Fprintln(a.out, "engine uninstall: no on-disk engine state to remove")
			return nil
		}
		fmt.Fprintln(a.out, "engine uninstall: removed engine state")
		for _, path := range removed {
			fmt.Fprintf(a.out, "  %s\n", path)
		}
		return nil
	}
}

// modeName names the Postgres mode for `iris engine info`: managed when no admin
// DSN is configured, external otherwise. It never renders the DSN.
func modeName(s config.Settings) string {
	if s.Managed() {
		return "managed"
	}
	return "external"
}

// emitInstallResult renders a successful install: a single JSON data envelope under
// --json, otherwise a human line on stdout. Neither carries the engine-minted
// superuser credential.
func (a *app) emitInstallResult(cmd *cobra.Command, s config.Settings) error {
	res := installResult{Mode: "external"}
	if s.Managed() {
		res.Mode = "managed"
		res.PgDir = daemon.ManagedPGDir(s)
	}

	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		return json.NewEncoder(a.out).Encode(dataEnvelope{Data: res})
	}
	if res.Mode == "managed" {
		fmt.Fprintf(a.out, "engine managed Postgres installed under %s\n", res.PgDir)
	} else {
		fmt.Fprintln(a.out, "external Postgres configured; no local instance installed")
	}
	return nil
}
