package declare_test

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// TestPluginsBlock proves the optional plugins block parses to alias-keyed
// requirements with the lifetime defaulting left to the engine, and that every
// malformed shape is rejected with an error naming the offending part.
func TestPluginsBlock(t *testing.T) {
	base := "name: p\nrun: [python, main.py]\n"

	t.Run("accept", func(t *testing.T) {
		src := base + `plugins:
  browser:
    ref: lightpanda@0.4
    lifetime: resident
  mail:
    ref: smtp-send@1.0
`
		decl, err := declare.ParseDeclaration([]byte(src))
		if err != nil {
			t.Fatalf("plugins declaration rejected: %v", err)
		}
		p := decl.Pipeline
		if len(p.Plugins) != 2 {
			t.Fatalf("Plugins = %+v, want two entries", p.Plugins)
		}
		if got := p.Plugins["browser"]; got.Ref != "lightpanda@0.4" || got.Lifetime != declare.LifetimeResident {
			t.Errorf("browser entry = %+v", got)
		}
		if got := p.Plugins["mail"]; got.Ref != "smtp-send@1.0" || got.Lifetime != "" {
			t.Errorf("mail entry = %+v, want lifetime empty (engine defaults it to run)", got)
		}
	})

	t.Run("absent-plugins-is-nil", func(t *testing.T) {
		decl, err := declare.ParseDeclaration([]byte(base))
		if err != nil {
			t.Fatalf("minimal declaration rejected: %v", err)
		}
		if decl.Pipeline.Plugins != nil {
			t.Errorf("Plugins = %+v, want nil when undeclared", decl.Pipeline.Plugins)
		}
	})

	reject := []struct {
		name string
		src  string
		want string
	}{
		{"not-a-map", base + "plugins: [mail]\n", "must be a map"},
		{"entry-not-a-map", base + "plugins:\n  mail: smtp-send@1.0\n", `"mail"`},
		{"bad-alias", base + "plugins:\n  Mail Sender:\n    ref: smtp-send@1.0\n", "alias"},
		{"missing-ref", base + "plugins:\n  mail:\n    lifetime: run\n", "needs a ref"},
		{"ref-without-version", base + "plugins:\n  mail:\n    ref: smtp-send\n", "name@version"},
		{"unknown-entry-field", base + "plugins:\n  mail:\n    ref: smtp-send@1.0\n    retries: 3\n", "retries"},
		{"bad-lifetime", base + "plugins:\n  mail:\n    ref: smtp-send@1.0\n    lifetime: forever\n", "run, lane, resident"},
		{"lifetime-not-string", base + "plugins:\n  mail:\n    ref: smtp-send@1.0\n    lifetime: 3\n", "lifetime"},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			_, err := declare.ParseDeclaration([]byte(tc.src))
			if err == nil {
				t.Fatalf("malformed plugins (%s) accepted; expected rejection", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
