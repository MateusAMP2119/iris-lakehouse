package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
	"github.com/MateusAMP2119/iris-lakehouse/internal/daemon"
)

// setupEngineChoiceName is the short-lived marker install.sh's two setup phases
// share under the engine home: engine phase writes it, catalog phase reads and
// removes it. Not user-facing config.
const setupEngineChoiceName = ".setup-engine-choice"

// setupCmd builds `iris setup`: post-install engine and/or catalog configuration.
// install.sh runs --phase engine then --phase catalog so the ceremony can show
// [3/4] and [4/4] as separate steps. Standalone `iris setup` runs both (all).
func (a *app) setupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "setup",
		Short: "Post-install engine and catalog setup",
		Args:  cobra.NoArgs,
		RunE:  a.setupRun(),
	}
	c.Flags().String("mode", "", "local|remote|skip (non-interactive; also IRIS_ENGINE_SETUP)")
	c.Flags().String("catalogs", "", "public|skip|<index-url>[,url…] (non-interactive; also IRIS_SETUP_CATALOGS)")
	c.Flags().String("phase", "all", "engine|catalog|all (install.sh uses engine then catalog)")
	return daemonless(c)
}

func (a *app) setupRun() runE {
	return func(cmd *cobra.Command, _ []string) error {
		jsonMode, _ := cmd.Flags().GetBool("json")
		if jsonMode {
			return a.usage("iris setup is interactive; do not pass --json")
		}
		phase, _ := cmd.Flags().GetString("phase")
		phase = strings.ToLower(strings.TrimSpace(phase))
		if phase == "" {
			phase = "all"
		}
		switch phase {
		case "engine", "catalog", "all":
		default:
			return a.usage("iris setup --phase must be engine, catalog, or all")
		}

		mode, _ := cmd.Flags().GetString("mode")
		if mode == "" {
			mode = os.Getenv("IRIS_ENGINE_SETUP")
		}
		mode = strings.ToLower(strings.TrimSpace(mode))

		catalogsPre, _ := cmd.Flags().GetString("catalogs")
		if catalogsPre == "" {
			catalogsPre = os.Getenv("IRIS_SETUP_CATALOGS")
		}
		catalogsPre = strings.TrimSpace(catalogsPre)

		p := a.newPainter(false)
		log := newCeremonyLog(a.out)
		done := func(label string) {
			mark := ceremonyCheckMark(p.green("✓"))
			log.line(formatCeremonyLine(label, mark))
		}

		var choice engineSetupChoice
		if phase == "engine" || phase == "all" {
			var err error
			choice, err = a.runEngineSetupPhase(cmd, mode, log)
			if err != nil {
				return err
			}
		}
		if phase == "catalog" || phase == "all" {
			if phase == "catalog" {
				choice = a.loadEngineSetupChoice()
			}
			if err := a.runCatalogSetupPhase(cmd, choice, catalogsPre, log, done); err != nil {
				return err
			}
		}
		if phase == "all" || phase == "catalog" {
			maybeReviewCeremony(a.out, log.content())
		}
		return nil
	}
}

// runEngineSetupPhase handles the engine menu and local install / remote connect.
// Local mode installs only — start waits for the catalog phase so iris.toml
// catalogs are in place before the daemon boots (no hot-reload).
func (a *app) runEngineSetupPhase(cmd *cobra.Command, mode string, log *ceremonyLog) (engineSetupChoice, error) {
	choice, err := selectEngineSetup(mode, a.out)
	if err != nil {
		return 0, &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", err)}
	}
	if err := a.saveEngineSetupChoice(choice); err != nil {
		return 0, &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", err)}
	}

	switch choice {
	case setupLocal:
		log.line("  • Selected: Local mode")
		// Real progress: the nested install's stage lines (parsed live off its
		// stderr) drive the bar — placing Postgres, starting it, privileges,
		// meta database, schema, journal, turn positions, socket, engine key.
		stages := make(chan struct{}, 16)
		if err := runProgressStaged(a.out, "• Installing engine", engineInstallStageCount, stages, func() error {
			return a.runSelfQuietStaged(cmd, stages, "engine install: ", "engine", "install")
		}); err != nil {
			return choice, err
		}
		return choice, nil
	case setupRemote:
		log.line("  • Selected: Remote mode")
		host, token, perr := promptRemoteEndpoint(a.out)
		if perr != nil {
			return choice, &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", perr)}
		}
		host = strings.TrimSpace(host)
		if host == "" {
			log.line("  • No endpoint given. Remote mode: 'iris engine connect <host>'.")
			return choice, nil
		}
		args := []string{"engine", "connect", host}
		if strings.TrimSpace(token) != "" {
			args = append(args, "--token", token)
		}
		return choice, a.runSelf(cmd, args...)
	default:
		log.line("  • Selected: Skip for now")
		log.line("  • No engine configured. Local mode: 'iris engine install && iris engine start -d';")
		log.line("    remote mode: 'iris engine connect <host>'.")
		return choice, nil
	}
}

