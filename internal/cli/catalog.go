package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/catalog"
)

// catalogEntry is one pack in the --json listing.
type catalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Source      string   `json:"source"`
	Requires    string   `json:"requires,omitempty"`
}

// catalogShowPayload is the --json document of `iris catalog show <pack>`.
type catalogShowPayload struct {
	catalogEntry
	Pipelines  []string `json:"pipelines"`
	Files      []string `json:"files"`
	ApplyOrder []string `json:"apply_order"`
	Readme     string   `json:"readme,omitempty"`
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
		Use: "list", Short: "List the packs available to install",
		Args: cobra.NoArgs, RunE: a.catalogList(),
	}
	show := &cobra.Command{
		Use: "show <pack>", Short: "Show a pack: README, pipelines, files, and apply order",
		Args: cobra.ExactArgs(1), RunE: a.catalogShow(),
	}
	return a.group("catalog", "Browse and install pipeline packs",
		daemonTouching(initCmd), daemonless(list), daemonless(show), daemonTouching(install))
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

// catalogList handles `iris catalog list` over the embedded packs (remote catalogs arrive with #220).
func (a *app) catalogList() runE {
	return func(cmd *cobra.Command, _ []string) error {
		packs, err := catalog.Embedded()
		if err != nil {
			return catalogFault("list", err)
		}
		entries := make([]catalogEntry, 0, len(packs))
		for _, p := range packs {
			entries = append(entries, catalogEntry{Name: p.Name, Description: p.Description, Tags: p.Tags, Source: p.Source, Requires: p.Requires})
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: entries})
		}
		for _, e := range entries {
			line := fmt.Sprintf("%s  [%s]", e.Name, e.Source)
			if len(e.Tags) > 0 {
				line += "  " + strings.Join(e.Tags, ",")
			}
			fmt.Fprintln(a.out, line)
			if e.Description != "" {
				fmt.Fprintf(a.out, "  %s\n", e.Description)
			}
		}
		return nil
	}
}

// catalogShow handles `iris catalog show <pack>`.
func (a *app) catalogShow() runE {
	return func(cmd *cobra.Command, args []string) error {
		p, ok, err := catalog.EmbeddedPack(args[0])
		if err != nil {
			return catalogFault("show", err)
		}
		if !ok {
			return catalogFault("show", fmt.Errorf("no such pack %q (run iris catalog list)", args[0]))
		}
		pipelines, err := catalog.PipelineNames(p)
		if err != nil {
			return catalogFault("show", err)
		}
		order, err := catalog.ApplyOrder(p)
		if err != nil {
			return catalogFault("show", err)
		}
		files := make([]string, 0, len(p.Files))
		for _, f := range p.Files {
			files = append(files, f.Path)
		}
		payload := catalogShowPayload{
			catalogEntry: catalogEntry{Name: p.Name, Description: p.Description, Tags: p.Tags, Source: p.Source, Requires: p.Requires},
			Pipelines:    pipelines, Files: files, ApplyOrder: order, Readme: p.README,
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: payload})
		}
		fmt.Fprintf(a.out, "%s  [%s]\n", payload.Name, payload.Source)
		if payload.Description != "" {
			fmt.Fprintf(a.out, "%s\n", payload.Description)
		}
		if len(payload.Tags) > 0 {
			fmt.Fprintf(a.out, "tags: %s\n", strings.Join(payload.Tags, ","))
		}
		if payload.Requires != "" {
			fmt.Fprintf(a.out, "requires: %s\n", payload.Requires)
		}
		fmt.Fprintf(a.out, "pipelines: %s\n", strings.Join(payload.Pipelines, ", "))
		fmt.Fprintln(a.out, "files:")
		for _, f := range payload.Files {
			fmt.Fprintf(a.out, "  %s\n", f)
		}
		if payload.Readme != "" {
			fmt.Fprintf(a.out, "\n%s", payload.Readme)
		}
		return nil
	}
}
