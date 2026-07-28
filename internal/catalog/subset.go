package catalog

import (
	"fmt"
	"path"
	"strings"
)

// This file narrows a pack to the pipelines someone actually asked for. A pack
// is the unit a catalog ships and the unit its index pins, but it is not always
// the unit anyone wants: a pack of twenty-two scrapers is a shelf, not a single
// purchase. Subset produces a smaller Pack, and everything downstream --
// ApplyOrder, both preflights, Materialize -- runs on it unchanged.
//
// Nothing here rewrites a pack's bytes. Every file that survives is the file the
// index pinned and the fetch verified, so a narrowed install carries exactly the
// provenance a whole one does. That is what decides the lane rule below.

// Subset returns p reduced to the named pipelines plus everything they cannot be
// applied without. An empty selection returns p unchanged, which is what a whole-
// pack install asks for.
//
// The closure is three rules:
//
//   - a selected pipeline's own depends_on, transitively, within the pack. A
//     declaration naming an absent dependency does not apply.
//   - every member of any lane the selection touches. A composer orders its whole
//     roster, so taking half a lane would leave the composer naming a pipeline
//     that is not there -- and the alternative, rewriting the composer to match,
//     would edit a file the index pinned by digest.
//   - every file that is not a pipeline's own: the README and the declared
//     schemas. Tables are what the selected pipelines write into.
func (p Pack) Subset(pipelines []string) (Pack, error) {
	if len(pipelines) == 0 {
		return p, nil
	}
	members, lanes, err := indexPack(p)
	if err != nil {
		return Pack{}, err
	}
	wantLanes := map[string]bool{}
	var walk func(name string, seen map[string]bool) error
	walk = func(name string, seen map[string]bool) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		m, ok := members[name]
		if !ok {
			return fmt.Errorf("catalog: pack %q declares no pipeline %q", p.Name, name)
		}
		wantLanes[m.lane] = true
		for _, dep := range m.deps {
			// A depends_on naming a pipeline outside this pack is the workspace's
			// business, not the subset's: it was already there or it was not.
			if _, inPack := members[dep]; !inPack {
				continue
			}
			if err := walk(dep, seen); err != nil {
				return err
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for _, name := range pipelines {
		if err := walk(name, seen); err != nil {
			return Pack{}, err
		}
	}
	// A touched lane contributes its whole roster, which can pull in members that
	// carry dependencies of their own.
	for changed := true; changed; {
		changed = false
		for lane := range wantLanes {
			for _, name := range lanes[lane].members {
				if seen[name] {
					continue
				}
				before := len(wantLanes)
				if err := walk(name, seen); err != nil {
					return Pack{}, err
				}
				if len(wantLanes) != before {
					changed = true
				}
			}
		}
	}

	// keep maps a file path to whether it survives: a selected member's own
	// directory, a selected lane's composer, or anything outside pipelines/.
	keepDir := map[string]bool{}
	for name := range seen {
		keepDir[path.Dir(members[name].path)] = true
	}
	keepFile := map[string]bool{}
	for lane := range wantLanes {
		if c := lanes[lane].composer; c != "" {
			keepFile[c] = true
		}
	}
	out := p
	out.Files = nil
	for _, f := range p.Files {
		switch {
		case keepFile[f.Path], keepDir[path.Dir(f.Path)]:
			out.Files = append(out.Files, f)
		case !isPipelineFile(f.Path):
			out.Files = append(out.Files, f)
		}
	}
	if len(out.Files) == 0 {
		return Pack{}, fmt.Errorf("catalog: pack %q: selection %v matched no files", p.Name, pipelines)
	}
	return out, nil
}

// isPipelineFile reports whether a pack path belongs to a single pipeline or
// lane rather than to the pack as a whole. Everything under pipelines/ is one
// pipeline's or one lane's; the README and schemas/ belong to all of them.
func isPipelineFile(p string) bool {
	return strings.HasPrefix(p, "pipelines/")
}