// runCatalogSetupPhase is the [4/4] Catalog step: pick a pack source, record it,
// then start a local engine so the daemon boots with catalogs already set.
func (a *app) runCatalogSetupPhase(cmd *cobra.Command, choice engineSetupChoice, catalogsPre string, log *ceremonyLog, done func(string)) error {
	defer a.clearEngineSetupChoice()

	if choice == setupRemote {
		log.line("  • Packs come from the remote engine's catalogs")
		return nil
	}

	if err := a.setupCatalogs(catalogsPre, log, done); err != nil {
		return err
	}

	// Local install left the engine stopped so catalogs land before first start.
	if choice == setupLocal {
		return a.startEngineIfNeeded(cmd, log, done)
	}
	return nil
}

// startEngineIfNeeded starts -d when no daemon answers; if one is already up,
// records that and leaves it alone (operator may need a restart to pick up a
// newly written catalogs list — rare on a fresh install path).
func (a *app) startEngineIfNeeded(cmd *cobra.Command, log *ceremonyLog, done func(string)) error {
	settings := a.resolveTarget(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if a.probeDaemon(ctx, settings) == nil {
		done("Engine already running")
		return nil
	}
	// Real progress: the detached daemon's observable milestones — its pidfile
	// appearing, then its control socket accepting a dial — drive the bar.
	stages := make(chan struct{}, 8)
	stop := make(chan struct{})
	go watchStartMilestones(stop, settings, stages)
	err := runProgressStaged(a.out, "• Starting engine", startMilestoneCount, stages, func() error {
		return a.runSelfQuiet(cmd, "engine", "start", "-d")
	})
	close(stop)
	if err != nil {
		return err
	}
	done("Engine started")
	return nil
}

// startMilestoneCount is the number of observable milestones a detached engine
// start passes through: pidfile written, control socket reachable.
const startMilestoneCount = 2

// watchStartMilestones polls for the detached daemon's real start milestones
// and reports each once on stages. It stops when both have fired or stop
// closes (the start returned, successfully or not).
func watchStartMilestones(stop <-chan struct{}, s config.Settings, stages chan<- struct{}) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	sentPID, sentSocket := false, false
	for !sentPID || !sentSocket {
		select {
		case <-stop:
			return
		case <-t.C:
			if !sentPID {
				if _, err := os.Stat(daemon.PIDPath(s)); err == nil {
					sentPID = true
					stages <- struct{}{}
				}
			}
			if !sentSocket {
				if conn, err := net.Dial("unix", s.Socket); err == nil {
					_ = conn.Close()
					sentSocket = true
					stages <- struct{}{}
				}
			}
		}
	}
}

// setupCatalogs runs the catalog menu (or preselect) and records the chosen
// index URLs in the engine home iris.toml. Skip leaves the file untouched.
func (a *app) setupCatalogs(preselect string, log *ceremonyLog, done func(string)) error {
	choice, urls, err := selectCatalogSetup(preselect, a.out)
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", err)}
	}
	switch choice {
	case catalogSetupSkip:
		log.line("  • Catalog: skipped")
		return nil
	case catalogSetupPublic:
		urls = []string{catalog.PublicCatalogURL}
		log.line("  • Selected: Public catalog")
	case catalogSetupCustom:
		log.line("  • Selected: Custom catalog")
	}
	home, err := config.Home(os.Getenv)
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: resolve the engine home: %v", err)}
	}
	tomlPath := filepath.Join(home, config.FileName)
	if err := config.UpsertTOML(tomlPath, nil, map[string][]string{"catalogs": urls}); err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: record catalogs: %v", err)}
	}
	done("Catalog configured")
	return nil
}

