package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
	"github.com/MateusAMP2119/iris-lakehouse/internal/plugin"
)

// pluginResult is the machine-readable payload of one plugin, shared by the
// plugin verbs' --json envelopes.
type pluginResult struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"`
	Digest  string   `json:"digest"`
	Path    string   `json:"path"`
	Verbs   []string `json:"verbs,omitempty"`
}

// pluginPayload renders an installed plugin as the envelope payload.
func pluginPayload(inst plugin.Installed) pluginResult {
	return pluginResult{
		Name:    inst.Ref.Name,
		Version: inst.Ref.Version,
		Kind:    string(inst.Kind),
		Digest:  inst.Digest,
		Path:    inst.Path,
		Verbs:   inst.Manifest.VerbNames(),
	}
}

// pluginCmd builds `iris plugin`: the local plugin store's lifecycle. Every
// verb is daemonless: plugins install under the engine home, and the daemon
// only reads them at run time.
func (a *app) pluginCmd() *cobra.Command {
	install := &cobra.Command{
		Use:   "install <manifest>",
		Short: "Install a plugin from a manifest URL or file, verifying its pinned sha256",
		Args:  cobra.ExactArgs(1),
		RunE:  a.pluginInstall(),
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List installed plugins",
		Args:  cobra.NoArgs,
		RunE:  a.pluginList(),
	}
	remove := &cobra.Command{
		Use:   "remove <name@version>",
		Short: "Remove one installed plugin version",
		Args:  cobra.ExactArgs(1),
		RunE:  a.pluginRemove(),
	}
	verify := &cobra.Command{
		Use:   "verify <name@version>",
		Short: "Re-verify an installed plugin's binary against its manifest pin",
		Args:  cobra.ExactArgs(1),
		RunE:  a.pluginVerify(),
	}
	return a.group("plugin", "Install and manage digest-pinned plugins",
		daemonless(install), daemonless(list), daemonless(remove), daemonless(verify))
}

// pluginInstaller resolves the engine home and returns the plugin installer
// rooted there.
func (a *app) pluginInstaller() (*plugin.Installer, error) {
	home, err := config.Home(os.Getenv)
	if err != nil {
		return nil, &fault{code: exitOpFailed, codeStr: "plugin_failed", message: fmt.Sprintf("iris plugin: %v", err)}
	}
	return plugin.NewInstaller(home), nil
}

// pluginFault maps a plugin operation failure to operation-failed (exit 4).
func pluginFault(op string, err error) error {
	return &fault{code: exitOpFailed, codeStr: "plugin_" + op + "_failed", message: fmt.Sprintf("iris plugin %s: %v", op, err)}
}

// pluginInstall is the handler for `iris plugin install`: fetch the manifest,
// download the platform binary, verify the pinned sha256 (a mismatch refuses
// the install), and record both under the engine home.
func (a *app) pluginInstall() runE {
	return func(cmd *cobra.Command, args []string) error {
		inst, err := a.pluginInstaller()
		if err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		installed, err := inst.Install(ctx, args[0])
		if err != nil {
			return pluginFault("install", err)
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: pluginPayload(installed)})
		}
		fmt.Fprintf(a.out, "installed %s (sha256 %s)\n", installed.Ref, installed.Digest)
		return nil
	}
}

// pluginList is the handler for `iris plugin list`: every installed plugin
// version, verified as it is listed.
func (a *app) pluginList() runE {
	return func(cmd *cobra.Command, _ []string) error {
		inst, err := a.pluginInstaller()
		if err != nil {
			return err
		}
		list, err := inst.List()
		if err != nil {
			return pluginFault("list", err)
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			payload := make([]pluginResult, 0, len(list))
			for _, p := range list {
				payload = append(payload, pluginPayload(p))
			}
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: payload})
		}
		if len(list) == 0 {
			fmt.Fprintln(a.out, "no plugins installed")
			return nil
		}
		for _, p := range list {
			fmt.Fprintf(a.out, "%s\t%s\t%s\n", p.Ref, p.Kind, p.Digest)
		}
		return nil
	}
}

// pluginRemove is the handler for `iris plugin remove`: delete one installed
// version. Reinstalling from its manifest restores it bit-for-bit, so no
// confirmation ceremony guards it.
func (a *app) pluginRemove() runE {
	return func(cmd *cobra.Command, args []string) error {
		ref, err := plugin.ParseRef(args[0])
		if err != nil {
			return a.usage(err.Error())
		}
		inst, err := a.pluginInstaller()
		if err != nil {
			return err
		}
		if err := inst.Remove(ref); err != nil {
			return pluginFault("remove", err)
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: map[string]string{
				"removed": ref.String(),
			}})
		}
		fmt.Fprintf(a.out, "removed %s\n", ref)
		return nil
	}
}

// pluginVerify is the handler for `iris plugin verify`: recompute the installed
// binary's sha256 and compare it to the manifest pin; drift is a failure.
func (a *app) pluginVerify() runE {
	return func(cmd *cobra.Command, args []string) error {
		ref, err := plugin.ParseRef(args[0])
		if err != nil {
			return a.usage(err.Error())
		}
		inst, err := a.pluginInstaller()
		if err != nil {
			return err
		}
		verified, err := inst.Verify(ref)
		if err != nil {
			return pluginFault("verify", err)
		}
		if jsonMode, _ := cmd.Flags().GetBool("json"); jsonMode {
			return json.NewEncoder(a.out).Encode(dataEnvelope{Data: pluginPayload(verified)})
		}
		fmt.Fprintf(a.out, "%s OK (sha256 %s)\n", verified.Ref, verified.Digest)
		return nil
	}
}
