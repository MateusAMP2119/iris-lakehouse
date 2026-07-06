package cli

import "github.com/spf13/cobra"

// runE is the signature of a cobra command handler.
type runE = func(cmd *cobra.Command, args []string) error

// newRootCommand builds the iris command tree: the root, its global persistent
// flags, and the noun-verb subcommands of specification section 8. The tree is
// built fresh per invocation from a constructor (no package globals, no init),
// so it closes over the invocation's app for output and logging.
func (a *app) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "iris",
		Short:         "Iris - provenance-first data engine and pipeline orchestrator",
		SilenceErrors: true, // errors are rendered by renderError, not cobra
		SilenceUsage:  true, // usage never pollutes stdout under --json
	}
	root.CompletionOptions.DisableDefaultCmd = true

	// Global flags, on every command by inheritance (specification section 8).
	pf := root.PersistentFlags()
	pf.Bool("json", false, "emit a single JSON document on stdout instead of human-readable output")
	pf.String("socket", "", "path to the engine's Unix control socket")
	pf.String("host", "", "address of a remote engine reached over TCP")
	pf.String("token", "", "PAT presented to a remote engine over TCP")

	root.AddCommand(
		a.declareCmd(),
		a.pipelineCmd(),
		a.runCmd(),
		a.dataCmd(),
		a.workloadCmd(),
		a.engineCmd(),
		a.deadletterCmd(),
		a.endpointCmd(),
		a.patCmd(),
	)
	return root
}

// daemonStub is the handler of a command that must reach a running daemon: with
// none reachable it reports no-daemon (exit 3) with start guidance.
func (a *app) daemonStub(op string) runE {
	return func(_ *cobra.Command, _ []string) error { return a.noDaemon(op) }
}

// localStub is the handler of a local-lifecycle command that does not dial a
// daemon and is not wired yet: it reports not-implemented (exit 4).
func (a *app) localStub(what string) runE {
	return func(_ *cobra.Command, _ []string) error { return a.notImplemented(what) }
}

// addConfirmFlags registers the --yes/--force flags of a destructive command
// (--yes honors soft blocks, --force overrides).
func addConfirmFlags(c *cobra.Command) {
	c.Flags().Bool("yes", false, "proceed past soft blocks without prompting")
	c.Flags().Bool("force", false, "override blocks, including hard ones")
}

// addScopeFlags registers the --pipeline/--all worklist scope of a dead-letter
// replay or drain.
func addScopeFlags(c *cobra.Command) {
	c.Flags().String("pipeline", "", "scope to one pipeline's entries")
	c.Flags().Bool("all", false, "scope to every outstanding entry")
}

// declareCmd builds `iris declare`: apply and destroy one declaration file. A
// bare `iris declare` is a usage error by design (no --all).
func (a *app) declareCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "declare",
		Short: "Register and tear down the workload graph, one declaration file per invocation",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return a.usage("declare requires a verb: apply <path> or destroy <path>")
		},
	}

	apply := &cobra.Command{
		Use:   "apply <path>",
		Short: "Register and apply one iris-declare.yaml (pipeline or composer)",
		Args:  cobra.ExactArgs(1),
		RunE:  a.daemonStub("declare apply"),
	}
	apply.Flags().Bool("dry-run", false, "report what would change without touching anything")

	destroy := &cobra.Command{
		Use:   "destroy <path>",
		Short: "Tear down one declaration: its pipeline, role, grants, and un-promoted data",
		Args:  cobra.ExactArgs(1),
		RunE:  a.daemonStub("declare destroy"),
	}
	destroy.Flags().Bool("dry-run", false, "report what would be torn down without touching anything")
	addConfirmFlags(destroy)

	c.AddCommand(apply, destroy)
	return c
}

// pipelineCmd builds `iris pipeline`: the single-unit lifecycle and reads.
func (a *app) pipelineCmd() *cobra.Command {
	c := &cobra.Command{Use: "pipeline", Short: "Operate on a single pipeline"}

	build := &cobra.Command{
		Use: "build <name>", Short: "Build source into the self-contained binary, recording its content hash",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("pipeline build"),
	}
	promote := &cobra.Command{
		Use: "promote <name>", Short: "Mark the pipeline's data permanent (gated on built)",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("pipeline promote"),
	}
	run := &cobra.Command{
		Use: "run <name>", Short: "Trigger a manual one-off run",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("pipeline run"),
	}
	list := &cobra.Command{
		Use: "list", Short: "List pipelines with a queued or running run",
		Args: cobra.NoArgs, RunE: a.daemonStub("pipeline list"),
	}
	list.Flags().Bool("all", false, "list every pipeline, not only active ones")
	show := &cobra.Command{
		Use: "show <name>", Short: "Show a pipeline's resolved declaration, role, grants, recent runs, and gate ledger",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("pipeline show"),
	}

	c.AddCommand(build, promote, run, list, show)
	return c
}

