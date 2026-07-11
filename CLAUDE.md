# Iris — engine + CLI

Provenance-first data engine and pipeline orchestrator, one Go binary (`cmd/iris`).
Source of truth: `docs/Iris Specification Inventory.md` (on any conflict with any other
document, the spec wins). Epics and build order: `docs/Iris Epics.md`. Work items:
`docs/Tasks/`. Live build status: `BUILD_STATE.md`.

## TDD doctrine (non-negotiable)

The spec is the source of truth and the test suite is its executable form. The
implementation is regenerable; the durable assets are the spec and the suite.

1. Every task starts from its contract rows in `spec/contracts.yaml` (one row per
   contract: stable id `Sxx/slug`, doc anchor, tier, status). Add or confirm the rows
   first.
2. Write failing tests for every non-exempt contract at that contract's tier **before
   any implementation**. Tiers: `unit` (pure logic, no I/O), `integration` (fakes and
   local process I/O, no live Postgres), `conformance` (real binary, running daemon,
   real Postgres).
3. Implement to green. Do not weaken a test to make it pass; a test expectation changes
   only with a spec delta.
4. Tests claim contracts via a Go subtest path or a `// spec: <contract-id>` annotation.
5. Every commit message names the contract ids it satisfies.
6. The traceability gate must pass: every non-exempt manifest contract is claimed by a
   test, and every test claims a real contract. Exempt contracts (naming, rationale,
   doctrine) live in the manifest marked `exempt` and need no test.
7. Nothing merges red: full test suite plus traceability gate green before any merge.

## Commands

- Build: `go build ./...`; binary: `go build -o iris ./cmd/iris` (always cgo-free;
  release/cross-compile with `CGO_ENABLED=0`).
- Unit + integration (database-free, what CI runs per Go version):
  `go test -race ./...` — conformance is excluded via the `conformance` build tag.
- Single test: `go test -race -run 'TestName(/subtest)?' ./internal/<pkg>/`.
- Conformance suite (real binary, real Postgres 16+):
  `go test -race -tags conformance -timeout 20m ./internal/conformance/...`
  Requires `IRIS_PG_DSN` pointing at a cluster whose role has CREATEDB + CREATEROLE
  (see `.github/workflows/ci.yml`); without it the suite provisions embedded Postgres
  where it can, but CI parity is the DSN path. Slow (~11m); don't run casually.
- Traceability gate: `go test ./internal/trace/...` (backlog mode, merge-blocking).
  Strict readout: `IRIS_TRACE_STRICT=1 go test -run TestTraceabilityGateStrict -v ./internal/trace/...`.
- Spec lock: the gate fails when `docs/Iris Specification Inventory.md` drifts from
  `spec/inventory.lock`. After an intentional spec delta (only alongside its test
  delta): `IRIS_TRACE_UPDATE_LOCK=1 go test -run TestSpecLockUpdate ./internal/trace`.
- Lint: `golangci-lint run` (config `.golangci.yml`; CI pins the version in
  `ci.yml` — currently v2.12.2).

## Branching rules

- `master`: release line. Only receives epic-checkpoint PRs from `development`.
- `development`: integration line. All issue branches merge here.
- Issue branches: `issue/EXX.Y-short-name`, one per task, cut from `development`,
  checked out in a dedicated git worktree. PR title `EXX.Y <task name>`; PR body lists
  the task's contract ids and its Done-when checklist.
- After each epic completes, a PR `Epic EXX` goes from `development` into `master` and
  waits for human review.
- Respect each task's `Depends on` section. Dependency-independent tasks may proceed in
  parallel worktrees; tasks in one dependency chain are strictly serial.

## Role split

The orchestrator (main session) never writes source or tests; it plans, spawns coder
agents, reviews diffs, runs gates, and handles git/PR state. All implementation work is
done by the `coder` agent (see `.claude/agents/coder.md`) inside the task's worktree.

## Conventions

- Single Go module, application not library: all packages under `internal/`, only
  `cmd/iris` is a main package. `spec/contracts.yaml` at repo root.
- Import graph one direction: `cli` → `daemon`/`api` → `dispatch` → `store`/`pg`/`exec`;
  `archive` beside `dispatch` reusing `store`/`pg`; `declare`, `build`, `pat` leaves.
- Plain idiomatic Go: gofmt/goimports/golangci-lint, `%w` wrapping, no cross-package
  panics, contexts through blocking calls, `slog` only, no mutable package globals,
  table-driven tests, doc comments on exported identifiers.
- Dependencies minimal and cgo-free: pgx, cobra, goccy/go-yaml, argon2id,
  embedded-postgres (or vendored equivalent). No ORM, migration framework, scheduler,
  SQLite, parquet, or cloud clients.
