package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MateusAMP2119/iris-lakehouse/internal/api"
	"github.com/MateusAMP2119/iris-lakehouse/internal/pg"
	"github.com/MateusAMP2119/iris-lakehouse/internal/store"
)

// writeSchemaTree lays a workspace schemas/ tree down on disk and returns the
// workspace root.
func writeSchemaTree(t *testing.T, tables map[string]string) string {
	t.Helper()
	ws := t.TempDir()
	for path, body := range tables {
		full := filepath.Join(ws, "schemas", path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return ws
}

// fakeBindings serves a scripted write map and pipeline registry.
type fakeBindings struct {
	bindings  []store.WriteBinding
	pipelines []string
	err       error
}

func (f fakeBindings) WriteBindings(context.Context) ([]store.WriteBinding, error) {
	return f.bindings, f.err
}

func (f fakeBindings) RegisteredPipelines(context.Context) ([]string, error) {
	return f.pipelines, f.err
}

// TestTableShapesFromWorkspace proves the declared-shape walk carries BOTH the
// token the operator wrote and the Postgres type it resolves to, along with
// every declared constraint -- the /schemas document is declaration truth, and
// a display-only retype would make the SCHEMA block lie about the workspace.
func TestTableShapesFromWorkspace(t *testing.T) {
	t.Run("table-shapes", func(t *testing.T) {
		ws := writeSchemaTree(t, map[string]string{
			"demo/orders/table.yaml": `schema: demo
table: orders
columns:
  - name: id
    type: bigint
    primary_key: true
  - name: placed_at
    type: timestamptz
  - name: total
    type: numeric(12,2)
  - name: currency
    type: varchar(3)
    unique: true
  - name: settled
    type: bool
    nullable: false
    default: "false"
`,
			"demo/audit/table.yaml": `schema: demo
table: audit
columns:
  - name: id
    type: uuid
    primary_key: true
  - name: note
    type: text
`,
		})

		shapes, ok := newWorkspaceDataSource(ws).TableShapes()
		if !ok {
			t.Fatal("a readable schemas tree must yield shapes")
		}
		if len(shapes) != 2 {
			t.Fatalf("read %d tables, want 2", len(shapes))
		}
		// Ordered schema then table: audit sorts before orders.
		if shapes[0].Table != "audit" || shapes[1].Table != "orders" {
			t.Fatalf("tables = %s, %s, want audit then orders", shapes[0].Table, shapes[1].Table)
		}

		orders := shapes[1]
		if got := orders.PrimaryKey; len(got) != 1 || got[0] != "id" {
			t.Errorf("primary key = %v, want [id]", got)
		}
		want := []api.ColumnShape{
			{Name: "id", Type: "bigint", PgType: "bigint", PrimaryKey: true, Nullable: false},
			{Name: "placed_at", Type: "timestamptz", PgType: "timestamptz", Nullable: true},
			{Name: "total", Type: "numeric(12,2)", PgType: "numeric(12,2)", Nullable: true},
			{Name: "currency", Type: "varchar(3)", PgType: "varchar(3)", Nullable: true, Unique: true},
			{Name: "settled", Type: "bool", PgType: "boolean", Nullable: false, Default: "false"},
		}
		if len(orders.Columns) != len(want) {
			t.Fatalf("orders has %d columns, want %d", len(orders.Columns), len(want))
		}
		for i, w := range want {
			got := orders.Columns[i]
			if got.Name != w.Name || got.Type != w.Type || got.PrimaryKey != w.PrimaryKey ||
				got.Nullable != w.Nullable || got.Unique != w.Unique || got.Default != w.Default {
				t.Errorf("column %d = %+v, want %+v", i, got, w)
			}
			if got.PgType == "" {
				t.Errorf("column %s carries no resolved pg type", got.Name)
			}
			if got.Type == "" {
				t.Errorf("column %s dropped the declared token", got.Name)
			}
		}
	})

	t.Run("an absent schemas tree is an empty workspace, not a failure", func(t *testing.T) {
		shapes, ok := newWorkspaceDataSource(t.TempDir()).TableShapes()
		if !ok {
			t.Fatal("a workspace with no schemas tree must read as empty, not unreadable")
		}
		if len(shapes) != 0 {
			t.Errorf("shapes = %v, want none", shapes)
		}
	})
}

// TestSchemasPlaneComposes proves the listing joins declared shapes to the
// pipelines that write them, deriving role to pipeline forward through
// PipelineRoleName so a grant held by a role no pipeline owns is left out
// rather than credited to one.
func TestSchemasPlaneComposes(t *testing.T) {
	t.Run("schemas-plane", func(t *testing.T) {
		ws := writeSchemaTree(t, map[string]string{
			"demo/orders/table.yaml": "schema: demo\ntable: orders\ncolumns:\n  - name: id\n    type: bigint\n    primary_key: true\n",
		})
		shapes := newWorkspaceDataSource(ws)

		t.Run("write grants become pipeline outputs", func(t *testing.T) {
			p := NewSchemasPlane(shapes, fakeBindings{
				pipelines: []string{"load_orders"},
				bindings: []store.WriteBinding{
					{Role: pg.PipelineRoleName("load_orders"), Schema: "demo", Table: "orders"},
				},
			})
			res, err := p.ListSchemas(context.Background())
			if err != nil {
				t.Fatalf("ListSchemas error = %v", err)
			}
			if len(res.Tables) != 1 {
				t.Fatalf("tables = %+v, want the declared one", res.Tables)
			}
			if len(res.Outputs) != 1 || res.Outputs[0].Pipeline != "load_orders" ||
				res.Outputs[0].Table != "orders" {
				t.Fatalf("outputs = %+v, want load_orders -> demo.orders", res.Outputs)
			}
		})

		t.Run("a grant held by no registered pipeline is not credited to one", func(t *testing.T) {
			p := NewSchemasPlane(shapes, fakeBindings{
				pipelines: []string{"load_orders"},
				bindings: []store.WriteBinding{
					{Role: "iris_pat_reader", Schema: "demo", Table: "orders"},
				},
			})
			res, err := p.ListSchemas(context.Background())
			if err != nil {
				t.Fatalf("ListSchemas error = %v", err)
			}
			if len(res.Outputs) != 0 {
				t.Errorf("outputs = %+v, want none: no pipeline owns that role", res.Outputs)
			}
		})

		t.Run("a failing binding read faults rather than serving half a document", func(t *testing.T) {
			boom := errors.New("meta unreachable")
			p := NewSchemasPlane(shapes, fakeBindings{err: boom})
			if _, err := p.ListSchemas(context.Background()); err == nil {
				t.Fatal("a failed binding read must propagate")
			}
		})

		t.Run("an unwired binding reader still serves the shapes", func(t *testing.T) {
			res, err := NewSchemasPlane(shapes, nil).ListSchemas(context.Background())
			if err != nil {
				t.Fatalf("ListSchemas error = %v", err)
			}
			if len(res.Tables) != 1 || len(res.Outputs) != 0 {
				t.Errorf("document = %+v, want shapes with no outputs", res)
			}
		})
	})
}
