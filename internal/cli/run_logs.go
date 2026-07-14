package cli

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// This file is the CLI side of `iris run logs <run>`: it GETs the run's captured
// stdout/stderr from the daemon (GET /runs/{id}/logs) and streams the plain text
// to stdout -- raw process output, never an envelope. Transport failure is
// no-daemon (exit 3) with start guidance; a run with no captured output on the
// answering node is operation-failed (exit 4) with the daemon's explanation.
func (a *app) runLogs() runE {
	return func(cmd *cobra.Command, args []string) error {
		runID := args[0]
		settings := a.resolveTarget(cmd)
		client, base, overTCP := a.daemonHTTPClient(settings)

		hreq, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, base+"/runs/"+runID+"/logs", nil)
		if err != nil {
			return &fault{code: exitOpFailed, codeStr: "request", message: fmt.Sprintf("run logs: build request: %v", err)}
		}
		if overTCP && settings.Token != "" {
			hreq.Header.Set("Authorization", "Bearer "+settings.Token)
		}

		resp, err := client.Do(hreq)
		if err != nil {
			a.logger.Debug("no iris daemon reachable", "op", "run logs", "socket", settings.Socket, "host", settings.Host, "err", err)
			return a.noDaemonFault()
		}
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}()

		if resp.StatusCode != http.StatusOK {
			return a.controlFailure(resp, "run logs")
		}
		if _, err := io.Copy(a.out, resp.Body); err != nil {
			return &fault{code: exitOpFailed, codeStr: "stream", message: fmt.Sprintf("run logs: stream captured output: %v", err)}
		}
		return nil
	}
}
