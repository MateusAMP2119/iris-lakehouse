# BUILD_STATE

Orchestrator resume file. One line per task: status ∈ {todo, in-progress, done}; done
lines carry the PR that actually merged the work into `development`. Epic rows track the
development→master checkpoint PR. Task briefs live in `docs/Tasks/`.

**Status 2026-07-10: all epics complete on development (44d0988).** Traceability
backlog: 0 unclaimed non-exempt contracts; strict gate passes (definition of done met).
Full CI — build matrix, unit+integration (Go 1.25/1.26), golangci-lint, traceability
gate, full conformance suite (real binary, real Postgres, -race) — green.

## Open items

- **Epic checkpoint PRs to master**: none opened yet for E04–E14/E13. All epics are
  complete on development; batch or per-epic checkpoint PRs into `master` await human
  review (per branching rules).
- **Spec departure needing owner sign-off (PR #110)**: engine key persists as workspace
  file `<ws>/.iris/engine.key` (0600, O_EXCL) instead of the spec's
  `ALTER DATABASE meta SET iris.engine_key`, which requires SUPERUSER and was silently
  skipped in external mode (no key existed at runtime). Multi-host HA requires `.iris`
  on shared storage, same model as `objects_path`. Documented in
  `internal/daemon/enginekeyfile.go` and PR #110.
- **Flake hardening (low, CI green)**: under sustained back-to-back local `-race` load,
  `TestMetaReadableWhileRunning` and `TestRetentionPruneUpstreamSurvivorNoViolation`
  (no `freshDatabases`, 30s leader-lock wait) and `TestGoldenLaneRunsAndFailures`
  (45s lane wait) can flake. CI runs the suite once on a fresh runner and is green.
- **E13.7 latent follow-ups** (documented in `lifecycle.go`): read-pool credential is
  single-node (minted per daemon start, last starter wins); endpoint registry is
  populated by live apply only, not reloaded from meta on restart.
- **No cross-host leader advertisement** (flagged by E11.2): a real standby's leader
  hint is `unknown`; the exit-6 envelope carries the guidance shape, the concrete
  address awaits a future advertisement task.

## E00 Conformance Harness and Traceability Gate — epic PR #6 (merged to master)

- [x] E00.1 Manifest seed and doctrine — done (PR #1)
- [x] E00.2 Traceability gate — done (PR #2)
- [x] E00.3 Golden files and fixtures — done (PR #3)
- [x] E00.4 Fakes and process IO — done (PR #5)
- [x] E00.5 Conformance runner and CI — done (PR #4)

## E01 Repo Skeleton, CLI Frame and Config — epic PR #14 (merged to master)

- [x] E01.1 Module and package layout — done (PR #10)
- [x] E01.2 Cobra tree and exit codes — done (PR #12)
- [x] E01.3 Config precedence — done (PR #13)
- [x] E01.4 CI and lint wiring — done (PR #11)

## E02 Engine Install, Daemon and Leadership — epic PR #23 (merged to master)

- [x] E02.1 Meta DDL and schema — done (PR #15)
- [x] E02.2 Admin DSN chain — done (PR #16)
- [x] E02.3 Managed Postgres subprocess — done (PR #17)
- [x] E02.4 Install and uninstall — done (PR #18)
- [x] E02.5 Listeners and daemon protocol — done (PR #19)
- [x] E02.6 Leader election single writer — done (PR #20)
- [x] E02.7 Crash reconciliation — done (PR #22)
- [x] E02.8 Logging and service unit — done (PR #21)

## E03 Declarations, Schemas and Apply — epic PR #36 (merged to master)

- [x] E03.1 Declaration parsing and discovery — done (PR #24)
- [x] E03.2 Lane composer validation — done (PR #27)
- [x] E03.3 Single file targets — done (PR #26)
- [x] E03.4 Dependency graph validation — done (PR #25)
- [x] E03.5 Type mapping and DDL — done (PR #28)
- [x] E03.6 Drift classification — done (PR #30)
- [x] E03.7 Migration ledger sync — done (PR #31)
- [x] E03.8 Idempotent provisioning — done (PR #32)
- [x] E03.9 Registry persistence in meta — done (PR #29)
- [x] E03.10 Apply destroy closure — done (PR #35)

## E04 Roles, Grants and Credentials — epic PR: pending

- [x] E04.1 Access declaration validation — done (PR #34)
- [x] E04.2 Role and credential lifecycle — done (PR #37)
- [x] E04.3 Grant reconcile and drift — done (PR #43; capture-reachability grants added in PR #101, provisioning order fixed in PR #108)
- [x] E04.4 Connection injection and enforcement — done (PR #46; manual-run injection completed in PR #106)

## E05 Dispatcher, Lanes and Dead Letters — epic PR: pending

- [x] E05.1 Exec seam — done (PR #39)
- [x] E05.2 Run environment — done (PR #40)
- [x] E05.3 Run records and states — done (PR #42)
- [x] E05.4 Lane model and walk — done (PR #38)
- [x] E05.5 Gate and consumption — done (PR #44)
- [x] E05.6 Failure propagation — done (PR #45)
- [x] E05.7 Dead letter replay — done (PR #47; daemon route wired in PR #106)
- [x] E05.8 Dead letter drain — done (PR #50; daemon route wired in PR #106)
- [x] E05.9 Retention and pruning — done (PR #54; run_inputs FK-free per spec delta in PR #101)
- [x] E05.10 Manual pipeline run — done (PR #49)
- [x] E05.11 Doctrines and scope — done (PR #88)
- [x] E05.12 Lane runner pass semantics — done (PR #59; production lane loop wired into daemon in PR #103)

## E06 Write Capture, Wipe and Promotion — epic PR: pending

- [x] E06.1 Journal DDL and partitioning — done (PR #48)
- [x] E06.2 Capture trigger emission — done (PR #52)
- [x] E06.3 Run attribution — done (PR #55)
- [x] E06.4 Payload tiers and modes — done (PR #58)
- [x] E06.5 Wipe replay and conflicts — done (PR #60)
- [x] E06.6 Promotion — done (PR #89)
- [x] E06.7 Live wipe closure — done (PR #90)

## E07 Provenance, Journal Lifecycle and Object Store — epic PR: pending

- [x] E07.1 Provenance walk — done (PR #74)
- [x] E07.2 Snapshot pin — done (merged via issue/E07.2-snapshot-pin-rework, commit 603f437; stale PR #78 closed)
- [x] E07.3 Seal and compaction — done (PR #79; real threshold-gated seal in PR #110)
- [x] E07.4 Checkpoint chain and engine key — done (PR #82; real digest/signature/chain in PR #110)
- [x] E07.5 Object store and export — done (PR #86)
- [x] E07.6 Archived reads and destroy closure — done (PR #90; api provenance redeclaration devfix PR #93)

## E08 Build, Artifacts and Modes — epic PR: pending

- [x] E08.1 Recipe inference and matrix — done (PR #62)
- [x] E08.2 Build and artifact storage — done (PR #66, review-fix PR #68)
- [x] E08.3 Promote gating — done (PR #76)
- [x] E08.4 Mode execution and retirement — done (PR #91)

## E09 Read API, Endpoints and PATs — epic PR: pending

- [x] E09.1 PAT store and scopes — done (PR #53)
- [x] E09.2 Endpoint compile and validation — done (PR #56)
- [x] E09.3 Param grammar and paging — done (PR #57)
- [x] E09.4 Envelope and serialization — done (PR #61)
- [x] E09.5 Route mux and auth — done (PR #69)
- [x] E09.6 Endpoint apply lifecycle — done (PR #71; live daemon route in PR #104)
- [x] E09.7 Read pool and SQL safety — done (PR #72; provisioning idempotency devfix PR #107)
- [x] E09.8 Q and data routes — done (PR #77)
- [x] E09.9 NDJSON streaming — done (PR #83)
- [x] E09.10 Read parity closure — done (PR #92; merge-conflict devfix PR #93)

## E10 Destructive Operation Gates — epic PR: pending

- [x] E10.1 Gate and blocker predicates — done (PR #75)
- [x] E10.2 Confirmation flows — done (PR #80)
- [x] E10.3 Remote tiering and failover — done (PR #87)

## E11 High Availability and Failover — epic PR: pending

- [x] E11.1 Leader lock election — done (PR #63)
- [x] E11.2 Standby reads and rejection — done (PR #100)
- [x] E11.3 Promotion and self demotion — done (PR #67)
- [x] E11.4 Host prerequisites and live failover — done (PR #102; wires WithInflightKiller + WithFreshSessions + session-renewal seam)

## E12 Stats, Info and Inspect — epic PR: pending

- [x] E12.1 Stats rollups — done (PR #64; production StatsSource in PR #106)
- [x] E12.2 Info inspect and show — done (PR #70)

## E14 Graph Views and Triage Surface — epic PR: pending (builds before E13)

- [x] E14.1 Ref grammar and triage shows — done (PR #81)
- [x] E14.2 Workload wiring panel — done (PR #84)
- [x] E14.3 Rail renderer and golden files — done (PR #85 merged without the renderer; spec-faithful renderer + S08 graph contracts landed in PR #109)
- [x] E14.4 Read routes and before cursor — done (PR #99)

## E13 Golden Sample and Acceptance — epic PR: pending (the spine; last)

- [x] E13.1 Golden workspace fixture — done (PR #94)
- [x] E13.2 Install and binary boot — done (PR #97)
- [x] E13.3 Lane runs and failures — done (PR #103)
- [x] E13.4 Journal capture and wipe — done (PR #98)
- [x] E13.5 Sealing and archival — done (PR #95; presence-stub seal replaced in PR #110)
- [x] E13.6 Data provenance lineage — done (PR #96)
- [x] E13.7 Endpoint reads and grants — done (PR #104; provisioning devfix PR #107)
- [x] E13.8 Failover and unattended closure — done (PR #111; empties the traceability backlog, gate probe synthesized)

## Cross-cutting devfix/debt PRs (2026-07-09/10 recovery session)

- PR #93 — api provenance redeclaration (post-#92 compile break; development was red)
- PR #101 — run_inputs FK-free per spec delta + capture grants in role provisioning
- PR #105 — provenance CLI readout conformance rewrite (IRIS_DB_URL root cause)
- PR #106 — deadletter plane, production stats, manual-run attribution, tree-wide lint zero
- PR #107 — read-pool/data-PAT provisioning idempotent on PG16+ non-superuser clusters
- PR #108 — conformance lane stability: pipeline-role provisioning self-heals capture schema, suite isolation (freshDatabases hardened), CI timeout 20m
- PR #109 — orphaned S03/S04/S06.1/S08 contracts claimed; spec-faithful rail renderer
- PR #110 — real seal: threshold gating, ed25519-signed checkpoint chain (spec departure noted in Open items)
