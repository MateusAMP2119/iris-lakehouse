package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/spf13/cobra"
)

// This file is `iris apply`: the whole-workspace sync. Edit declarations and
// scripts freely, run one command — the leader diffs every discovered
// declaration against its last-applied head and applies what changed, in
// dependency order. The single-file `iris declare apply` stays the primitive.

// applyCmd builds the top-level `iris apply` command.
func (a *app) applyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply",
		Short: "Sync the whole workspace: apply every new or changed declaration, in dependency order",
		Args:  cobra.NoArgs,
		RunE:  a.workspaceApply(),
	}
	c.Flags().Bool("dry-run", false, "report the sync plan without applying anything")
	return daemonTouching(c)
}

// workspaceApply is the handler: POST /workspace/apply, render the plan.
func (a *app) workspaceApply() runE {
	return func(cmd *cobra.Command, _ []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		resp, err := a.postJSON(cmd, "/workspace/apply", api.WorkspaceApplyRequest{DryRun: dryRun}, "apply")
		if err != nil {
			return err
		}
		defer drainCloseBody(resp)
		if resp.StatusCode != http.StatusOK {
			return a.controlErrorFault(resp, "apply")
		}
		var env struct {
			Data api.WorkspaceApplyResult `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			return &fault{code: exitOpFailed, codeStr: "decode", message: fmt.Sprintf("apply: decode daemon response: %v", err)}
		}
		return a.emitWorkspaceApply(cmd, env.Data)
	}
}

// changeGlyphs maps a change action to its plan glyph.
var changeGlyphs = map[string]string{
	api.ChangeRegistered: "+",
	api.ChangeUpdated:    "~",
	api.ChangeUnchanged:  "=",
	api.ChangeMissing:    "!",
}

// emitWorkspaceApply renders the sync outcome: one JSON document under --json,
// otherwise the aligned plan with a summary line, warnings on stderr.
func (a *app) emitWorkspaceApply(cmd *cobra.Command, res api.WorkspaceApplyResult) error {
	if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
		return json.NewEncoder(a.out).Encode(struct {
			Data api.WorkspaceApplyResult `json:"data"`
		}{Data: res})
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(a.errOut, "iris: warning: %s\n", w)
	}
	if len(res.Changes) == 0 {
		fmt.Fprintln(a.out, "workspace carries no declarations")
		return nil
	}
	width := 0
	for _, c := range res.Changes {
		if l := len([]rune(c.Target)); l > width {
			width = l
		}
	}
	unchanged := 0
	for _, c := range res.Changes {
		detail := c.Detail
		switch c.Action {
		case api.ChangeUnchanged:
			unchanged++
			detail = "unchanged"
		case api.ChangeRegistered:
			detail = "registered (" + c.Kind + nonEmpty(", ", c.Detail) + ")"
		case api.ChangeUpdated:
			detail = "updated (" + c.Kind + nonEmpty(", ", c.Detail) + ")"
		}
		fmt.Fprintf(a.out, "  %s %-*s  %s\n", changeGlyphs[c.Action], width, c.Target, detail)
	}
	verb := "applied"
	if res.DryRun {
		verb = "would apply"
	}
	fmt.Fprintf(a.out, "%s %d change(s), %d unchanged\n", verb, planned(res), unchanged)
	return nil
}

// planned counts the plan's to-apply entries: on a dry run the registered and
// updated actions, otherwise what actually applied.
func planned(res api.WorkspaceApplyResult) int {
	if !res.DryRun {
		return res.Applied
	}
	n := 0
	for _, c := range res.Changes {
		if c.Action == api.ChangeRegistered || c.Action == api.ChangeUpdated {
			n++
		}
	}
	return n
}

// nonEmpty joins sep+s when s is non-empty.
func nonEmpty(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}
