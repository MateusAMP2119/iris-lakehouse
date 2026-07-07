//go:build conformance

package conformance

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"

	"github.com/MateusAMP2119/iris-engine-cli/internal/pg"
)

// This file is E06.7 Live wipe closure: the epic's final conformance leg, proving
// `iris workload wipe [<pipeline>]` against a REAL database (specification
// sections 5, 12 and 14). It exercises the live wipe executor (pg.ExecuteWipe)
// the way E06.6's promotion leg exercises pg.ExecutePromotionFlip: it stands up a
// bare Postgres cluster, provisions the partitioned journal, the real
// iris.capture() function, a user table with the three per-operation capture
// triggers, and a pipeline writer role through the live pg path, drives real
// captured writes, then runs the live wipe and asserts the outcome from an
// independent admin connection. The full binary/daemon wiring of the command
// rides E13's acceptance scenario; this leg proves what only a live database can:
// wipe is one atomic transaction, exactly reverts in-scope disposable rows,
// never clobbers permanent writes, and retains every journal row.
//
// Contracts covered here:
//   - S12/wipe-reverts-unpromoted-keeps-journal
//   - S05/wipe-never-clobbers-permanent
//   - S05/wipe-atomic-transaction
//   - S14/capture-overhead-budget

const (
	wipeWriter   = "iris_wipe_writer"
	wipeWriterPW = "writer_pw"
)

// wipeCluster is a stood-up bare Postgres cluster provisioned through the live pg
// path for the wipe legs: the admin data-database client (which runs the wipe), an
// independent admin read connection (asserting the outcome), and a writer-role DSN
// the run connections derive from.
type wipeCluster struct {
	ctx           context.Context
	client        *pg.Client
	admin         *pgx.Conn
	writerBaseDSN string
}

