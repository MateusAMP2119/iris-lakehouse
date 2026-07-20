package cli

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// This file is the CLI side of `iris run logs <run>`: it GETs the run's
// captured output from the daemon (GET /runs/{id}/logs) and streams the plain
// text to stdout -- raw process output, never an envelope. A framed capture (a
// declared logs block) renders naturalized by default; --log and --frames
// filter it to one stream, --tagged streams the raw tagged file. Transport
// failure is no-daemon (exit 3) with start guidance; a run with no captured
// output on the answering node is operation-failed (exit 4) with the daemon's
// explanation.
func (a *app) runLogs() runE {
	return func(cmd *cobra.Command, args []string) error {
		runID := args[0]
		onlyLog, _ := cmd.Flags().GetBool("log")
		onlyFrames, _ := cmd.Flags().GetBool("frames")
		tagged, _ := cmd.Flags().GetBool("tagged")
		picked := 0
		for _, f := range []bool{onlyLog, onlyFrames, tagged} {
			if f {
				picked++
			}
		}
		if picked > 1 {
			return &fault{code: exitOpFailed, codeStr: "flags", message: "run logs: --log, --frames, and --tagged are mutually exclusive"}
		}
		query := ""
		switch {
		case onlyLog:
			query = "?stream=log"
		case onlyFrames:
			query = "?stream=frames"
		case tagged:
			query = "?format=tagged"
		}
		settings := a.resolveTarget(cmd)
		client, base, overTCP := a.daemonHTTPClient(settings)

		hreq, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, base+"/runs/"+runID+"/logs"+query, nil)
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
