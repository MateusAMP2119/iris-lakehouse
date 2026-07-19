package catalog

import (
	"fmt"
	"path"
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// declFile is the declaration filename inside pack pipeline folders.
const declFile = "iris-declare.yaml"

// packMember is one parsed pack pipeline.
type packMember struct {
	name, path, lane string
	deps             []string
}

// packLane gathers one lane's composer and roster.
type packLane struct {
	composer string   // composer decl path, "" when the lane has none
	order    []string // composer's member order
	members  []string // member names seen in the lane
}

// ApplyOrder derives the pack's declare sequence at pipeline granularity: members
// topo-sorted by in-pack depends_on (composer order breaking ties), each lane's
// composer spliced in right after its first member — the window the 2+ interlock
// requires and the engine's own per-pipeline validation accepts.
func ApplyOrder(p Pack) ([]string, error) {
	members, lanes, err := indexPack(p)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("catalog: pack %q declares no pipelines", p.Name)
	}
	for name, l := range lanes {
		if err := l.validate(name); err != nil {
			return nil, fmt.Errorf("catalog: pack %q: %w", p.Name, err)
		}
	}
	seq, err := topoMembers(members, lanes)
	if err != nil {
		return nil, fmt.Errorf("catalog: pack %q: %w", p.Name, err)
	}
	var out []string
	emitted := map[string]int{}
	for _, m := range seq {
		out = append(out, m.path)
		emitted[m.lane]++
		if l := lanes[m.lane]; l != nil && l.composer != "" && emitted[m.lane] == 1 {
			out = append(out, l.composer)
		}
	}
	return out, nil
}

// indexPack parses the pack's declarations into members and lanes, refusing duplicate pipeline names anywhere in the pack.
func indexPack(p Pack) (map[string]*packMember, map[string]*packLane, error) {
	members := map[string]*packMember{}
	lanes := map[string]*packLane{}
	for _, f := range p.Files {
		if path.Base(f.Path) != declFile {
			continue
		}
		decl, err := declare.ParseDeclaration(f.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("catalog: pack %q: parse %s: %w", p.Name, f.Path, err)
		}
		switch decl.Kind {
		case declare.KindComposer:
			l := laneOf(lanes, decl.Composer.Lane)
			if l.composer != "" {
				return nil, nil, fmt.Errorf("catalog: pack %q: lane %q declares two composers", p.Name, decl.Composer.Lane)
			}
			l.composer = f.Path
			l.order = decl.Composer.Order
		case declare.KindPipeline:
			name := decl.Pipeline.Name
			if prev, dup := members[name]; dup {
				return nil, nil, fmt.Errorf("catalog: pack %q: pipeline %q declared twice (%s and %s); a pipeline belongs to exactly one lane", p.Name, name, prev.path, f.Path)
			}
			lane := decl.Pipeline.Lane
			if lane == "" {
				lane = path.Base(path.Dir(path.Dir(f.Path))) // containment: pipelines/<lane>/<pipeline>/
			}
			members[name] = &packMember{name: name, path: f.Path, lane: lane, deps: decl.Pipeline.DependsOn}
			l := laneOf(lanes, lane)
			l.members = append(l.members, name)
		}
	}
	return members, lanes, nil
}

// laneOf returns the lane's record, creating it on first sight.
func laneOf(lanes map[string]*packLane, name string) *packLane {
	if l, ok := lanes[name]; ok {
		return l
	}
	l := &packLane{}
	lanes[name] = l
	return l
}

// validate checks one lane's shape: the 2+ interlock, and a composer ordering exactly its members.
func (l *packLane) validate(lane string) error {
	if l.composer == "" {
		if len(l.members) > 1 {
			return fmt.Errorf("lane %q has %d members but no composer (the 2+ interlock requires one)", lane, len(l.members))
		}
		if len(l.members) == 0 {
			return fmt.Errorf("lane %q carries no pipelines", lane)
		}
		return nil
	}
	if len(l.members) == 0 {
		return fmt.Errorf("lane %q declares a composer but no members", lane)
	}
	if len(l.order) != len(l.members) {
		return fmt.Errorf("lane %q composer orders %d member(s) but the pack carries %d", lane, len(l.order), len(l.members))
	}
	have := map[string]bool{}
	for _, m := range l.members {
		have[m] = true
	}
	seen := map[string]bool{}
	for _, name := range l.order {
		if !have[name] {
			return fmt.Errorf("lane %q composer orders %q, which the pack does not carry", lane, name)
		}
		if seen[name] {
			return fmt.Errorf("lane %q composer orders %q twice", lane, name)
		}
		seen[name] = true
	}
	return nil
}

// topoMembers orders the members by in-pack depends_on (Kahn), ready set sorted by lane, composer position, then name; a true pipeline cycle refuses.
func topoMembers(members map[string]*packMember, lanes map[string]*packLane) ([]*packMember, error) {
	orderPos := map[string]int{}
	for lane, l := range lanes {
		for i, name := range l.order {
			orderPos[lane+"/"+name] = i
		}
	}
	indeg := map[string]int{}
	downstream := map[string][]string{}
	for name, m := range members {
		indeg[name] += 0
		for _, dep := range m.deps {
			if _, inPack := members[dep]; !inPack {
				continue // cross-pack dependency: the apply-time gate adjudicates it
			}
			indeg[name]++
			downstream[dep] = append(downstream[dep], name)
		}
	}
	less := func(a, b string) bool {
		ma, mb := members[a], members[b]
		if ma.lane != mb.lane {
			return ma.lane < mb.lane
		}
		pa, pb := orderPos[ma.lane+"/"+a], orderPos[mb.lane+"/"+b]
		if pa != pb {
			return pa < pb
		}
		return a < b
	}
	var ready []string
	for name, d := range indeg {
		if d == 0 {
			ready = append(ready, name)
		}
	}
	var out []*packMember
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		next := ready[0]
		ready = ready[1:]
		out = append(out, members[next])
		for _, dn := range downstream[next] {
			if indeg[dn]--; indeg[dn] == 0 {
				ready = append(ready, dn)
			}
		}
	}
	if len(out) != len(members) {
		var stuck []string
		for name, d := range indeg {
			if d > 0 {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		return nil, fmt.Errorf("depends_on cycle among pipelines %v", stuck)
	}
	return out, nil
}
