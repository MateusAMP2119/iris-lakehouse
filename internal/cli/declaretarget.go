package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-engine-cli/internal/declare"
)

// declareTargetStub is the handler for declare apply/destroy. Cobra's
// ExactArgs(1) (set at command construction) already enforces the single-
// target rule and rejects a bare or multi-file invocation before this ever
// runs (specification sections 3, 6.3, 8, and 12: exactly one declaration
// file per invocation, no --all, bare invocation exits 2). This resolves and
// parses that one target -- a file named iris-declare.yaml, or a folder
// resolved to its iris-declare.yaml, with no workspace sweep or transitive
// chaining -- and surfaces a resolution or parse failure as operation-failed
// (exit 4) naming the problem. A resolved target then passes, unchanged, to
// the daemon-dial stub: the command's real apply/destroy semantics land in a
// later epic (E03.9/E03.10).
func (a *app) declareTargetStub(op string) runE {
	return func(cmd *cobra.Command, args []string) error {
		if _, _, err := declare.LoadDeclarationFile(args[0]); err != nil {
			return &fault{code: exitOpFailed, codeStr: "declare_target", message: err.Error()}
		}
		return a.requireDaemon(cmd, op)
	}
}

// applyReport is the --json data payload of `iris declare apply`: the advisory
// warnings the apply surfaced (e.g. cross-mode reads, specification section 5). It
// is the shape the success envelope's "data" object takes, so a warning rides the
// --json output as a first-class warnings array rather than log noise.
type applyReport struct {
	// Warnings are the non-refusing advisories apply surfaced.
	Warnings []declare.Warning `json:"warnings"`
}

// declareApply is the handler for `iris declare apply`. It resolves and parses the
// single target declaration (like declareTargetStub), then computes the advisory
// warnings the apply surfaces -- cross-mode reads and the like -- through the
// applyWarnings seam. When any warning is produced, apply surfaces it and reports
// success-with-warnings: under --json the warnings ride the success envelope's
// warnings array (specification section 5), and in human mode they print to stderr
// (stdout stays clean). With no warning the command resolves its target and passes,
// unchanged, to the daemon-dial stub.
//
// The applyWarnings seam is nil in production, so pre-daemon apply computes no
// warnings and this reduces to the resolve-then-dial stub of E03.3: the meta-backed
// data-mode facts the warning needs, and the full apply, arrive in E03.9/E03.10.
// What is real from now is the warning structure and its place in the --json
// envelope.
func (a *app) declareApply() runE {
	return func(cmd *cobra.Command, args []string) error {
		_, decl, err := declare.LoadDeclarationFile(args[0])
		if err != nil {
			return &fault{code: exitOpFailed, codeStr: "declare_target", message: err.Error()}
		}
		var warnings []declare.Warning
		if a.applyWarnings != nil {
			warnings = a.applyWarnings(decl)
		}
		if len(warnings) > 0 {
			return a.emitApplyWarnings(cmd, warnings)
		}
		return a.requireDaemon(cmd, "declare apply")
	}
}

// emitApplyWarnings renders apply's advisory warnings and returns success. Under
// --json it writes the single success envelope carrying the warnings array to
// stdout; in human mode it writes each warning to stderr, leaving stdout clean.
func (a *app) emitApplyWarnings(cmd *cobra.Command, warnings []declare.Warning) error {
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		return json.NewEncoder(a.out).Encode(dataEnvelope{Data: applyReport{Warnings: warnings}})
	}
	for _, w := range warnings {
		fmt.Fprintf(a.errOut, "iris: warning: %s\n", w.Message)
	}
	return nil
}