func (a *app) saveEngineSetupChoice(choice engineSetupChoice) error {
	home, err := config.Home(os.Getenv)
	if err != nil {
		return fmt.Errorf("resolve the engine home: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create engine home: %w", err)
	}
	var s string
	switch choice {
	case setupLocal:
		s = "local"
	case setupRemote:
		s = "remote"
	default:
		s = "skip"
	}
	path := filepath.Join(home, setupEngineChoiceName)
	if err := os.WriteFile(path, []byte(s+"\n"), 0o600); err != nil {
		return fmt.Errorf("record setup choice: %w", err)
	}
	return nil
}

func (a *app) loadEngineSetupChoice() engineSetupChoice {
	home, err := config.Home(os.Getenv)
	if err != nil {
		return setupSkip
	}
	data, err := os.ReadFile(filepath.Join(home, setupEngineChoiceName)) //nolint:gosec // G304: fixed name under the resolved engine home.
	if err != nil {
		// No marker: prefer remote when iris.toml already points at a host.
		settings := a.resolveTarget(nil)
		if strings.TrimSpace(settings.Host) != "" {
			return setupRemote
		}
		return setupSkip
	}
	switch strings.TrimSpace(string(data)) {
	case "local":
		return setupLocal
	case "remote":
		return setupRemote
	default:
		return setupSkip
	}
}

func (a *app) clearEngineSetupChoice() {
	home, err := config.Home(os.Getenv)
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(home, setupEngineChoiceName))
}

// runSelf re-invokes the current iris binary with args (same argv0), inheriting
// stdio so nested commands render normally.
func (a *app) runSelf(_ *cobra.Command, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: resolve self: %v", err)}
	}
	c := exec.Command(exe, args...)
	c.Stdout = a.out
	c.Stderr = a.errOut
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris %s: %v", strings.Join(args, " "), err)}
	}
	return nil
}

// engineInstallStageCount is how many "engine install: …" stage lines a
// managed-mode install emits (daemon.InstallEngine): placing Postgres,
// starting Postgres, privileges verified, meta database created-or-exists,
// meta schema, data journal, turn positions, control socket, engine key.
const engineInstallStageCount = 9

// stageScanWriter tees a subprocess stream and reports each line carrying the
// stage marker with one non-blocking send — the live progress feed for the
// setup ceremony bar.
type stageScanWriter struct {
	buf    bytes.Buffer
	marker string
	stages chan<- struct{}
}

func (w *stageScanWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Partial line: keep it buffered for the next Write.
			w.buf.WriteString(line)
			break
		}
		if strings.Contains(line, w.marker) {
			select {
			case w.stages <- struct{}{}:
			default:
			}
		}
	}
	return len(p), nil
}

// runSelfQuietStaged is runSelfQuiet with the child's stderr additionally
// scanned for marker lines, each reported on stages as one completed step.
func (a *app) runSelfQuietStaged(_ *cobra.Command, stages chan<- struct{}, marker string, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: resolve self: %v", err)}
	}
	var out, errb bytes.Buffer
	c := exec.Command(exe, args...)
	c.Stdout = &out
	c.Stderr = io.MultiWriter(&errb, &stageScanWriter{marker: marker, stages: stages})
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		if a.out != nil {
			fmt.Fprint(a.out, out.String())
		}
		if a.errOut != nil {
			fmt.Fprint(a.errOut, errb.String())
		}
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris %s: %v", strings.Join(args, " "), err)}
	}
	return nil
}

// runSelfQuiet is runSelf with stdout/stderr captured so nested lifecycle
// commands don't break the install ceremony grid. On failure the captured
// streams are replayed, then the error is returned.
func (a *app) runSelfQuiet(_ *cobra.Command, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: resolve self: %v", err)}
	}
	var out, errb bytes.Buffer
	c := exec.Command(exe, args...)
	c.Stdout = &out
	c.Stderr = &errb
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		if a.out != nil {
			fmt.Fprint(a.out, out.String())
		}
		if a.errOut != nil {
			fmt.Fprint(a.errOut, errb.String())
		}
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris %s: %v", strings.Join(args, " "), err)}
	}
	return nil
}
