package spec_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-engine-cli/internal/spec"
)

// manifestPath points at the repo-root spec/contracts.yaml relative to this
// package directory (internal/spec), where `go test` runs.
var manifestPath = filepath.Join("..", "..", "spec", "contracts.yaml")

// idRE is the independent test-side check that a contract id is a spec section
// (Sxx or Sxx.y) plus a lowercase-slug, mirroring the manifest's own rule.
var idRE = regexp.MustCompile(`^S\d\d(\.\d+)?/[a-z0-9-]+$`)

// TestManifestRowSchema proves that spec/contracts.yaml parses into one row per
// behavioral Q/A contract, each carrying a stable id (section + slug), a doc
// anchor, a tier, and a status.
//
// spec: S16/manifest-row-schema
func TestManifestRowSchema(t *testing.T) {
	m, err := spec.Load(manifestPath)
	if err != nil {
		t.Fatalf("Load(%s): %v", manifestPath, err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(m.Contracts) == 0 {
		t.Fatal("manifest carries no contracts")
	}

	// Every row carries the four schema fields, well-formed.
	seen := make(map[string]bool, len(m.Contracts))
	for _, c := range m.Contracts {
		if seen[c.ID] {
			t.Errorf("duplicate contract id %q (want one row per contract)", c.ID)
		}
		seen[c.ID] = true
		if !idRE.MatchString(c.ID) {
			t.Errorf("contract %q: id not of form Sxx[.y]/slug", c.ID)
		}
		if c.Anchor == "" || !strings.Contains(c.Anchor, "#") {
			t.Errorf("contract %s: doc anchor %q missing or has no section fragment", c.ID, c.Anchor)
		}
		switch c.Tier {
		case spec.TierUnit, spec.TierIntegration, spec.TierConformance, spec.TierExempt:
		default:
			t.Errorf("contract %s: unknown tier %q", c.ID, c.Tier)
		}
		switch c.Status {
		case spec.StatusUnclaimed, spec.StatusExempt:
		default:
			t.Errorf("contract %s: unknown status %q", c.ID, c.Status)
		}
	}

	// Spot-check this task's own contract rows, seeded from the E00 table.
	spot := []struct {
		id     string
		tier   spec.Tier
		status spec.Status
	}{
		{"S16/manifest-row-schema", spec.TierUnit, spec.StatusUnclaimed},
		{"S16/exempt-needs-no-test", spec.TierUnit, spec.StatusUnclaimed},
		{"S16/spec-driven-doctrine", spec.TierExempt, spec.StatusExempt},
		{"S16/tdd-loop-workflow", spec.TierExempt, spec.StatusExempt},
		{"S16/tier-taxonomy-cheapest", spec.TierExempt, spec.StatusExempt},
	}
	for _, want := range spot {
		c, ok := m.Find(want.id)
		if !ok {
			t.Errorf("contract %s: not present in manifest", want.id)
			continue
		}
		if c.Tier != want.tier {
			t.Errorf("contract %s: tier = %q, want %q", want.id, c.Tier, want.tier)
		}
		if c.Status != want.status {
			t.Errorf("contract %s: status = %q, want %q", want.id, c.Status, want.status)
		}
	}
}

// TestParseValidate exercises the schema rules on small inline manifests.
//
// spec: S16/manifest-row-schema
func TestParseValidate(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "valid mixed manifest",
			yaml: `contracts:
  - id: S16/manifest-row-schema
    anchor: "docs/Iris Specification Inventory.md#16-testing"
    tier: unit
    status: unclaimed
  - id: S16/spec-driven-doctrine
    anchor: "docs/Iris Specification Inventory.md#16-testing"
    tier: exempt
    status: exempt`,
			wantErr: false,
		},
		{
			name:    "empty manifest",
			yaml:    "contracts: []",
			wantErr: true,
		},
		{
			name: "malformed id",
			yaml: `contracts:
  - id: not-a-contract-id
    anchor: "docs#x"
    tier: unit
    status: unclaimed`,
			wantErr: true,
		},
		{
			name: "missing anchor",
			yaml: `contracts:
  - id: S05/wipe-scope-rule
    anchor: ""
    tier: unit
    status: unclaimed`,
			wantErr: true,
		},
		{
			name: "unknown tier",
			yaml: `contracts:
  - id: S05/wipe-scope-rule
    anchor: "docs#x"
    tier: smoke
    status: unclaimed`,
			wantErr: true,
		},
		{
			name: "unknown status",
			yaml: `contracts:
  - id: S05/wipe-scope-rule
    anchor: "docs#x"
    tier: unit
    status: green`,
			wantErr: true,
		},
		{
			name: "duplicate id",
			yaml: `contracts:
  - id: S05/wipe-scope-rule
    anchor: "docs#x"
    tier: unit
    status: unclaimed
  - id: S05/wipe-scope-rule
    anchor: "docs#x"
    tier: unit
    status: unclaimed`,
			wantErr: true,
		},
		{
			name: "exempt tier with unclaimed status",
			yaml: `contracts:
  - id: S16/spec-driven-doctrine
    anchor: "docs#x"
    tier: exempt
    status: unclaimed`,
			wantErr: true,
		},
		{
			name: "unit tier with exempt status",
			yaml: `contracts:
  - id: S16/manifest-row-schema
    anchor: "docs#x"
    tier: unit
    status: exempt`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := spec.Parse([]byte(tt.yaml))
			if err != nil {
				// A parse error is an acceptable validation failure for the
				// negative cases, never for the positive one.
				if !tt.wantErr {
					t.Fatalf("Parse: unexpected error: %v", err)
				}
				return
			}
			gotErr := m.Validate() != nil
			if gotErr != tt.wantErr {
				t.Fatalf("Validate error = %v, want error = %v", m.Validate(), tt.wantErr)
			}
		})
	}
}

