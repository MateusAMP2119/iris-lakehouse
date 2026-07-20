package catalog

import "testing"

// TestSatisfies proves the engine version gate across the release/snapshot/dev matrix.
func TestSatisfies(t *testing.T) {
	cases := []struct {
		name     string
		engine   string
		requires string
		want     bool
		wantErr  bool
	}{
		{"empty requires always satisfies", "v0.1.0", "", true, false},
		{"empty requires satisfies dev", "dev", "", true, false},
		{"empty requires skips engine parse", "garbage", "", true, false},
		{"release equal", "v1.2.3", "v1.2.3", true, false},
		{"release patch above", "v1.2.4", "v1.2.3", true, false},
		{"release patch below", "v1.2.2", "v1.2.3", false, false},
		{"release minor above", "v1.3.0", "v1.2.9", true, false},
		{"release minor below", "v1.1.9", "v1.2.0", false, false},
		{"release major above", "v2.0.0", "v1.9.9", true, false},
		{"release major below", "v1.9.9", "v2.0.0", false, false},
		{"snapshot of required core sorts below it", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", "v1.2.3", false, false},
		{"snapshot above lower release", "v1.2.4-snapshot.20260701.0a1b2c3d4e5f", "v1.2.3", true, false},
		{"snapshot below higher release", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", "v1.2.4", false, false},
		{"dev build satisfies everything", "dev", "v99.0.0", true, false},
		{"local build satisfies everything", "local.20260701.0a1b2c3d4e5f", "v99.0.0", true, false},
		{"dirty local build satisfies everything", "local.20260701.0a1b2c3d4e5f-dirty", "v99.0.0", true, false},
		{"requires without v prefix", "v1.2.3", "1.2.3", false, true},
		{"requires two parts", "v1.2.3", "v1.2", false, true},
		{"requires non-numeric", "v1.2.3", "v1.2.x", false, true},
		{"requires snapshot form refused", "v1.2.3", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", false, true},
		{"requires word refused", "dev", "latest", false, true},
		{"malformed engine", "nightly", "v1.2.3", false, true},
		{"engine two parts", "v1.2", "v1.2.3", false, true},
		{"engine bad snapshot date", "v1.2.3-snapshot.2026.0a1b2c3d4e5f", "v1.2.3", false, true},
		{"engine bad snapshot sha", "v1.2.3-snapshot.20260701.XYZ", "v1.2.3", false, true},
		{"engine snapshot missing sha", "v1.2.3-snapshot.20260701", "v1.2.3", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Satisfies(tc.engine, tc.requires)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Satisfies(%q, %q) = %v, nil; want error", tc.engine, tc.requires, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Satisfies(%q, %q): unexpected error: %v", tc.engine, tc.requires, err)
			}
			if got != tc.want {
				t.Errorf("Satisfies(%q, %q) = %v, want %v", tc.engine, tc.requires, got, tc.want)
			}
		})
	}
}

// TestCompareVersions proves the ordering the gate rests on, including same-core snapshot dates.
func TestCompareVersions(t *testing.T) {
	mustParse := func(t *testing.T, s string) engineVersion {
		t.Helper()
		v, err := parseEngineVersion(s)
		if err != nil {
			t.Fatalf("parseEngineVersion(%q): %v", s, err)
		}
		return v
	}
	cases := []struct {
		name string
		a, b string
		want int
	}{
		{"equal releases", "v1.2.3", "v1.2.3", 0},
		{"core orders releases", "v1.2.3", "v1.10.0", -1},
		{"snapshot below its release", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", "v1.2.3", -1},
		{"snapshot above lower release", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", "v1.2.2", 1},
		{"same-core snapshots order by date", "v1.2.3-snapshot.20260601.0a1b2c3d4e5f", "v1.2.3-snapshot.20260701.ffffffffffff", -1},
		{"same-core same-date snapshots equal", "v1.2.3-snapshot.20260701.0a1b2c3d4e5f", "v1.2.3-snapshot.20260701.ffffffffffff", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := mustParse(t, tc.a), mustParse(t, tc.b)
			if got := compareVersions(a, b); got != tc.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			if got := compareVersions(b, a); got != -tc.want {
				t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}