// runCmd builds `iris run`: execution-record reads and cancel.
func (a *app) runCmd() *cobra.Command {
	c := &cobra.Command{Use: "run", Short: "Inspect and control execution records"}

	list := &cobra.Command{
		Use: "list", Short: "List run history",
		Args: cobra.NoArgs, RunE: a.daemonStub("run list"),
	}
	list.Flags().Bool("graph", false, "draw lineage rails instead of a flat list")
	list.Flags().Bool("ascii", false, "render the graph with ASCII glyphs")
	list.Flags().String("after", "", "only runs after this run")
	list.Flags().String("before", "", "only runs before this run")

	show := &cobra.Command{
		Use: "show <run>", Short: "Show one run: state, snapshot pin, artifact and declaration hashes",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("run show"),
	}
	show.Flags().Bool("trace", false, "show ancestry over run_inputs")
	show.Flags().Bool("down", false, "with --trace, walk descendants instead of ancestors")

	logs := &cobra.Command{
		Use: "logs <run>", Short: "Tail a run's captured output",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("run logs"),
	}
	cancel := &cobra.Command{
		Use: "cancel <run>", Short: "Cancel one running run (kills its process group)",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("run cancel"),
	}

	c.AddCommand(list, show, logs, cancel)
	return c
}

// dataCmd builds `iris data`: row-level provenance, the single computed read.
func (a *app) dataCmd() *cobra.Command {
	c := &cobra.Command{Use: "data", Short: "Row-level reads"}
	prov := &cobra.Command{
		Use:   "provenance <schema.table> <pk>",
		Short: "Show a row's provenance: author, layer stack, consumed upstreams, hashes",
		Args:  cobra.ExactArgs(2), RunE: a.daemonStub("data provenance"),
	}
	c.AddCommand(prov)
	return c
}

// workloadCmd builds `iris workload`: the standing wiring panel and the dev
// loop's data scope.
func (a *app) workloadCmd() *cobra.Command {
	c := &cobra.Command{Use: "workload", Short: "The standing wiring and the dev loop's data scope"}

	show := &cobra.Command{
		Use: "show [pipeline]", Short: "Show the wiring panel: lanes, composer walk, gate state per edge",
		Args: cobra.MaximumNArgs(1), RunE: a.daemonStub("workload show"),
	}
	wipe := &cobra.Command{
		Use: "wipe [pipeline]", Short: "Revert un-promoted disposable data, all of it or one pipeline's",
		Args: cobra.MaximumNArgs(1), RunE: a.daemonStub("workload wipe"),
	}
	addConfirmFlags(wipe)

	c.AddCommand(show, wipe)
	return c
}

// engineCmd builds `iris engine`: the daemon, its state, and its service unit.
// Local-lifecycle verbs (start/install/uninstall/service) do not dial a daemon
// and report not-implemented; the rest reach a running daemon.
func (a *app) engineCmd() *cobra.Command {
	c := &cobra.Command{Use: "engine", Short: "Manage the daemon, its state, and its service unit"}

	start := &cobra.Command{
		Use: "start", Short: "Run an engine candidate (foreground; -d to detach)",
		Args: cobra.NoArgs, RunE: a.localStub("engine start"),
	}
	// Daemon-scoped flags live only on engine start (specification section 8).
	start.Flags().BoolP("detach", "d", false, "detach and run the engine in the background")
	start.Flags().String("pg-dsn", "", "DSN of an external Postgres to use instead of the managed one")
	start.Flags().String("retain", "", "run-history retention count")
	start.Flags().String("journal-partition-rows", "", "rows per journal partition before sealing")
	start.Flags().String("objects-path", "", "filesystem path for the local object store")
	start.Flags().String("tcp", "", "address to expose the read API and control plane over TCP")
	start.Flags().String("tls-cert", "", "TLS certificate for the TCP listener")
	start.Flags().String("tls-key", "", "TLS key for the TCP listener")

	stop := &cobra.Command{
		Use: "stop", Short: "Stop a detached daemon (graceful SIGTERM)",
		Args: cobra.NoArgs, RunE: a.daemonStub("engine stop"),
	}
	install := &cobra.Command{
		Use: "install", Short: "Create meta and the journal, ensure tables, set up the socket",
		Args: cobra.NoArgs, RunE: a.localStub("engine install"),
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Full engine teardown (gated)",
		Args: cobra.NoArgs, RunE: a.localStub("engine uninstall"),
	}
	addConfirmFlags(uninstall)
	info := &cobra.Command{
		Use: "info", Short: "Show engine and version info, role, listeners, uptime",
		Args: cobra.NoArgs, RunE: a.daemonStub("engine info"),
	}
	logs := &cobra.Command{
		Use: "logs", Short: "Tail the daemon log",
		Args: cobra.NoArgs, RunE: a.daemonStub("engine logs"),
	}
	inspect := &cobra.Command{
		Use: "inspect", Short: "Dump the engine-table DDL, read-only",
		Args: cobra.NoArgs, RunE: a.daemonStub("engine inspect"),
	}
	stats := &cobra.Command{
		Use: "stats", Short: "Show rollups: run, lane, and dead-letter counts",
		Args: cobra.NoArgs, RunE: a.daemonStub("engine stats"),
	}

	service := &cobra.Command{Use: "service", Short: "Manage the platform service unit"}
	svcInstall := &cobra.Command{
		Use: "install", Short: "Generate and install the platform service unit (systemd/launchd)",
		Args: cobra.NoArgs, RunE: a.localStub("engine service install"),
	}
	svcUninstall := &cobra.Command{
		Use: "uninstall", Short: "Remove the installed service unit",
		Args: cobra.NoArgs, RunE: a.localStub("engine service uninstall"),
	}
	service.AddCommand(svcInstall, svcUninstall)

	c.AddCommand(start, stop, install, uninstall, info, logs, inspect, stats, service)
	return c
}