// TestExemptNeedsNoTest proves the manifest-side rule: a contract marked exempt
// is valid and satisfiable with no claiming test, while a behavioral contract
// still demands one. (The full two-direction gate lives in E00.2; here only the
// manifest exposes exemption and validation demands no claim for it.)
//
// spec: S16/exempt-needs-no-test
func TestExemptNeedsNoTest(t *testing.T) {
	const src = `contracts:
  - id: S16/manifest-row-schema
    anchor: "docs/Iris Specification Inventory.md#16-testing"
    tier: unit
    status: unclaimed
  - id: S16/spec-driven-doctrine
    anchor: "docs/Iris Specification Inventory.md#16-testing"
    tier: exempt
    status: exempt`

	m, err := spec.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// No test claims anything here, yet the exempt row must still be valid.
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: exempt row rejected with no claiming test: %v", err)
	}

	exempt, ok := m.Find("S16/spec-driven-doctrine")
	if !ok {
		t.Fatal("exempt contract not found")
	}
	if !exempt.IsExempt() {
		t.Error("exempt contract: IsExempt() = false, want true")
	}
	if exempt.NeedsClaimingTest() {
		t.Error("exempt contract: NeedsClaimingTest() = true, want false")
	}

	behavioral, ok := m.Find("S16/manifest-row-schema")
	if !ok {
		t.Fatal("behavioral contract not found")
	}
	if behavioral.IsExempt() {
		t.Error("behavioral contract: IsExempt() = true, want false")
	}
	if !behavioral.NeedsClaimingTest() {
		t.Error("behavioral contract: NeedsClaimingTest() = false, want true")
	}

	// The partitions agree: Testable excludes exempt, Exempt includes it.
	for _, c := range m.Testable() {
		if c.IsExempt() {
			t.Errorf("Testable() returned exempt contract %s", c.ID)
		}
	}
	if got := m.Exempt(); len(got) != 1 || got[0].ID != "S16/spec-driven-doctrine" {
		t.Errorf("Exempt() = %v, want exactly [S16/spec-driven-doctrine]", got)
	}

	// The three doctrine rows in the real manifest are exempt and claim-free.
	real, err := spec.Load(manifestPath)
	if err != nil {
		t.Fatalf("Load(%s): %v", manifestPath, err)
	}
	for _, id := range []string{"S16/spec-driven-doctrine", "S16/tdd-loop-workflow", "S16/tier-taxonomy-cheapest"} {
		c, ok := real.Find(id)
		if !ok {
			t.Errorf("doctrine contract %s: absent from manifest", id)
			continue
		}
		if !c.IsExempt() || c.NeedsClaimingTest() {
			t.Errorf("doctrine contract %s: IsExempt=%v NeedsClaimingTest=%v, want exempt/no-test", id, c.IsExempt(), c.NeedsClaimingTest())
		}
	}
}
