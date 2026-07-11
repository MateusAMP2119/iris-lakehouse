package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-engine-cli/internal/buildinfo"
	"github.com/MateusAMP2119/iris-engine-cli/internal/update"
)

// updateResult is the machine-readable payload of `iris update`, the --json data
// envelope: the terminal status, the running and latest versions, and (when
// replaced) the executable path.
type updateResult struct {
	Status string `json:"status"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Path   string `json:"path,omitempty"`
}

// updateCmd builds `iris update`: a root lifecycle verb (self-replace of the
// installed iris binary with the latest GitHub release, specification section 8).
// It is daemonless (it touches no daemon); it is the counterpart of the root
// `iris uninstall` and is distinct from `iris engine install`/`uninstall`, which
// manage engine state.
func (a *app) updateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "update",
		Short: "Self-replace the installed binary with the latest GitHub release",
		Args:  cobra.NoArgs,
		RunE:  a.updateSelf(),
	}
	return daemonless(c)
}

// updateSelf is the handler for `iris update`: a daemonless self-replace of the
// installed binary with the latest GitHub release (specification section 8). It
// resolves the latest release tag, refuses on a dev build, reports
// already-up-to-date without touching the binary when the tag matches, and
// otherwise downloads, checksum-verifies, and atomically replaces the running
// executable. Any failure is operation-failed (exit 4); a dev build and a
// permission failure carry actionable guidance in the message.
func (a *app) updateSelf() runE {
	return func(cmd *cobra.Command, _ []string) error {
		run := a.runUpdate
		if run == nil {
			run = update.New().Run
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		res, err := run(ctx, buildinfo.Version)
		if err != nil {
			return a.updateFault(err)
		}
		return a.emitUpdateResult(cmd, res)
	}
}

// updateFault maps a self-update failure to the operation-failed category (exit
// 4), preserving the error's own guidance for a dev build or a permission
// failure and tagging the machine code for the --json envelope.
func (a *app) updateFault(err error) error {
	code := "update_failed"
	var dev *update.DevBuildError
	if errors.As(err, &dev) {
		code = "dev_build"
	}
	return &fault{code: exitOpFailed, codeStr: code, message: fmt.Sprintf("iris update: %v", err)}
}

// emitUpdateResult renders a successful update: under --json the single data
// envelope, otherwise a human line on stdout naming the outcome.
func (a *app) emitUpdateResult(cmd *cobra.Command, res update.Result) error {
	payload := updateResult{From: res.From, To: res.To, Path: res.Path}
	switch res.Status {
	case update.StatusUpToDate:
		payload.Status = "up_to_date"
	case update.StatusUpdated:
		payload.Status = "updated"
	}
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		return json.NewEncoder(a.out).Encode(dataEnvelope{Data: payload})
	}
	switch res.Status {
	case update.StatusUpToDate:
		fmt.Fprintf(a.out, "iris is already up to date (version %s)\n", res.To)
	case update.StatusUpdated:
		fmt.Fprintf(a.out, "updated iris %s -> %s\n", res.From, res.To)
	}
	return nil
}