// deadletterCmd builds `iris deadletter` (sole alias: dl). replay and drain
// require a scope: <run>, --pipeline <name>, or --all; a bare invocation is a
// usage error.
func (a *app) deadletterCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "deadletter",
		Aliases: []string{"dl"},
		Short:   "The dead-letter worklist",
	}

	list := &cobra.Command{
		Use: "list", Short: "List outstanding entries",
		Args: cobra.NoArgs, RunE: a.daemonStub("deadletter list"),
	}
	show := &cobra.Command{
		Use: "show <run>", Short: "Show one entry: reason, error, failed_upstream, blast radius",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("deadletter show"),
	}
	replay := &cobra.Command{
		Use: "replay [run]", Short: "Replay root causes (auto-walks failed_upstream)",
		Args: cobra.MaximumNArgs(1), RunE: a.deadletterScopedStub("deadletter replay"),
	}
	addScopeFlags(replay)
	drain := &cobra.Command{
		Use: "drain [run]", Short: "Discard entries: worklist only, nothing re-runs (gated)",
		Args: cobra.MaximumNArgs(1), RunE: a.deadletterScopedStub("deadletter drain"),
	}
	addScopeFlags(drain)
	addConfirmFlags(drain)

	c.AddCommand(list, show, replay, drain)
	return c
}

// deadletterScopedStub validates that a replay/drain names a scope before it
// would reach the daemon: a bare invocation is a usage error (exit 2), a scoped
// one reports no-daemon (exit 3) while none is reachable.
func (a *app) deadletterScopedStub(op string) runE {
	return func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		pipeline, _ := cmd.Flags().GetString("pipeline")
		if len(args) == 0 && !all && pipeline == "" {
			return a.usage(op + " requires <run>, --pipeline <name>, or --all")
		}
		return a.noDaemon(op)
	}
}

// endpointCmd builds `iris endpoint`: declared read surfaces with their own
// lifecycle, apart from declare apply.
func (a *app) endpointCmd() *cobra.Command {
	c := &cobra.Command{Use: "endpoint", Short: "Declared read surfaces (own lifecycle)"}

	apply := &cobra.Command{
		Use: "apply [name]", Short: "Publish endpoints/ (or one): validate, compile, atomic",
		Args: cobra.MaximumNArgs(1), RunE: a.daemonStub("endpoint apply"),
	}
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Retire a read surface (shape only, no data touched)",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("endpoint remove"),
	}
	list := &cobra.Command{
		Use: "list", Short: "List declared endpoints",
		Args: cobra.NoArgs, RunE: a.daemonStub("endpoint list"),
	}
	show := &cobra.Command{
		Use: "show <name>", Short: "Show resolved config, compiled shape, source fields",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("endpoint show"),
	}

	c.AddCommand(apply, remove, list, show)
	return c
}

// patCmd builds `iris pat`: PAT lifecycle with scopes {control, read, data}.
func (a *app) patCmd() *cobra.Command {
	c := &cobra.Command{Use: "pat", Short: "Manage PATs with scopes {control, read, data}"}

	create := &cobra.Command{
		Use: "create", Short: "Mint a new PAT",
		Args: cobra.ArbitraryArgs, RunE: a.daemonStub("pat create"),
	}
	list := &cobra.Command{
		Use: "list", Short: "List PATs",
		Args: cobra.NoArgs, RunE: a.daemonStub("pat list"),
	}
	revoke := &cobra.Command{
		Use: "revoke <pat>", Short: "Revoke a PAT",
		Args: cobra.ExactArgs(1), RunE: a.daemonStub("pat revoke"),
	}

	c.AddCommand(create, list, revoke)
	return c
}
