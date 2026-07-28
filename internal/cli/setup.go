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
	"strconv"
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

// catalogSetupProbeTimeout bounds the reachability fetch of one catalog index
// during setup. An installer must fail fast on a catalog that black-holes, not
// hang the last step of the ceremony.
const catalogSetupProbeTimeout = 15 * time.Second

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
	c.Flags().String("existing", "", "reuse|restart|wipe when engine state already exists (non-interactive; also IRIS_SETUP_EXISTING)")
	c.Flags().Bool("default", false, "non-interactive preset: local engine, wipe existing state, public catalog")
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

		existingPre, _ := cmd.Flags().GetString("existing")
		if existingPre == "" {
			existingPre = os.Getenv("IRIS_SETUP_EXISTING")
		}
		existingPre = strings.ToLower(strings.TrimSpace(existingPre))

		if def, _ := cmd.Flags().GetBool("default"); def {
			mode, existingPre, catalogsPre = applySetupDefaults(mode, existingPre, catalogsPre)
		}

		p := a.newPainter(false)
		log := newCeremonyLog(a.out)
		done := func(label string) {
			mark := ceremonyCheckMark(p.green("✓"))
			log.line(formatCeremonyLine(label, mark))
		}

		var choice engineSetupChoice
		if phase == "engine" || phase == "all" {
			var err error
			choice, err = a.runEngineSetupPhase(cmd, mode, existingPre, log)
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
func (a *app) runEngineSetupPhase(cmd *cobra.Command, mode, existingPre string, log *ceremonyLog) (engineSetupChoice, error) {
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
		// State from a previous install is surfaced, never silently adopted.
		if handled, herr := a.handleExistingEngine(cmd, existingPre, log); herr != nil {
			return choice, herr
		} else if handled {
			return choice, nil
		}
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

// applySetupDefaults fills the setup answers a --default run leaves unset —
// local engine, wipe existing state, public catalog — so one flag yields a
// clean, prompt-free install. An explicit flag or env answer still wins.
func applySetupDefaults(mode, existing, catalogs string) (string, string, string) {
	if mode == "" {
		mode = "local"
	}
	if existing == "" {
		existing = "wipe"
	}
	if catalogs == "" {
		catalogs = "public"
	}
	return mode, existing, catalogs
}

// existingEngine is what the installer found under the engine home before a
// local setup: a live daemon and/or durable state from a previous install.
type existingEngine struct {
	running bool
	pid     int
	state   bool
}

// detectExistingEngine probes the local daemon and the engine home for state a
// fresh local setup would otherwise silently adopt.
func (a *app) detectExistingEngine(cmd *cobra.Command) existingEngine {
	var e existingEngine
	settings := a.resolveTarget(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	e.running = a.probeDaemon(ctx, settings) == nil
	if data, err := os.ReadFile(daemon.PIDPath(settings)); err == nil { //nolint:gosec // G304: fixed name under the resolved engine home.
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
			e.pid = pid
		}
	}
	home, err := config.Home(os.Getenv)
	if err != nil {
		return e
	}
	for _, d := range []string{"pg", "workspace"} {
		if _, serr := os.Stat(filepath.Join(home, d)); serr == nil {
			e.state = true
			break
		}
	}
	return e
}

// handleExistingEngine surfaces pre-existing local engine state before a fresh
// local install: reuse (default — nothing stopped, nothing touched), restart
// (stop now, keep the data; the catalog phase relaunches on the new binary),
// or wipe (stop and erase the local state for a truly clean install). It
// reports handled=true when the nested engine install should be skipped
// entirely — reuse with a live daemon, whose state is already complete.
func (a *app) handleExistingEngine(cmd *cobra.Command, preselect string, log *ceremonyLog) (bool, error) {
	e := a.detectExistingEngine(cmd)
	if !e.running && !e.state {
		return false, nil
	}
	action, err := selectExistingEngineAction(preselect, e.running, a.out)
	if err != nil {
		return false, &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", err)}
	}
	switch action {
	case existingRestart:
		log.line("  • Existing engine detected · restarting on the new binary (data kept)")
		if e.running {
			// Same shape as `iris uninstall`'s first step: say what is being
			// looked for, stop it, then report what was actually stopped.
			log.line("  • Checking for running processes...")
			if serr := a.runSelfQuiet(cmd, "engine", "stop"); serr != nil {
				return false, serr
			}
			if serr := a.waitEngineStopped(cmd); serr != nil {
				return false, serr
			}
			stopped := "  • Stopped the running engine"
			if e.pid != 0 {
				stopped = fmt.Sprintf("  • Stopped the running engine (pid %d)", e.pid)
			}
			log.line(stopped + " · data preserved")
		}
		return false, nil // the idempotent install completes; the catalog phase starts the engine
	case existingWipe:
		if e.running {
			if serr := a.runSelfQuiet(cmd, "engine", "stop"); serr != nil {
				return false, serr
			}
		}
		if werr := wipeEngineState(a.resolveTarget(cmd)); werr != nil {
			return false, &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %v", werr)}
		}
		log.line("  • Existing engine state erased (clean install)")
		return false, nil
	default: // reuse
		if e.running {
			label := "  • Existing engine detected (running"
			if e.pid != 0 {
				label += fmt.Sprintf(", pid %d", e.pid)
			}
			label += ") · reused, data preserved"
			log.line(label)
			log.line("    it keeps the old binary until: iris engine stop && iris engine start -d")
			return true, nil
		}
		log.line("  • Existing engine data detected · preserved")
		return false, nil
	}
}

// waitEngineStopped blocks until no daemon answers the resolved endpoint. The
// stop returns once the signal is delivered, not once the process is gone, and
// the catalog phase reads a live daemon as "already running" — so a restart
// that skipped this wait could leave the old binary serving and call it done.
func (a *app) waitEngineStopped(cmd *cobra.Command) error {
	settings := a.resolveTarget(cmd)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, cancel := context.WithTimeout(ctx, stopGraceTimeout)
	defer cancel()
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		if a.probeDaemon(deadline, settings) != nil {
			return nil
		}
		select {
		case <-deadline.Done():
			return &fault{
				code: exitOpFailed, codeStr: "stop_failed",
				message: "iris setup: the running engine is still answering after stop; stop it by hand (iris engine stop) and install again",
			}
		case <-t.C:
		}
	}
}

// wipeEngineState erases the engine home's durable state for a clean install —
// Postgres, workspace, logs, cache, config, pidfile, socket — keeping only the
// binary directory.
func wipeEngineState(s config.Settings) error {
	home, err := config.Home(os.Getenv)
	if err != nil {
		return fmt.Errorf("resolve the engine home: %w", err)
	}
	targets := []string{
		filepath.Join(home, "pg"),
		filepath.Join(home, "workspace"),
		filepath.Join(home, "logs"),
		filepath.Join(home, "ps-cache"),
		filepath.Join(home, config.FileName),
		daemon.PIDPath(s),
		s.Socket,
	}
	for _, t := range targets {
		if t == "" {
			continue
		}
		if err := os.RemoveAll(t); err != nil {
			return fmt.Errorf("erase %s: %w", t, err)
		}
	}
	return nil
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
//
// Every chosen catalog is fetched before it is recorded, carrying any tokens
// from IRIS_CATALOG_TOKENS, and a catalog that does not answer fails the phase:
// install.sh exits on that, so a private catalog with no usable credential is a
// loud install failure rather than an engine that comes up healthy and lists no
// packs. Recording an index nobody could read is what made an unreachable
// catalog look like an empty one.
//
// This is the one place a client reaches a catalog URL directly. The daemon owns
// catalog egress everywhere else -- clients name packs, never URLs -- but the
// operator has just typed this URL and the daemon does not start until after
// this phase, so there is no leader to ask.
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
	tokenEntries := splitEnvList(os.Getenv(config.EnvCatalogTokens))
	tokens, err := catalog.ParseHostTokens(tokenEntries)
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: %s: %v", config.EnvCatalogTokens, err)}
	}
	for _, u := range urls {
		if err := a.verifyCatalog(u, tokens); err != nil {
			return err
		}
		log.line("  • Reachable: " + u)
	}
	home, err := config.Home(os.Getenv)
	if err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: resolve the engine home: %v", err)}
	}
	lists := map[string][]string{"catalogs": urls}
	if len(tokenEntries) > 0 {
		// The env var lives for this install only; the daemon reads iris.toml.
		lists["catalog_tokens"] = tokenEntries
	}
	tomlPath := filepath.Join(home, config.FileName)
	if err := config.UpsertTOML(tomlPath, nil, lists); err != nil {
		return &fault{code: exitOpFailed, codeStr: "setup_failed", message: fmt.Sprintf("iris setup: record catalogs: %v", err)}
	}
	done("Catalog configured")
	return nil
}

