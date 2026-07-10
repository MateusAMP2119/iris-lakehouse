package pg

import (
	"strings"
	"testing"

	"github.com/MateusAMP2119/iris-engine-cli/internal/declare"
)

// TestRenderProvisionPipelineRoleGrantsCapture proves the pipeline-role provisioning
// path grants capture reachability out of the box (specification section 4: capture is
// always on, every role). A freshly provisioned least-privilege role must be able to
// reach the always-on iris.capture() function -- its write fires the per-table capture
// trigger, which calls the function -- so provisioning issues USAGE on the iris schema
// and EXECUTE on iris.capture() for every role, independent of any declared field
// grant. These grants are what E06.2's capture-emission proof relied on as explicit
// test setup before; now they ride provisioning.
//
// spec: S04/pipeline-role-reaches-capture
func TestRenderProvisionPipelineRoleGrantsCapture(t *testing.T) {
	role := PipelineRoleName("ingest")

	// A role with NO declared field grants still gets capture reachability: the grants
	// are pipeline-independent, not derived from reads/writes.
	stmts, err := renderProvisionPipelineRole(RoleProvision{
		Role:          role,
		CredentialDDL: "ALTER ROLE " + quoteIdentifier(role) + " PASSWORD 'x';",
		MetaDatabase:  "meta",
		DataDatabase:  "data",
	})
	if err != nil {
		t.Fatalf("renderProvisionPipelineRole: %v", err)
	}

	wantUsage := "GRANT USAGE ON SCHEMA iris TO " + quoteIdentifier(role) + ";"
	wantExec := "GRANT EXECUTE ON FUNCTION iris.capture() TO " + quoteIdentifier(role) + ";"
	if !containsExactly(stmts, wantUsage, 1) {
		t.Errorf("provisioning must grant iris-schema USAGE exactly once; want %q in\n%s", wantUsage, strings.Join(stmts, "\n"))
	}
	if !containsExactly(stmts, wantExec, 1) {
		t.Errorf("provisioning must grant capture EXECUTE exactly once; want %q in\n%s", wantExec, strings.Join(stmts, "\n"))
	}

	// Capture reachability follows CONNECT: the role can connect before it may execute.
	connectIdx := indexOfContaining(stmts, "GRANT CONNECT ON DATABASE")
	usageIdx := indexOf(stmts, wantUsage)
	execIdx := indexOf(stmts, wantExec)
	if connectIdx < 0 || usageIdx < connectIdx || execIdx < connectIdx {
		t.Errorf("capture grants must come after GRANT CONNECT; connect@%d usage@%d exec@%d", connectIdx, usageIdx, execIdx)
	}

	// A role WITH declared field grants gets capture reachability too, and exactly once.
	withGrants, err := renderProvisionPipelineRole(RoleProvision{
		Role:          role,
		CredentialDDL: "ALTER ROLE " + quoteIdentifier(role) + " PASSWORD 'x';",
		MetaDatabase:  "meta",
		DataDatabase:  "data",
		Grants: []declare.FieldGrant{
			{Schema: "analytics", Table: "orders", Field: "amount", Access: declare.AccessRead},
		},
	})
	if err != nil {
		t.Fatalf("renderProvisionPipelineRole with grants: %v", err)
	}
	if !containsExactly(withGrants, wantUsage, 1) || !containsExactly(withGrants, wantExec, 1) {
		t.Errorf("capture reachability must appear exactly once regardless of field grants:\n%s", strings.Join(withGrants, "\n"))
	}
}

// TestRenderCaptureReachabilityGrants proves the shared helper renders exactly the two
// idempotent capture-reachability grants, with the role as a quoted identifier and the
// engine-owned iris schema and iris.capture() function named.
//
// spec: S04/pipeline-role-reaches-capture
func TestRenderCaptureReachabilityGrants(t *testing.T) {
	got := RenderCaptureReachabilityGrants(`weird"role`)
	want := []string{
		`GRANT USAGE ON SCHEMA iris TO "weird""role";`,
		`GRANT EXECUTE ON FUNCTION iris.capture() TO "weird""role";`,
	}
	if len(got) != len(want) {
		t.Fatalf("RenderCaptureReachabilityGrants returned %d statements, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("grant[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func containsExactly(stmts []string, target string, want int) bool {
	n := 0
	for _, s := range stmts {
		if s == target {
			n++
		}
	}
	return n == want
}

func indexOf(stmts []string, target string) int {
	for i, s := range stmts {
		if s == target {
			return i
		}
	}
	return -1
}

func indexOfContaining(stmts []string, sub string) int {
	for i, s := range stmts {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}
