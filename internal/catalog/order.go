package catalog

import (
	"fmt"
	"path"
	"sort"

	"github.com/MateusAMP2119/iris-lakehouse/internal/declare"
)

// declFile is the declaration filename inside pack pipeline folders.
const declFile = "iris-declare.yaml"

// laneSet gathers one lane's composer and members while deriving the apply order.
type laneSet struct {
	name     string
	composer string            // composer decl path, "" when the lane has none
	order    []string          // composer's member order
	members  map[string]string // pipeline name -> decl path
	deps     map[string][]string
}

// ApplyOrder derives the pack's declare sequence: per lane first member, composer, remaining members in composer order (the 2+ interlock dictates this); cross-lane depends_on edges order the lanes.
func ApplyOrder(p Pack) ([]string, error) {
	lanes := map[string]*laneSet{}
	pipeLane := map[string]string{}
	for _, f := range p.Files {
		if path.Base(f.Path) != declFile {
			continue
		}
		decl, err := declare.ParseDeclaration(f.Data)
		if err != nil {
			return nil, fmt.Errorf("catalog: pack %q: parse %s: %w", p.Name, f.Path, err)
		}
		switch decl.Kind {
		case declare.KindComposer:
			ls := laneOf(lanes, decl.Composer.Lane)
			if ls.composer != "" {
				return nil, fmt.Errorf("catalog: pack %q: lane %q declares two composers", p.Name, ls.name)
			}
			ls.composer = f.Path
			ls.order = decl.Composer.Order
		case declare.KindPipeline:
			lane := decl.Pipeline.Lane
			if lane == "" {
				lane = path.Base(path.Dir(path.Dir(f.Path))) // containment: pipelines/<lane>/<pipeline>/
			}
			ls := laneOf(lanes, lane)
			if _, dup := ls.members[decl.Pipeline.Name]; dup {
				return nil, fmt.Errorf("catalog: pack %q: pipeline %q declared twice", p.Name, decl.Pipeline.Name)
			}
			ls.members[decl.Pipeline.Name] = f.Path
			ls.deps[decl.Pipeline.Name] = decl.Pipeline.DependsOn
			pipeLane[decl.Pipeline.Name] = lane
		}
	}
	ordered, err := orderLanes(lanes, pipeLane)
	if err != nil {
		return nil, fmt.Errorf("catalog: pack %q: %w", p.Name, err)
	}
	var out []string
	for _, ls := range ordered {
		seq, err := ls.sequence()
		if err != nil {
			return nil, fmt.Errorf("catalog: pack %q: %w", p.Name, err)
		}
		out = append(out, seq...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog: pack %q declares no pipelines", p.Name)
	}
	return out, nil
}

// laneOf returns the lane's set, creating it on first sight.
func laneOf(lanes map[string]*laneSet, name string) *laneSet {
	if ls, ok := lanes[name]; ok {
		return ls
	}
	ls := &laneSet{name: name, members: map[string]string{}, deps: map[string][]string{}}
	lanes[name] = ls
	return ls
}

// sequence returns the lane's own apply order: first member, composer, remaining members.
func (ls *laneSet) sequence() ([]string, error) {
	if ls.composer == "" {
		if len(ls.members) > 1 {
			return nil, fmt.Errorf("lane %q has %d members but no composer (the 2+ interlock requires one)", ls.name, len(ls.members))
		}
		for _, p := range ls.members {
			return []string{p}, nil
		}
		return nil, fmt.Errorf("lane %q carries no pipelines", ls.name)
	}
	if len(ls.members) == 0 {
		return nil, fmt.Errorf("lane %q declares a composer but no members", ls.name)
	}
	if len(ls.order) != len(ls.members) {
		return nil, fmt.Errorf("lane %q composer orders %d member(s) but the pack carries %d", ls.name, len(ls.order), len(ls.members))
	}
	seq := make([]string, 0, len(ls.members)+1)
	for i, name := range ls.order {
		p, ok := ls.members[name]
		if !ok {
			return nil, fmt.Errorf("lane %q composer orders %q, which the pack does not carry", ls.name, name)
		}
		seq = append(seq, p)
		if i == 0 {
			seq = append(seq, ls.composer)
		}
	}
	return seq, nil
}

// orderLanes topo-sorts lanes by their cross-lane depends_on edges, alphabetical when free.
func orderLanes(lanes map[string]*laneSet, pipeLane map[string]string) ([]*laneSet, error) {
	names := make([]string, 0, len(lanes))
	for n := range lanes {
		names = append(names, n)
	}
	sort.Strings(names)
	upstream := map[string]map[string]bool{}
	for _, ls := range lanes {
		for _, deps := range ls.deps {
			for _, dep := range deps {
				from, ok := pipeLane[dep]
				if !ok {
					continue // cross-pack dependency: the apply-time gate adjudicates it
				}
				if from == ls.name {
					continue
				}
				if upstream[ls.name] == nil {
					upstream[ls.name] = map[string]bool{}
				}
				upstream[ls.name][from] = true
			}
		}
	}
	var out []*laneSet
	done := map[string]bool{}
	for len(out) < len(names) {
		progressed := false
		for _, n := range names {
			if done[n] {
				continue
			}
			ready := true
			for dep := range upstream[n] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				done[n] = true
				out = append(out, lanes[n])
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("depends_on cycle across lanes")
		}
	}
	return out, nil
}
