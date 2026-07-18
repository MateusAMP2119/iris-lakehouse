package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
	"github.com/MateusAMP2119/iris-lakehouse/internal/dispatch"
	"github.com/MateusAMP2119/iris-lakehouse/internal/exec"
	"github.com/MateusAMP2119/iris-lakehouse/internal/plugin"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// toolScript is the multi-verb tool plugin the real-process tests exec: the
// verb rides argv[1], the JSON args ride stdin, the JSON result rides stdout.
const toolScript = `#!/bin/sh
verb="$1"
case "$verb" in
send)
  args=$(cat)
  printf '{"echoed":%s}' "$args"
  ;;
slow)
  sleep 5
  ;;
fail)
  echo "boom" >&2
  exit 3
  ;;
junk)
  printf 'not json'
  ;;
esac
`

// installTestPlugin installs the multi-verb tool plugin into a fresh engine
// home through the real installer (manifest + digest-verified binary), so the
// resolver path under test is the production one.
func installTestPlugin(t *testing.T, home string) plugin.Installed {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "tool.sh"), []byte(toolScript), 0o755); err != nil { //nolint:gosec // test tool must be executable
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(toolScript))
	doc := fmt.Sprintf(`name: smtp-send
version: "1.0"
kind: tool
verbs:
  send: {}
  slow:
    timeout: 200ms
  fail: {}
  junk: {}
binaries:
  %s/%s:
    url: tool.sh
    sha256: %s
`, runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sum[:]))
	manifest := filepath.Join(src, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &plugin.Installer{Home: home, Client: http.DefaultClient, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	installed, err := inst.Install(context.Background(), manifest)
	if err != nil {
		t.Fatalf("install test plugin: %v", err)
	}
	return installed
}

func TestPluginVerbCallerToolContract(t *testing.T) {
	installed := installTestPlugin(t, t.TempDir())
	caller := &pluginVerbCaller{
		runner:  exec.NewOSRunner(),
		dir:     t.TempDir(),
		aliases: map[string]plugin.Installed{"mail": installed},
	}
	ctx := context.Background()

	t.Run("verb answers with its stdout JSON", func(t *testing.T) {
		res, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "mail.send", Args: []byte(`{"to":"ops@example.com"}`)})
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		if string(res) != `{"echoed":{"to":"ops@example.com"}}` {
			t.Fatalf("result = %s", res)
		}
	})

	t.Run("undeclared alias refuses", func(t *testing.T) {
		_, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "browser.fetch"})
		if err == nil || !strings.Contains(err.Error(), "not declared") {
			t.Fatalf("Call error = %v, want undeclared-alias refusal", err)
		}
	})

	t.Run("unknown verb refuses naming the surface", func(t *testing.T) {
		_, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "mail.explode"})
		if err == nil || !strings.Contains(err.Error(), `no verb "explode"`) {
			t.Fatalf("Call error = %v, want unknown-verb refusal", err)
		}
	})

	t.Run("non-zero exit fails with the stderr tail", func(t *testing.T) {
		_, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "mail.fail"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("Call error = %v, want failure carrying the stderr tail", err)
		}
	})

	t.Run("manifest timeout kills a hung verb", func(t *testing.T) {
		_, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "mail.slow"})
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("Call error = %v, want timeout", err)
		}
	})

	t.Run("non-JSON output refuses", func(t *testing.T) {
		_, err := caller.Call(ctx, dispatch.TurnCall{ID: "c1", Verb: "mail.junk"})
		if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
			t.Fatalf("Call error = %v, want invalid-JSON refusal", err)
		}
	})
}

