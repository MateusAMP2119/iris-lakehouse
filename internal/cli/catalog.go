package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// catalogListPayload is the --json document of `iris catalog list`.
type catalogListPayload struct {
	Packs    []api.CatalogPack `json:"packs"`
	Warnings []string          `json:"warnings,omitempty"`
}

// catalogCmd builds `iris catalog`: pack browsing plus the leader-mediated install (#217).
func (a *app) catalogCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use: "init", Short: "Install the starter pack (" + catalog.StarterPack + ") into the engine's workspace",
		Args: cobra.NoArgs, RunE: a.catalogInstall(true),
	}
	install := &cobra.Command{
		Use: "install <pack>", Short: "Materialize a pack into the engine's workspace through the leader",
		Args: cobra.ExactArgs(1), RunE: a.catalogInstall(false),
	}
	for _, c := range []*cobra.Command{initCmd, install} {
		c.Flags().Bool("apply", false, "run the declare sequence in the derived order after materializing")
		c.Flags().Bool("force", false, "overwrite existing workspace paths instead of refusing")
	}
	list := &cobra.Command{
		Use: "list", Short: "List the packs available to install from configured catalogs",
		Args: cobra.NoArgs, RunE: a.catalogList(),
	}
	show := &cobra.Command{
		Use: "show <pack>", Short: "Show a pack: README, pipelines, files, apply order, and source",
		Args: cobra.ExactArgs(1), RunE: a.catalogShow(),
	}
	return a.group("catalog", "Browse and install pipeline packs",
		daemonTouching(initCmd), daemonTouching(list), daemonTouching(show), daemonTouching(install))
}

// catalogFault maps a catalog operation failure to operation-failed (exit 4).
func catalogFault(op string, err error) error {
	return &fault{code: exitOpFailed, codeStr: "catalog_" + op + "_failed", message: fmt.Sprintf("iris catalog %s: %v", op, err)}
}

// catalogInstall handles `iris catalog install <pack>` and, with starter, `iris catalog init`.
func (a *app) catalogInstall(starter bool) runE {
	return func(cmd *cobra.Command, args []string) error {
		pack := catalog.StarterPack
		if !starter {
			pack = args[0]
		}
		apply, _ := cmd.Flags().GetBool("apply")
		force, _ := cmd.Flags().GetBool("force")
		var res api.CatalogInstallResult
		if err := a.postDaemonJSON(cmd, "/catalog/install",
			api.CatalogInstallRequest{Pack: pack, Apply: apply, Force: force}, "catalog install", &res); err != nil {
			return err
		}
		// Warnings render like declare apply's: top-level beside data under --json, stderr-prefixed otherwise.
		warnings := res.Warnings
		res.Warnings = nil
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(struct {
				Data     api.CatalogInstallResult `json:"data"`
				Warnings []string                 `json:"warnings,omitempty"`
			}{Data: res, Warnings: warnings})
		}
		for _, w := range warnings {
			fmt.Fprintf(a.errOut, "iris: warning: %s\n", w)
		}
		fmt.Fprintf(a.out, "installed pack %s (%d files)\n", res.Pack, len(res.Files))
		for _, f := range res.Files {
			fmt.Fprintf(a.out, "  %s\n", f)
		}
		if res.Applied {
			fmt.Fprintf(a.out, "applied %d declaration(s)\n", len(res.ApplyOrder))
		} else {
			fmt.Fprintln(a.out, "apply order:")
			for _, p := range res.ApplyOrder {
				fmt.Fprintf(a.out, "  %s\n", p)
			}
			fmt.Fprintln(a.out, "declare them in that order with `iris declare apply <path>`")
		}
		return nil
	}
}

// catalogListTimeout bounds the listing read so a wedged daemon fails instead of hanging the verb.
const catalogListTimeout = 10 * time.Second