// verifyCatalog fetches one index, turning a failure into a fault that says what
// to do about it. An auth-shaped refusal is the likely one on a private catalog,
// and the remedy is a token, so the message names how to supply one.
func (a *app) verifyCatalog(indexURL string, tokens catalog.HostTokens) error {
	probe := a.catalogProbe
	if probe == nil {
		probe = fetchCatalogIndex
	}
	if err := probe(indexURL, tokens); err != nil {
		return &fault{
			code:    exitOpFailed,
			codeStr: "setup_failed",
			message: fmt.Sprintf("iris setup: catalog %s did not answer: %v\n"+
				"  A private catalog needs a token. Either authenticate the GitHub CLI (gh auth login)\n"+
				"  and re-run, or set %s='<host>=<token>' before installing.",
				indexURL, err, config.EnvCatalogTokens),
		}
	}
	return nil
}

// fetchCatalogIndex is the production catalogProbe: one bounded index fetch,
// carrying whatever token the host is configured for.
func fetchCatalogIndex(indexURL string, tokens catalog.HostTokens) error {
	ctx, cancel := context.WithTimeout(context.Background(), catalogSetupProbeTimeout)
	defer cancel()
	_, err := catalog.Remote{URL: indexURL, Fetch: tokens.Fetch}.Index(ctx)
	return err
}

// splitEnvList splits a comma-separated environment value, dropping blanks. It
// matches how IRIS_CATALOGS and IRIS_CATALOG_TOKENS are read into settings.
func splitEnvList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