func TestPluginResolver(t *testing.T) {
	home := t.TempDir()
	installed := installTestPlugin(t, home)
	reqs := map[string]declare.PluginRequirement{"mail": {Ref: "smtp-send@1.0"}}

	t.Run("resolves pins and aliases", func(t *testing.T) {
		r := newPluginResolver(home)
		res, err := r.resolve("p", "sum1", reqs)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(res.pins) != 1 || res.pins[0] != (store.RunPluginPin{Alias: "mail", Plugin: "smtp-send", Version: "1.0", Digest: installed.Digest}) {
			t.Fatalf("pins = %+v", res.pins)
		}
		if _, ok := res.aliases["mail"]; !ok {
			t.Fatal("aliases missing mail")
		}
		if res.caller(exec.NewOSRunner(), home) == nil {
			t.Fatal("caller is nil for a resolved plugin set")
		}
	})

	t.Run("zero declared plugins resolve to a nil caller", func(t *testing.T) {
		r := newPluginResolver(home)
		res, err := r.resolve("p", "sum1", nil)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if res.caller(exec.NewOSRunner(), home) != nil {
			t.Fatal("caller is non-nil with nothing declared")
		}
	})

	t.Run("nil resolver refuses declared plugins", func(t *testing.T) {
		var r *pluginResolver
		if _, err := r.resolve("p", "sum1", reqs); err == nil {
			t.Fatal("nil resolver resolved declared plugins")
		}
		if _, err := r.resolve("p", "sum1", nil); err != nil {
			t.Fatalf("nil resolver with nothing declared: %v", err)
		}
	})

	t.Run("unsupported lifetime refuses", func(t *testing.T) {
		r := newPluginResolver(home)
		_, err := r.resolve("p", "sum1", map[string]declare.PluginRequirement{
			"mail": {Ref: "smtp-send@1.0", Lifetime: declare.LifetimeResident},
		})
		if err == nil || !strings.Contains(err.Error(), "not supported yet") {
			t.Fatalf("resolve error = %v, want unsupported-lifetime refusal", err)
		}
	})

	t.Run("missing install refuses the run", func(t *testing.T) {
		r := newPluginResolver(home)
		_, err := r.resolve("p", "sum1", map[string]declare.PluginRequirement{
			"mail": {Ref: "ghost@1.0"},
		})
		if err == nil || !strings.Contains(err.Error(), "not installed") {
			t.Fatalf("resolve error = %v, want not-installed refusal", err)
		}
	})

	t.Run("drifted binary refuses the run", func(t *testing.T) {
		driftHome := t.TempDir()
		drifted := installTestPlugin(t, driftHome)
		if err := os.WriteFile(drifted.Path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test tamper
			t.Fatal(err)
		}
		r := newPluginResolver(driftHome)
		_, err := r.resolve("p", "sum1", reqs)
		if err == nil || !strings.Contains(err.Error(), "drifted") {
			t.Fatalf("resolve error = %v, want drift refusal", err)
		}
	})
}

// callTurnScript is a protocol-speaking resident that makes one plugin call per
// turn: on the run frame it emits the call, reads the engine's res reply off
// stdin, logs it to stderr, and echoes done.
const callTurnScript = `while read line; do
  case "$line" in
  *'"go"'*)
    turn=$(printf '%s' "$line" | sed 's/.*"turn"://;s/[^0-9].*//')
    ;;
  *'"run"'*)
    printf '{"event":"call","id":"c1","verb":"mail.send","args":{"to":"ops"}}\n'
    read reply
    echo "reply: $reply" >&2
    printf '{"event":"done","turn":%s}\n' "$turn"
    ;;
  esac
done
`

func TestDriveTurnServesPluginCalls(t *testing.T) {
	ctx := context.Background()
	runner := exec.NewOSRunner()
	installed := installTestPlugin(t, t.TempDir())

	t.Run("call answered ok, recorded, turn completes", func(t *testing.T) {
		dir := t.TempDir()
		argv := writeScript(t, dir, callTurnScript)
		ses, err := spawnResident(ctx, runner, "k", dir, argv, nil)
		if err != nil {
			t.Fatalf("spawnResident: %v", err)
		}
		defer ses.end()
		buf := &lockedBuffer{}
		ses.out.Set(buf)

		caller := &pluginVerbCaller{runner: runner, dir: dir, aliases: map[string]plugin.Installed{"mail": installed}}
		res := driveTurn(ctx, ses, ses.nextTurn(), nil, testTurnWrites(), nil, caller)
		if res.kind != turnDone {
			t.Fatalf("turn = %+v, want done", res)
		}
		if len(res.calls) != 1 {
			t.Fatalf("calls = %+v, want one record", res.calls)
		}
		rec := res.calls[0]
		if rec.CallID != "c1" || rec.Verb != "mail.send" || rec.Status != store.PluginCallOK {
			t.Fatalf("call record = %+v", rec)
		}
		if rec.ArgsDigest == "" || rec.ResponseDigest == "" {
			t.Fatalf("call record missing digests: %+v", rec)
		}
		// The reply reached the script's stdin as an ok res frame.
		awaitOutput(t, buf, `"event":"res"`)
		awaitOutput(t, buf, `"ok"`)
	})

	t.Run("no caller answers err, records err, turn still completes", func(t *testing.T) {
		dir := t.TempDir()
		argv := writeScript(t, dir, callTurnScript)
		ses, err := spawnResident(ctx, runner, "k", dir, argv, nil)
		if err != nil {
			t.Fatalf("spawnResident: %v", err)
		}
		defer ses.end()
		buf := &lockedBuffer{}
		ses.out.Set(buf)

		res := driveTurn(ctx, ses, ses.nextTurn(), nil, testTurnWrites(), nil, nil)
		if res.kind != turnDone {
			t.Fatalf("turn = %+v, want done (an err reply is an answer, not a violation)", res)
		}
		if len(res.calls) != 1 || res.calls[0].Status != store.PluginCallErr || res.calls[0].Error == "" {
			t.Fatalf("calls = %+v, want one err record", res.calls)
		}
		awaitOutput(t, buf, `"err"`)
	})
}
