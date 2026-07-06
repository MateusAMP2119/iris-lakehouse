// Package cli builds the Iris command-line surface: the cobra noun-verb command
// tree, the global flags, and the exit-code and --json output contracts of
// specification section 8. It sits at the top of the product import graph
// (nothing imports it), and cmd/iris is a thin entrypoint over Execute.
//
// The command handlers are stubs during epic E01. What is real from day one is
// the contract around those stubs: the categorical exit codes, the single-JSON
// envelope on stdout under --json, and the strict separation of log output
// (stderr) from command output (stdout). A stub that would reach a running
// daemon reports "no daemon reachable" (exit 3) with guidance to start the
// engine -- the honest current behavior, since no daemon exists yet -- and a
// stub that would act on the local host but is not wired yet reports "not
// implemented" (exit 4).
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// The exit codes are the categories of specification section 8. Detail rides the
// message or the --json envelope; the CLI never emits a code outside this set,
// and in particular overrides cobra's default exit 1.
const (
	exitOK           = 0 // success
	exitUsage        = 2 // usage error (bad flags/args, unknown command)
	exitNoDaemon     = 3 // no daemon reachable (start guidance)
	exitOpFailed     = 4 // operation failed (includes not-yet-implemented stubs)
	exitDeadLettered = 5 // run dead-lettered
	exitNotLeader    = 6 // not the leader
)

// Execute builds the command tree, runs it against args, and returns the process
// exit code (a specification section 8 category). It writes command output to
// stdout and human-readable errors to stderr, and never calls os.Exit, so it is
// drivable from tests. cmd/iris is a thin wrapper that passes os.Args[1:] and the
// process streams and exits with the returned code.
func Execute(args []string, stdout, stderr io.Writer) int {
	return newApp(stdout, stderr).run(args)
}

// app carries one invocation's output streams, logger, and resolved output mode.
// It is constructed per Execute call and threaded into the command handlers as a
// closure receiver, so the package holds no mutable global state and no init().
type app struct {
	out      io.Writer
	errOut   io.Writer
	logger   *slog.Logger
	jsonMode bool
}

// newApp builds an app whose structured logs go to stderr at info level, keeping
// stdout free for command output. Tests use newAppWithLogger to inject a logger.
func newApp(stdout, stderr io.Writer) *app {
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return newAppWithLogger(stdout, stderr, logger)
}

// newAppWithLogger builds an app with an explicit logger, so a test can assert
// where log output lands relative to command output.
func newAppWithLogger(stdout, stderr io.Writer, logger *slog.Logger) *app {
	return &app{out: stdout, errOut: stderr, logger: logger}
}

// run resolves the output mode, builds and executes the command tree, and maps
// the outcome to an exit-code category.
func (a *app) run(args []string) int {
	a.jsonMode = jsonRequested(args)
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetOut(a.out)
	root.SetErr(a.errOut)
	if err := root.Execute(); err != nil {
		return a.renderError(err)
	}
	return exitOK
}

// fault is a command outcome carrying a specification section 8 exit-code
// category, a machine code for the --json error envelope, and a human message.
type fault struct {
	code    int
	codeStr string
	message string
}

// Error implements error; the message is the human-readable outcome.
func (f *fault) Error() string { return f.message }

// noDaemon is the outcome of a stub that must reach a running daemon while none
// is reachable: exit 3, with guidance to start the engine folded into the
// message so it rides both the human output and the --json envelope. It logs the
// diagnostic to stderr (never stdout) at debug level, off by default.
func (a *app) noDaemon(op string) error {
	a.logger.Debug("no iris daemon reachable", "op", op)
	return &fault{
		code:    exitNoDaemon,
		codeStr: "no_daemon",
		message: `no Iris daemon reachable; start the engine with "iris engine start", or target a running daemon with --socket or --host`,
	}
}

// notImplemented is the outcome of a local-lifecycle stub that does not dial a
// daemon and is not wired yet: exit 4 (operation failed).
func (a *app) notImplemented(what string) error {
	return &fault{code: exitOpFailed, codeStr: "not_implemented", message: what + " is not implemented yet"}
}

// usage is a usage-error outcome (exit 2) raised by a handler, distinct from the
// arg/flag errors cobra raises before a handler runs.
func (a *app) usage(msg string) error {
	return &fault{code: exitUsage, codeStr: "usage", message: msg}
}

// errEnvelope is the --json error document: the read-API error envelope shape of
// specification section 7, {"error":{"code":...,"message":...}}.
type errEnvelope struct {
	Error errBody `json:"error"`
}

// errBody is the error object inside errEnvelope.
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// renderError writes an error outcome and returns its exit-code category. A
// fault carries its own code; any other error is one of cobra's own arg, flag, or
// unknown-command errors, all of which are usage errors (exit 2) -- cobra's
// default exit 1 is never surfaced. Under --json the error envelope is the single
// document on stdout; otherwise the message is written to stderr, leaving stdout
// clean.
func (a *app) renderError(err error) int {
	var f *fault
	if !errors.As(err, &f) {
		f = &fault{code: exitUsage, codeStr: "usage", message: err.Error()}
	}
	if a.jsonMode {
		_ = json.NewEncoder(a.out).Encode(errEnvelope{Error: errBody{Code: f.codeStr, Message: f.message}})
	} else {
		fmt.Fprintf(a.errOut, "iris: %s\n", f.message)
	}
	return f.code
}

// jsonRequested reports whether --json is set, scanning args directly so the
// output mode is resolved even when flag parsing later fails (an unknown flag, a
// missing argument). Only tokens before the "--" terminator count, matching
// pflag's own boundary between flags and positionals.
func jsonRequested(args []string) bool {
	want := false
	for _, arg := range args {
		switch {
		case arg == "--":
			return want
		case arg == "--json":
			want = true
		case strings.HasPrefix(arg, "--json="):
			want = truthy(arg[len("--json="):])
		}
	}
	return want
}

// truthy parses a boolean flag value the way pflag does for --json=<v>.
func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "t", "true", "y", "yes":
		return true
	default:
		return false
	}
}