// newWipeCluster stands up a throwaway cluster, provisions the journal, the real
// capture function, and a least-privilege writer role, and returns the pieces the
// wipe legs drive. Everything is torn down at test cleanup.
func newWipeCluster(t *testing.T) *wipeCluster {
	t.Helper()
	const (
		superuser = "postgres"
		superpw   = "superpw"
	)
	port := freePort(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	runtimeDir := filepath.Join(t.TempDir(), "runtime")

	cluster := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V18).
		Username(superuser).Password(superpw).Database("postgres").
		Port(port).
		DataPath(dataDir).RuntimePath(runtimeDir).
		StartTimeout(90 * time.Second))
	if err := cluster.Start(); err != nil {
		t.Fatalf("start bare Postgres cluster: %v", err)
	}
	t.Cleanup(func() { _ = cluster.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsnTo := func(db, user, pw string) string {
		return fmt.Sprintf("postgres://%s:%s@localhost:%d/%s?sslmode=disable", user, pw, port, db)
	}

	client, err := pg.Connect(ctx, testConnSource{dsn: dsnTo("postgres", superuser, superpw)})
	if err != nil {
		t.Fatalf("pg.Connect (data database): %v", err)
	}
	t.Cleanup(client.Close)

	if err := pg.EnsureJournal(ctx, client); err != nil {
		t.Fatalf("EnsureJournal: %v", err)
	}
	if err := client.EnsureCaptureFunction(ctx); err != nil {
		t.Fatalf("EnsureCaptureFunction: %v", err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '%s'", wipeWriter, wipeWriterPW),
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", pg.DataDatabase, wipeWriter),
		fmt.Sprintf("GRANT USAGE ON SCHEMA iris TO %s", wipeWriter),
		fmt.Sprintf("GRANT EXECUTE ON FUNCTION iris.capture() TO %s", wipeWriter),
	} {
		if err := client.Exec(ctx, stmt); err != nil {
			t.Fatalf("mint writer role (%q): %v", stmt, err)
		}
	}

	admin, err := pgx.Connect(ctx, dsnTo(pg.DataDatabase, superuser, superpw))
	if err != nil {
		t.Fatalf("connect admin read conn: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })

	return &wipeCluster{
		ctx:           ctx,
		client:        client,
		admin:         admin,
		writerBaseDSN: dsnTo(pg.DataDatabase, wipeWriter, wipeWriterPW),
	}
}

// provisionTable creates a user schema and table through the live pg path, installs
// the three per-operation capture triggers (when withTriggers), and grants the
// writer role the field access a pipeline needs to write it.
func (wc *wipeCluster) provisionTable(t *testing.T, schema, table, createDDL string, withTriggers bool) {
	t.Helper()
	for _, stmt := range []string{
		fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", schema),
		createDDL,
	} {
		if err := wc.client.Exec(wc.ctx, stmt); err != nil {
			t.Fatalf("provision (%q): %v", stmt, err)
		}
	}
	if withTriggers {
		for _, trig := range pg.RenderCaptureTriggers(schema, table) {
			if err := wc.client.Exec(wc.ctx, trig); err != nil {
				t.Fatalf("install capture trigger: %v\n%s", err, trig)
			}
		}
	}
	for _, grant := range []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, wipeWriter),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s.%s TO %s", schema, table, wipeWriter),
	} {
		if err := wc.client.Exec(wc.ctx, grant); err != nil {
			t.Fatalf("grant writer (%q): %v", grant, err)
		}
	}
}

// writerConn opens a fresh writer connection carrying the run id plus the write's
// wipe-eligibility -- the two per-session settings the engine injects on a run's
// data connection at spawn. A disposable run is wipe-eligible (writes born open); a
// permanent run is not (writes born promoted).
func (wc *wipeCluster) writerConn(t *testing.T, runID int64, wipeEligible bool) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(wc.ctx, pg.InjectRunID(wc.writerBaseDSN, runID))
	if err != nil {
		t.Fatalf("connect writer with run id %d: %v", runID, err)
	}
	t.Cleanup(func() { _ = conn.Close(wc.ctx) })
	val := "off"
	if wipeEligible {
		val = "on"
	}
	if _, err := conn.Exec(wc.ctx, fmt.Sprintf("SET %s = '%s'", pg.WipeEligibleSetting, val)); err != nil {
		t.Fatalf("set %s=%s on run %d: %v", pg.WipeEligibleSetting, val, runID, err)
	}
	return conn
}

// TestWipeRevertsUnpromotedKeepsJournal proves the epic's headline safety property
// against a real database: `iris workload wipe` reverts un-promoted disposable data
// while retaining every journal row (specification section 12). A single disposable
// run inserts, updates, and deletes rows; the live wipe rolls all of it back to the
// pre-run state, yet not one journal row is deleted -- every visited entry survives,
// only its undo marker flips to wiped.
//
// spec: S12/wipe-reverts-unpromoted-keeps-journal
func TestWipeRevertsUnpromotedKeepsJournal(t *testing.T) {
	wc := newWipeCluster(t)
	wc.provisionTable(t, "analytics", "orders",
		"CREATE TABLE analytics.orders (id integer PRIMARY KEY, amount numeric NOT NULL)", true)

	const run int64 = 70001
	conn := wc.writerConn(t, run, true) // disposable: writes born open, wipe-eligible
	execWrite(wc.ctx, t, conn, "INSERT INTO analytics.orders (id, amount) VALUES (1, 10), (2, 20), (3, 30)")
	execWrite(wc.ctx, t, conn, "UPDATE analytics.orders SET amount = 25 WHERE id = 2")
	execWrite(wc.ctx, t, conn, "DELETE FROM analytics.orders WHERE id = 3")

	// Pre-wipe: five open journal entries (3 inserts, 1 update, 1 delete), three live
	// rows (1 and the updated 2; 3 was deleted).
	assertCount(wc.ctx, t, wc.admin, 5, "SELECT count(*) FROM public.data_journal WHERE undo='open'")
	assertCount(wc.ctx, t, wc.admin, 2, "SELECT count(*) FROM analytics.orders")

	res, err := wc.client.ExecuteWipe(wc.ctx, pg.WipeTarget{})
	if err != nil {
		t.Fatalf("ExecuteWipe: %v", err)
	}
	if res.Wiped != 5 || res.Skipped != 0 {
		t.Errorf("wipe result Wiped=%d Skipped=%d, want 5, 0", res.Wiped, res.Skipped)
	}

	// Data reverted to the pre-run state: the table is empty (every row was this
	// disposable run's), the delete's pre-image was restored then its insert undone.
	assertCount(wc.ctx, t, wc.admin, 0, "SELECT count(*) FROM analytics.orders")

	// Journal RETAINED in full: no row deleted, only undo markers flipped to wiped.
	assertCount(wc.ctx, t, wc.admin, 5, "SELECT count(*) FROM public.data_journal")
	assertCount(wc.ctx, t, wc.admin, 0, "SELECT count(*) FROM public.data_journal WHERE undo='open'")
	assertCount(wc.ctx, t, wc.admin, 5, "SELECT count(*) FROM public.data_journal WHERE undo='wiped'")
}

// TestWipeNeverClobbersPermanent proves the never-clobbers rule against a real
// database (specification section 5): a bare wipe exactly reverts the disposable
// rows nothing permanent sits on, while every permanent write is preserved. A
// disposable run inserts rows 1..3; a permanent run then updates row 1 (born
// promoted) and inserts a permanent row 4. The wipe reverts rows 2 and 3, but the
// open insert of row 1 is conflict-skipped because the later promoted update is
// still in the row's value -- so the permanent write is never clobbered -- and the
// promoted entries and their row survive untouched.
//
// spec: S05/wipe-never-clobbers-permanent
func TestWipeNeverClobbersPermanent(t *testing.T) {
	wc := newWipeCluster(t)
	wc.provisionTable(t, "analytics", "orders",
		"CREATE TABLE analytics.orders (id integer PRIMARY KEY, amount numeric NOT NULL)", true)

	const (
		disposableRun int64 = 71001
		permanentRun  int64 = 71002
	)
	disp := wc.writerConn(t, disposableRun, true) // disposable: born open
	execWrite(wc.ctx, t, disp, "INSERT INTO analytics.orders (id, amount) VALUES (1, 10), (2, 20), (3, 30)")

	perm := wc.writerConn(t, permanentRun, false) // permanent: born promoted, slim
	execWrite(wc.ctx, t, perm, "UPDATE analytics.orders SET amount = 111 WHERE id = 1")
	execWrite(wc.ctx, t, perm, "INSERT INTO analytics.orders (id, amount) VALUES (4, 40)")

	// Sanity: the permanent run's two writes are born promoted (out of wipe scope);
	// the disposable run's three inserts are open (in scope).
	assertCount(wc.ctx, t, wc.admin, 2,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND undo='promoted'", permanentRun)
	assertCount(wc.ctx, t, wc.admin, 3,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND undo='open'", disposableRun)

	res, err := wc.client.ExecuteWipe(wc.ctx, pg.WipeTarget{})
	if err != nil {
		t.Fatalf("ExecuteWipe: %v", err)
	}

	// Rows 2 and 3 (nothing permanent on top) reverted; row 1 (a promoted update on
	// top) conflict-skipped and left as-is, row 4 (permanent) untouched.
	if res.Wiped != 2 || res.Skipped != 1 {
		t.Errorf("wipe result Wiped=%d Skipped=%d, want 2, 1", res.Wiped, res.Skipped)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].ConflictingRunID != permanentRun {
		t.Errorf("conflicts=%+v, want exactly one naming the permanent run %d", res.Conflicts, permanentRun)
	}

	// Permanent writes NEVER clobbered or dropped: row 1 keeps the permanent value,
	// row 4 is present; the two disposable-only rows are gone.
	assertCount(wc.ctx, t, wc.admin, 1, "SELECT count(*) FROM analytics.orders WHERE id=1 AND amount=111")
	assertCount(wc.ctx, t, wc.admin, 1, "SELECT count(*) FROM analytics.orders WHERE id=4 AND amount=40")
	assertCount(wc.ctx, t, wc.admin, 0, "SELECT count(*) FROM analytics.orders WHERE id IN (2, 3)")
	assertCount(wc.ctx, t, wc.admin, 2, "SELECT count(*) FROM analytics.orders")

	// Journal fully retained; the permanent entries stay promoted, the disposable
	// entries retire to skipped (the contested insert) and wiped (rows 2, 3).
	assertCount(wc.ctx, t, wc.admin, 5, "SELECT count(*) FROM public.data_journal")
	assertCount(wc.ctx, t, wc.admin, 2,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND undo='promoted'", permanentRun)
	assertCount(wc.ctx, t, wc.admin, 1,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND undo='skipped'", disposableRun)
	assertCount(wc.ctx, t, wc.admin, 2,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND undo='wiped'", disposableRun)
}

// TestWipeAtomicTransaction proves the wipe runs in a single data-database
// transaction (specification section 5): a mid-wipe failure leaves NO partial wipe
// applied. A disposable run's writes are captured normally, then one captured
// update entry's pre-image is tampered so restoring it violates the table's CHECK
// constraint. Reverse replay deletes the later insert first, then hits the poisoned
// restore and aborts; because journal and tables co-reside in one transaction, the
// abort rolls the whole wipe back -- the already-deleted row is restored and every
// journal entry is still undo='open'.
//
// spec: S05/wipe-atomic-transaction
func TestWipeAtomicTransaction(t *testing.T) {
	wc := newWipeCluster(t)
	wc.provisionTable(t, "analytics", "orders",
		"CREATE TABLE analytics.orders (id integer PRIMARY KEY, amount numeric NOT NULL CHECK (amount >= 0))", true)

	const run int64 = 72001
	conn := wc.writerConn(t, run, true) // disposable
	execWrite(wc.ctx, t, conn, "INSERT INTO analytics.orders (id, amount) VALUES (1, 10), (2, 20), (3, 30)")
	execWrite(wc.ctx, t, conn, "UPDATE analytics.orders SET amount = 25 WHERE id = 2")
	execWrite(wc.ctx, t, conn, "INSERT INTO analytics.orders (id, amount) VALUES (4, 40)")

	// Poison the update entry's captured pre-image so its restore violates CHECK
	// (amount >= 0). Reverse replay reverts the later insert of row 4 first (a
	// successful step), THEN restores this pre-image and fails: proof the abort
	// unwinds an already-applied step.
	if _, err := wc.admin.Exec(wc.ctx,
		`UPDATE public.data_journal SET pre_image='{"id":2,"amount":-5}'::json WHERE run_id=$1 AND op='update'`, run); err != nil {
		t.Fatalf("tamper pre_image to force a mid-wipe failure: %v", err)
	}

	res, err := wc.client.ExecuteWipe(wc.ctx, pg.WipeTarget{})
	if err == nil {
		t.Fatalf("ExecuteWipe succeeded, want failure from the poisoned restore; result=%+v", res)
	}

	// Atomic rollback: NOTHING committed. Every row is exactly as before the wipe --
	// row 4 (which reverse replay deleted before the failure) is back, row 2 still
	// carries its update -- and no journal entry retired.
	assertCount(wc.ctx, t, wc.admin, 4, "SELECT count(*) FROM analytics.orders")
	assertCount(wc.ctx, t, wc.admin, 1, "SELECT count(*) FROM analytics.orders WHERE id=4 AND amount=40")
	assertCount(wc.ctx, t, wc.admin, 1, "SELECT count(*) FROM analytics.orders WHERE id=2 AND amount=25")
	assertCount(wc.ctx, t, wc.admin, 5, "SELECT count(*) FROM public.data_journal WHERE undo='open'")
	assertCount(wc.ctx, t, wc.admin, 0, "SELECT count(*) FROM public.data_journal WHERE undo IN ('wiped', 'skipped')")

	// And the capture triggers the wipe disables for its reverts are re-enabled on
	// rollback: a fresh disposable write is still captured.
	const probe int64 = 72002
	pc := wc.writerConn(t, probe, true)
	execWrite(wc.ctx, t, pc, "INSERT INTO analytics.orders (id, amount) VALUES (5, 50)")
	assertCount(wc.ctx, t, wc.admin, 1,
		"SELECT count(*) FROM public.data_journal WHERE run_id=$1 AND op='insert'", probe)
}

// TestCaptureOverheadBudget carries the data-durability overhead gate against a
// real database (specification section 14): a promoted bulk insert with always-on
// capture completes within 1.25x of the capture-less baseline. The definitive
// 10M-row acceptance-scenario run lands in E13; this leg owns the contract row and
// proves the budget structurally on a wide-row bulk load, so the slim promoted
// stamp is a small fraction of the user row write. It compares the minimum wall
// time (the most stable estimator: OS/GC noise only ever adds time) of a single
// statement-level captured INSERT...SELECT against the identical insert on a
// trigger-free twin table.
//
// spec: S14/capture-overhead-budget
func TestCaptureOverheadBudget(t *testing.T) {
	wc := newWipeCluster(t)
	// Identical wide-row tables; only the capture triggers differ.
	wc.provisionTable(t, "perf", "captured",
		"CREATE TABLE perf.captured (id bigint PRIMARY KEY, payload text NOT NULL)", true)
	wc.provisionTable(t, "perf", "bare",
		"CREATE TABLE perf.bare (id bigint PRIMARY KEY, payload text NOT NULL)", false)

	const (
		rows = 50000
		reps = 5
	)
	// Promoted (permanent) writer: every stamp born promoted and slim, the cheapest
	// capture and exactly the "promoted bulk insert" the budget names.
	conn := wc.writerConn(t, 73001, false)

	insert := func(table string, resetJournal bool) time.Duration {
		if err := wc.client.Exec(wc.ctx, "TRUNCATE perf."+table); err != nil {
			t.Fatalf("truncate perf.%s: %v", table, err)
		}
		if resetJournal {
			if err := wc.client.Exec(wc.ctx, "TRUNCATE public.data_journal"); err != nil {
				t.Fatalf("truncate journal: %v", err)
			}
		}
		stmt := fmt.Sprintf(
			"INSERT INTO perf.%s (id, payload) SELECT g, repeat('x', 2048) FROM generate_series(1, %d) g",
			table, rows)
		start := time.Now()
		if _, err := conn.Exec(wc.ctx, stmt); err != nil {
			t.Fatalf("bulk insert into perf.%s: %v", table, err)
		}
		return time.Since(start)
	}

	// Warm caches and plans on both paths, untimed.
	insert("captured", true)
	insert("bare", false)

	minDur := func(table string, resetJournal bool) time.Duration {
		best := time.Duration(1) << 62
		for i := 0; i < reps; i++ {
			if d := insert(table, resetJournal); d < best {
				best = d
			}
		}
		return best
	}
	capturedMin := minDur("captured", true)
	bareMin := minDur("bare", false)

	// The captured path really did capture, at stamp cost: one slim promoted stamp
	// per row, no pre-image, from the single statement-level trigger firing.
	assertCount(wc.ctx, t, wc.admin, int64(rows),
		"SELECT count(*) FROM public.data_journal WHERE undo='promoted'")
	assertCount(wc.ctx, t, wc.admin, 0,
		"SELECT count(*) FROM public.data_journal WHERE pre_image IS NOT NULL")

	ratio := float64(capturedMin) / float64(bareMin)
	t.Logf("capture overhead: captured=%s bare=%s ratio=%.3f (budget 1.25x)", capturedMin, bareMin, ratio)
	if ratio > 1.25 {
		t.Errorf("promoted bulk insert with capture ran %.3fx the capture-less baseline, over the 1.25x budget", ratio)
	}
}
