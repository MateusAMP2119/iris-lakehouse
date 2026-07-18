package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/config"
)

// TestIrisTOMLEngineSettingsOnly proves that iris.toml is limited to
// engine/connection settings and is never treated as a project manifest. Every
// documented engine setting is honored; every project-manifest-shaped key
// contributes nothing to the resolved settings and is reported as ignored so the
// choice is visible rather than silent.
func TestIrisTOMLEngineSettingsOnly(t *testing.T) {
	t.Run("engine and connection settings are honored", func(t *testing.T) {
		src := `
# an iris.toml with the full documented engine/connection surface
socket = "/var/run/iris.sock"
host = "10.0.0.5:7000"
token = "pat-abc"
pg_dsn = "postgres://admin@db/iris"
retain = 250
journal_partition_rows = 5000000
objects_path = "/data/objects"
tcp = "0.0.0.0:7000"
tls_cert = "/etc/iris/cert.pem"
tls_key = "/etc/iris/key.pem"
catalogs = ["https://a/catalog.json", "https://b/catalog.json"]
`
		res, err := config.ParseTOML([]byte(src))
		if err != nil {
			t.Fatalf("ParseTOML: %v", err)
		}
		if len(res.Ignored) != 0 {
			t.Errorf("engine-only toml reported ignored keys %v, want none", res.Ignored)
		}
		got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
		checks := []struct {
			field string
			got   any
			want  any
		}{
			{"Socket", got.Socket, "/var/run/iris.sock"},
			{"Host", got.Host, "10.0.0.5:7000"},
			{"Token", got.Token, "pat-abc"},
			{"PgDSN", got.PgDSN, "postgres://admin@db/iris"},
			{"Retain", got.Retain, int64(250)},
			{"JournalPartitionRows", got.JournalPartitionRows, int64(5000000)},
			{"ObjectsPath", got.ObjectsPath, "/data/objects"},
			{"TCP", got.TCP, "0.0.0.0:7000"},
			{"TLSCert", got.TLSCert, "/etc/iris/cert.pem"},
			{"TLSKey", got.TLSKey, "/etc/iris/key.pem"},
		}
		for _, c := range checks {
			if c.got != c.want {
				t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
			}
		}
		if want := []string{"https://a/catalog.json", "https://b/catalog.json"}; !reflect.DeepEqual(got.Catalogs, want) {
			t.Errorf("Catalogs = %#v, want %#v", got.Catalogs, want)
		}
	})

	t.Run("project-manifest keys are not honored", func(t *testing.T) {
		// The keys a project manifest (iris-declare.yaml) carries must never
		// register as engine settings, whether they sit alone or beside real ones.
		src := `
socket = "/keep.sock"
name = "orders"
run = "python main.py"
pipelines = "ingest"
schemas = "analytics"
depends_on = "extract_orders"
reads = "raw.orders_staging"
writes = "analytics.orders"
env_file = ".env"
order = "load_orders"
composer = "ingest"
`
		res, err := config.ParseTOML([]byte(src))
		if err != nil {
			t.Fatalf("ParseTOML: %v", err)
		}

		// The lone engine setting still lands...
		got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
		if got.Socket != "/keep.sock" {
			t.Errorf("Socket = %q, want /keep.sock (the one engine key)", got.Socket)
		}

		// ...and every project-manifest key is reported as not honored.
		projectKeys := []string{
			"name", "run", "pipelines", "schemas", "depends_on",
			"reads", "writes", "env_file", "order", "composer",
		}
		ignored := map[string]bool{}
		for _, k := range res.Ignored {
			ignored[k] = true
		}
		for _, k := range projectKeys {
			if !ignored[k] {
				t.Errorf("project-manifest key %q was not reported ignored; iris.toml must never honor it", k)
			}
		}
	})

	t.Run("a project manifest as iris.toml resolves to pure defaults", func(t *testing.T) {
		// A file carrying only project-manifest keys leaves the engine settings
		// untouched: resolving it changes nothing versus the empty layer.
		src := `
name = "orders"
run = "python main.py"
schemas = "analytics"
`
		res, err := config.ParseTOML([]byte(src))
		if err != nil {
			t.Fatalf("ParseTOML: %v", err)
		}
		withFile := config.Resolve(config.Defaults("/ws"), res.Layer, config.Layer{}, config.Layer{})
		noFile := config.Resolve(config.Defaults("/ws"), config.Layer{}, config.Layer{}, config.Layer{})
		if !reflect.DeepEqual(withFile, noFile) {
			t.Errorf("a project-manifest iris.toml changed the resolved settings:\n with = %+v\n none = %+v", withFile, noFile)
		}
	})

	t.Run("comments, blank lines, and inline comments parse", func(t *testing.T) {
		src := "# leading comment\n\nretain = 12 # keep a dozen\nsocket = \"/x.sock\" # the socket\n"
		res, err := config.ParseTOML([]byte(src))
		if err != nil {
			t.Fatalf("ParseTOML: %v", err)
		}
		got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
		if got.Retain != 12 {
			t.Errorf("Retain = %d, want 12 (inline comment stripped)", got.Retain)
		}
		if got.Socket != "/x.sock" {
			t.Errorf("Socket = %q, want /x.sock (inline comment stripped)", got.Socket)
		}
	})

	t.Run("catalogs string arrays parse", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
			want []string
		}{
			{"two elements", "catalogs = [\"https://a/catalog.json\", \"https://b/catalog.json\"]\n", []string{"https://a/catalog.json", "https://b/catalog.json"}},
			{"single element no spaces", "catalogs = [\"https://a/catalog.json\"]\n", []string{"https://a/catalog.json"}},
			{"empty array", "catalogs = []\n", []string{}},
			{"padded spaces", "catalogs = [ \"https://a\" ,\t\"https://b\" ]\n", []string{"https://a", "https://b"}},
			{"inline comment after array", "catalogs = [\"https://a\"] # indexes\n", []string{"https://a"}},
			{"escaped quote in element", "catalogs = [\"https://a/\\\"v1\\\"\"]\n", []string{`https://a/"v1"`}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				res, err := config.ParseTOML([]byte(tc.src))
				if err != nil {
					t.Fatalf("ParseTOML: %v", err)
				}
				got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
				if !reflect.DeepEqual(got.Catalogs, tc.want) {
					t.Errorf("Catalogs = %#v, want %#v", got.Catalogs, tc.want)
				}
			})
		}
	})

	t.Run("malformed arrays are rejected", func(t *testing.T) {
		// Trailing commas are pinned as rejected: the parser is strict, elements are
		// strictly comma-separated with no dangling separator.
		bad := map[string]string{
			"unterminated bracket":      "catalogs = [\"https://a/catalog.json\"\n",
			"bare word element":         "catalogs = [foo]\n",
			"integer element":           "catalogs = [1, 2]\n",
			"nested array":              "catalogs = [[\"https://a\"]]\n",
			"trailing comma":            "catalogs = [\"https://a\",]\n",
			"missing comma":             "catalogs = [\"https://a\" \"https://b\"]\n",
			"multiline array":           "catalogs = [\n\"https://a\",\n\"https://b\"\n]\n",
			"unterminated element":      "catalogs = [\"https://a]\n",
			"array for a string key":    "socket = [\"https://a\"]\n",
			"array for an int key":      "retain = [\"5\"]\n",
			"plain string for catalogs": "catalogs = \"https://a\"\n",
			"bare int for catalogs":     "catalogs = 5\n",
		}
		for name, src := range bad {
			t.Run(name, func(t *testing.T) {
				if _, err := config.ParseTOML([]byte(src)); err == nil {
					t.Errorf("ParseTOML(%q) = nil error, want a strict-parse rejection", src)
				}
			})
		}
	})

	t.Run("an array error names its key", func(t *testing.T) {
		_, err := config.ParseTOML([]byte("catalogs = [\"https://a\"\n"))
		if err == nil {
			t.Fatal("ParseTOML(unterminated array) = nil error, want a rejection")
		}
		if !strings.Contains(err.Error(), "catalogs") {
			t.Errorf("array error %q does not name the key catalogs", err)
		}
	})

	t.Run("LoadTOMLFile round-trips catalogs", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "iris.toml")
		src := "socket = \"/x.sock\"\ncatalogs = [\"https://a/catalog.json\", \"https://b/catalog.json\"]\n"
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := config.LoadTOMLFile(path)
		if err != nil {
			t.Fatalf("LoadTOMLFile: %v", err)
		}
		got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
		if want := []string{"https://a/catalog.json", "https://b/catalog.json"}; !reflect.DeepEqual(got.Catalogs, want) {
			t.Errorf("Catalogs = %#v, want %#v from the loaded file", got.Catalogs, want)
		}
		if got.Socket != "/x.sock" {
			t.Errorf("Socket = %q, want /x.sock beside the catalogs key", got.Socket)
		}
	})

	t.Run("an iris.toml without catalogs resolves identically (backward compat)", func(t *testing.T) {
		// A pre-#220 file must resolve exactly as before the knob existed: Catalogs
		// stays unset and every other setting lands unchanged.
		res, err := config.ParseTOML([]byte("socket = \"/x.sock\"\nretain = 5\n"))
		if err != nil {
			t.Fatalf("ParseTOML: %v", err)
		}
		if res.Layer.Catalogs != nil {
			t.Errorf("catalogs-free file set Layer.Catalogs = %#v, want nil (unset)", *res.Layer.Catalogs)
		}
		got := config.Resolve(config.Defaults("/ws"), res.Layer, config.Layer{}, config.Layer{})
		sock, retain := "/x.sock", int64(5)
		want := config.Resolve(config.Defaults("/ws"), config.Layer{Socket: &sock, Retain: &retain}, config.Layer{}, config.Layer{})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("catalogs-free resolution = %+v, want %+v", got, want)
		}
		if got.Catalogs != nil {
			t.Errorf("catalogs-free Catalogs = %#v, want nil", got.Catalogs)
		}
	})

	t.Run("strict syntax is rejected", func(t *testing.T) {
		bad := map[string]string{
			"table header":             "[engine]\nsocket = \"/x\"\n",
			"missing equals":           "socket\n",
			"int key with string":      "retain = \"nope\"\n",
			"string key with bare int": "socket = 5\n",
			"unterminated string":      "socket = \"/x\n",
			"dotted key":               "engine.socket = \"/x\"\n",
		}
		for name, src := range bad {
			t.Run(name, func(t *testing.T) {
				if _, err := config.ParseTOML([]byte(src)); err == nil {
					t.Errorf("ParseTOML(%q) = nil error, want a strict-parse rejection", src)
				}
			})
		}
	})

	t.Run("LoadTOMLFile tolerates an absent file", func(t *testing.T) {
		// The zero-config path has no iris.toml; loading a missing file is not an
		// error, it just contributes an empty layer.
		res, err := config.LoadTOMLFile("/no/such/iris.toml")
		if err != nil {
			t.Fatalf("LoadTOMLFile(absent) = %v, want nil", err)
		}
		if res.Layer != (config.Layer{}) {
			t.Errorf("absent file produced a non-empty layer: %+v", res.Layer)
		}
	})

	t.Run("LoadTOMLFile reads a present file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "iris.toml")
		if err := os.WriteFile(path, []byte("socket = \"/loaded.sock\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := config.LoadTOMLFile(path)
		if err != nil {
			t.Fatalf("LoadTOMLFile: %v", err)
		}
		got := config.Resolve(config.Defaults(""), res.Layer, config.Layer{}, config.Layer{})
		if got.Socket != "/loaded.sock" {
			t.Errorf("Socket = %q, want /loaded.sock from the loaded file", got.Socket)
		}
	})
}