// fetchCatalogListing reads GET /catalog. live=false means no engine answered.
// Catalog egress is daemon-side only: with no engine the CLI does not fetch packs itself.
func (a *app) fetchCatalogListing(cmd *cobra.Command, op string) (payload catalogListPayload, live bool, err error) {
	settings := a.resolveTarget(cmd)
	client, base, overTCP := a.daemonHTTPClient(settings)
	ctx, cancel := context.WithTimeout(cmd.Context(), catalogListTimeout)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, base+"/catalog", nil)
	if rerr != nil {
		return catalogListPayload{}, true, &fault{code: exitOpFailed, codeStr: "request", message: fmt.Sprintf("iris catalog %s: build request: %v", op, rerr)}
	}
	if overTCP && settings.Token != "" {
		req.Header.Set("Authorization", "Bearer "+settings.Token)
	}
	resp, derr := client.Do(req)
	if derr != nil {
		return catalogListPayload{}, false, nil
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error errBody `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&env)
		code := env.Error.Code
		if code == "" {
			code = "operation_failed"
		}
		msg := env.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("daemon status %d", resp.StatusCode)
		}
		return catalogListPayload{}, true, &fault{code: exitOpFailed, codeStr: code, message: fmt.Sprintf("iris catalog %s: %s", op, msg)}
	}
	var env struct {
		Data api.CatalogListResult `json:"data"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&env); derr != nil {
		return catalogListPayload{}, true, &fault{code: exitOpFailed, codeStr: "decode", message: fmt.Sprintf("iris catalog %s: decode /catalog response: %v", op, derr)}
	}
	return catalogListPayload{Packs: env.Data.Packs, Warnings: env.Data.Warnings}, true, nil
}

// catalogList handles `iris catalog list`: the daemon's multi-source listing.
func (a *app) catalogList() runE {
	return func(cmd *cobra.Command, _ []string) error {
		payload, live, err := a.fetchCatalogListing(cmd, "list")
		if err != nil {
			return err
		}
		if !live {
			return catalogFault("list", fmt.Errorf("engine unreachable; start it (iris engine start -d) or pass --socket/--host"))
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: payload})
		}
		for _, e := range payload.Packs {
			line := fmt.Sprintf("%s  [%s]", e.Name, e.Source)
			if e.Installed {
				line += "  ●installed"
			}
			if e.Shadowed {
				line += "  (shadowed)"
			}
			if len(e.Tags) > 0 {
				line += "  " + strings.Join(e.Tags, ",")
			}
			fmt.Fprintln(a.out, line)
			if e.Description != "" {
				fmt.Fprintf(a.out, "  %s\n", e.Description)
			}
		}
		for _, w := range payload.Warnings {
			fmt.Fprintf(a.out, "warning: %s\n", w)
		}
		return nil
	}
}

// catalogShow handles `iris catalog show <pack>`: the daemon's view of one pack.
func (a *app) catalogShow() runE {
	return func(cmd *cobra.Command, args []string) error {
		payload, live, err := a.fetchCatalogListing(cmd, "show")
		if err != nil {
			return err
		}
		if !live {
			return catalogFault("show", fmt.Errorf("engine unreachable; start it (iris engine start -d) or pass --socket/--host"))
		}
		var entry *api.CatalogPack
		for i := range payload.Packs {
			if payload.Packs[i].Name == args[0] && !payload.Packs[i].Shadowed {
				entry = &payload.Packs[i]
				break
			}
		}
		if entry == nil {
			return catalogFault("show", fmt.Errorf("no such pack %q (run iris catalog list)", args[0]))
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: entry})
		}
		fmt.Fprintf(a.out, "%s  [%s]\n", entry.Name, entry.Source)
		if entry.Description != "" {
			fmt.Fprintf(a.out, "%s\n", entry.Description)
		}
		if len(entry.Tags) > 0 {
			fmt.Fprintf(a.out, "tags: %s\n", strings.Join(entry.Tags, ","))
		}
		if entry.Requires != "" {
			fmt.Fprintf(a.out, "requires: %s\n", entry.Requires)
		}
		if entry.SHA256 != "" {
			fmt.Fprintf(a.out, "sha256: %s\n", entry.SHA256)
		}
		if len(entry.Pipelines) > 0 {
			fmt.Fprintf(a.out, "pipelines: %s\n", strings.Join(entry.Pipelines, ", "))
		}
		if len(entry.Files) > 0 {
			fmt.Fprintln(a.out, "files:")
			for _, f := range entry.Files {
				fmt.Fprintf(a.out, "  %s\n", f)
			}
		}
		if len(entry.ApplyOrder) > 0 {
			fmt.Fprintln(a.out, "apply order:")
			for _, s := range entry.ApplyOrder {
				fmt.Fprintf(a.out, "  %s\n", s)
			}
		}
		if entry.Readme != "" {
			fmt.Fprintf(a.out, "\n%s", entry.Readme)
		}
		return nil
	}
}
