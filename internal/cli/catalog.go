package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
		Use: "init", Short: "Install the embedded starter pack (" + catalog.StarterPack + ") into the engine's workspace",
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
		Use: "list", Short: "List the packs available to install (embedded plus configured catalogs)",
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
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: res})
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
			fmt.Fprintf(a.out, "run `iris catalog install %s --apply --force` (or apply each in order) to declare them\n", res.Pack)
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(a.out, "warning: %s\n", w)
		}
		return nil
	}
}

// fetchCatalogListing reads GET /catalog; live=false means no engine answered (the caller falls back to embedded).
func (a *app) fetchCatalogListing(cmd *cobra.Command) (payload catalogListPayload, live bool, err error) {
	settings := a.resolveTarget(cmd)
	client, base, overTCP := a.daemonHTTPClient(settings)
	req, rerr := http.NewRequestWithContext(cmd.Context(), http.MethodGet, base+"/catalog", nil)
	if rerr != nil {
		return catalogListPayload{}, false, nil
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
		msg := env.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("daemon status %d", resp.StatusCode)
		}
		return catalogListPayload{}, true, fmt.Errorf("%s", msg)
	}
	var env struct {
		Data api.CatalogListResult `json:"data"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&env); derr != nil {
		return catalogListPayload{}, true, fmt.Errorf("decode /catalog response: %v", derr)
	}
	return catalogListPayload{Packs: env.Data.Packs, Warnings: env.Data.Warnings}, true, nil
}

// embeddedListing renders the embedded packs as listing entries: the zero-network fallback.
func embeddedListing() (catalogListPayload, error) {
	packs, err := catalog.Embedded()
	if err != nil {
		return catalogListPayload{}, err
	}
	out := catalogListPayload{Warnings: []string{"engine unreachable · embedded packs only"}}
	for _, p := range packs {
		out.Packs = append(out.Packs, localPackEntry(p))
	}
	return out, nil
}

// localPackEntry renders one embedded pack with its full preview material.
func localPackEntry(p catalog.Pack) api.CatalogPack {
	entry := api.CatalogPack{
		Name: p.Name, Description: p.Description, Tags: p.Tags,
		Requires: p.Requires, SHA256: p.SHA256, Source: p.Source, Readme: p.README,
	}
	for _, f := range p.Files {
		entry.Files = append(entry.Files, f.Path)
	}
	if names, err := catalog.PipelineNames(p); err == nil {
		entry.Pipelines = names
	}
	if order, err := catalog.ApplyOrder(p); err == nil {
		entry.ApplyOrder = order
	}
	return entry
}

// catalogList handles `iris catalog list`: the daemon's multi-source listing, embedded fallback with no engine.
func (a *app) catalogList() runE {
	return func(cmd *cobra.Command, _ []string) error {
		payload, live, err := a.fetchCatalogListing(cmd)
		if err != nil {
			return catalogFault("list", err)
		}
		if !live {
			if payload, err = embeddedListing(); err != nil {
				return catalogFault("list", err)
			}
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

// catalogShow handles `iris catalog show <pack>`: the daemon's view of one pack, embedded fallback with no engine.
func (a *app) catalogShow() runE {
	return func(cmd *cobra.Command, args []string) error {
		payload, live, err := a.fetchCatalogListing(cmd)
		if err != nil {
			return catalogFault("show", err)
		}
		var entry *api.CatalogPack
		if live {
			for i := range payload.Packs {
				if payload.Packs[i].Name == args[0] && !payload.Packs[i].Shadowed {
					entry = &payload.Packs[i]
					break
				}
			}
		} else if p, ok, lerr := catalog.EmbeddedPack(args[0]); lerr != nil {
			return catalogFault("show", lerr)
		} else if ok {
			e := localPackEntry(p)
			entry = &e
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
		if entry.Readme != "" {
			fmt.Fprintf(a.out, "\n%s", entry.Readme)
		}
		return nil
	}
}
