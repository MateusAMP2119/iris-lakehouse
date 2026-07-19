package catalog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// snapshotMarker separates a snapshot pre-release from its release core.
const snapshotMarker = "-snapshot."

// engineVersion is a parsed engine version: a release core plus an optional snapshot pre-release date.
type engineVersion struct {
	core     [3]int
	snapshot bool
	date     int // YYYYMMDD, snapshots only
}

// Satisfies reports whether engine meets a pack's requires pin; requires must be release-form "vX.Y.Z" (empty always satisfies) and dev builds satisfy everything.
func Satisfies(engine, requires string) (bool, error) {
	if requires == "" {
		return true, nil
	}
	req, err := parseRelease(requires)
	if err != nil {
		return false, fmt.Errorf("catalog: requires %q is not a release version (vX.Y.Z): %w", requires, err)
	}
	if isDevBuild(engine) {
		return true, nil
	}
	v, err := parseEngineVersion(engine)
	if err != nil {
		return false, fmt.Errorf("catalog: engine version %q: %w", engine, err)
	}
	return compareVersions(v, engineVersion{core: req}) >= 0, nil
}

// isDevBuild reports a from-source build ("dev" or "local.<date>.<sha>"), which the gate never blocks.
func isDevBuild(engine string) bool {
	return engine == "dev" || strings.HasPrefix(engine, "local.")
}

// parseRelease parses a strict release core "vX.Y.Z".
func parseRelease(s string) ([3]int, error) {
	var core [3]int
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return core, errors.New("missing v prefix")
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return core, errors.New("want three numeric parts")
	}
	for i, p := range parts {
		n, err := parseNum(p)
		if err != nil {
			return core, err
		}
		core[i] = n
	}
	return core, nil
}

// parseNum parses a non-negative decimal component, digits only.
func parseNum(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty numeric part")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-numeric part %q", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("part %q: %w", s, err)
	}
	return n, nil
}

// parseEngineVersion parses a release "vX.Y.Z" or a snapshot "vX.Y.Z-snapshot.YYYYMMDD.<hex sha>".
func parseEngineVersion(s string) (engineVersion, error) {
	relPart, suffix, snap := strings.Cut(s, snapshotMarker)
	core, err := parseRelease(relPart)
	if err != nil {
		return engineVersion{}, err
	}
	if !snap {
		return engineVersion{core: core}, nil
	}
	dateStr, sha, ok := strings.Cut(suffix, ".")
	if !ok || !isHex(sha) {
		return engineVersion{}, fmt.Errorf("malformed snapshot suffix %q", suffix)
	}
	if len(dateStr) != 8 {
		return engineVersion{}, fmt.Errorf("malformed snapshot date %q", dateStr)
	}
	date, err := parseNum(dateStr)
	if err != nil {
		return engineVersion{}, fmt.Errorf("malformed snapshot date %q", dateStr)
	}
	return engineVersion{core: core, snapshot: true, date: date}, nil
}

// isHex reports whether s is non-empty lowercase hex (a git short sha).
func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// compareVersions orders engine versions: core first, a snapshot below its own release, same-core snapshots by date.
func compareVersions(a, b engineVersion) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			if a.core[i] < b.core[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.snapshot && !b.snapshot:
		return -1
	case !a.snapshot && b.snapshot:
		return 1
	case a.snapshot && b.snapshot && a.date != b.date:
		if a.date < b.date {
			return -1
		}
		return 1
	}
	return 0
}
