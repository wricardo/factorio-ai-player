package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Belt stretches are the sequential runs of belts a factory is actually built
// from: one chain where every belt outputs into the next, through underground
// pairs and linked belts, cut at splitters and at side-loads. Each run becomes a
// :BeltStretch node so "which machines put items into this belt" is one short
// query instead of a BELT_OUT*0..600 walk with a guessed hop bound.
//
// Only entities that physically move items connect to a stretch (inserters,
// drills, loaders, splitter ports, a side-loading tail belt). Machines and
// chests are reached through their own inserter links, never linked directly.

const beltStretchLabel = "BeltStretch"

var graphBeltFamily = map[string]bool{
	"transport-belt": true, "underground-belt": true, "splitter": true, "loader": true,
	"loader-1x1": true, "linked-belt": true, "lane-splitter": true,
}

// graphTravel is the engine's belt travel vector per direction.
var graphTravel = map[int]struct{ x, y float64 }{
	0: {0, -1}, 4: {1, 0}, 8: {0, 1}, 12: {-1, 0},
}

type graphBelt struct {
	id, typ, shape string
	surface        string
	x, y           float64
	dir            int
	unit           int64
	hasUnit        bool
}

// graphBeltLink is one engine belt connection between two entities.
type graphBeltLink struct {
	from, to      string
	kind          string
	fromPort      string
	fromPortIndex string
	toPort        string
	toPortIndex   string
}

type graphSideload struct {
	from string
	side string
}

type graphStretch struct {
	id      string
	surface string
	members []string
	index   map[string]int
	endKind string
}

func (b *graphBuilder) beltOf(id string) (graphBelt, bool) {
	n, ok := b.owned[id]
	if !ok {
		return graphBelt{}, false
	}
	typ, _ := n.Props["type"].(string)
	if !graphBeltFamily[typ] {
		return graphBelt{}, false
	}
	belt := graphBelt{id: id, typ: typ}
	belt.x, _ = n.Props["x"].(float64)
	belt.y, _ = n.Props["y"].(float64)
	if v, ok := n.Props["direction"].(int); ok {
		belt.dir = v
	}
	belt.shape, _ = n.Props["beltShape"].(string)
	if v, ok := n.Props["unit"].(int64); ok {
		belt.unit, belt.hasUnit = v, true
	}
	if s, ok := n.Props["surface"].(string); ok {
		belt.surface = s
	}
	return belt, true
}

// beltLinks returns the engine belt connections of the finished edge set. Line
// links are junction metadata, not item flow, so they never join a stretch.
func (b *graphBuilder) beltLinks() []graphBeltLink {
	out := make([]graphBeltLink, 0, len(b.edges))
	for _, e := range b.edges {
		if e.Type != "BELT_OUT" {
			continue
		}
		kind, _ := e.Props["kind"].(string)
		if kind == "line" {
			continue
		}
		l := graphBeltLink{from: e.From, to: e.To, kind: kind}
		l.fromPort, _ = e.Props["fromPort"].(string)
		l.fromPortIndex, _ = e.Props["fromPortIndex"].(string)
		l.toPort, _ = e.Props["toPort"].(string)
		l.toPortIndex, _ = e.Props["toPortIndex"].(string)
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].from != out[j].from {
			return out[i].from < out[j].from
		}
		if out[i].to != out[j].to {
			return out[i].to < out[j].to
		}
		return out[i].kind < out[j].kind
	})
	return out
}

// sideOf returns the side of belt owner that other lies on, relative to the
// belt's travel: negative dot is the right side, matching the Lua graph_side.
func beltSide(owner graphBelt, ox, oy float64) string {
	dir, ok := graphTravel[owner.dir]
	if !ok {
		return ""
	}
	dx, dy := ox-owner.x, oy-owner.y
	dot := dx*dir.y - dy*dir.x
	if dot > 0.001 {
		return "left"
	}
	if dot < -0.001 {
		return "right"
	}
	return ""
}

func graphNearlyEqual(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// behind reports whether other sits exactly one tile behind owner: the tile a
// belt receives a straight continuation from.
func behind(owner, other graphBelt) bool {
	dir, ok := graphTravel[owner.dir]
	if !ok {
		return false
	}
	return graphNearlyEqual(other.x, owner.x-dir.x) && graphNearlyEqual(other.y, owner.y-dir.y)
}

// buildStretches derives :BeltStretch nodes, membership, flow and transfer
// edges. It runs after every raw link became a BELT_OUT/first-class edge, and it
// converts nothing else, so an unknown link still fails in finish.
func (b *graphBuilder) buildStretches() error {
	links := b.beltLinks()
	prev := map[string][]graphBeltLink{}
	next := map[string][]graphBeltLink{}
	for _, l := range links {
		if _, ok := b.beltOf(l.from); !ok {
			continue
		}
		if _, ok := b.beltOf(l.to); !ok {
			// A loader unloading into a chest ends the run.
			l.kind = "loader"
		}
		prev[l.to] = append(prev[l.to], l)
		next[l.from] = append(next[l.from], l)
	}

	// continuing predecessor per belt, plus the side-loads that end at it.
	continueFrom := map[string]string{}
	sideloads := map[string][]graphSideload{}
	for id := range prev {
		belt, ok := b.beltOf(id)
		if !ok {
			continue
		}
		var undergroundLink *graphBeltLink
		var candidates []graphBeltLink
		for _, l := range prev[id] {
			p, ok := b.beltOf(l.from)
			if !ok {
				continue
			}
			if l.kind == "underground" || l.kind == "linked" {
				lf := l
				undergroundLink = &lf
				continue
			}
			if p.typ == "splitter" {
				// A splitter output starts a new stretch.
				continue
			}
			candidates = append(candidates, l)
		}
		chosen := ""
		switch {
		case undergroundLink != nil:
			chosen = undergroundLink.from
		case belt.shape == "left" || belt.shape == "right":
			// A curved belt continues from its single input side; geometry cannot
			// tell a curve from a side-load, the engine's belt_shape can.
			if len(candidates) == 1 {
				chosen = candidates[0].from
			} else {
				for _, l := range candidates {
					p, _ := b.beltOf(l.from)
					if behind(belt, p) {
						chosen = l.from
					}
				}
			}
		default:
			for _, l := range candidates {
				p, _ := b.beltOf(l.from)
				if behind(belt, p) {
					chosen = l.from
				}
			}
		}
		if chosen != "" {
			continueFrom[id] = chosen
		}
		for _, l := range candidates {
			if l.from == chosen {
				continue
			}
			p, _ := b.beltOf(l.from)
			sideloads[id] = append(sideloads[id], graphSideload{from: l.from, side: beltSide(belt, p.x, p.y)})
		}
	}

	// Walk every run from its head. A head has no continuing predecessor: the
	// start of a line, a T-junction, or the belt right after a splitter.
	// Splitters are boundaries, never members: each splitter output starts its
	// own run, so a splitter itself belongs to no stretch.
	var belts []string
	for id := range b.owned {
		if belt, ok := b.beltOf(id); ok && belt.typ != "splitter" {
			belts = append(belts, id)
		}
	}
	sort.Slice(belts, func(i, j int) bool {
		bi, _ := b.beltOf(belts[i])
		bj, _ := b.beltOf(belts[j])
		if bi.surface != bj.surface {
			return bi.surface < bj.surface
		}
		if bi.x != bj.x {
			return bi.x < bj.x
		}
		return bi.y < bj.y
	})

	stretchOf := map[string]*graphStretch{}
	stretches := []*graphStretch{}
	var walk func(start string) *graphStretch
	walk = func(start string) *graphStretch {
		if s, ok := stretchOf[start]; ok {
			return s
		}
		startBelt, _ := b.beltOf(start)
		s := &graphStretch{surface: startBelt.surface, index: map[string]int{}}
		s.id = stretchID(startBelt)
		for cur := start; cur != ""; {
			if _, seen := stretchOf[cur]; seen {
				s.endKind = "loop"
				break
			}
			s.index[cur] = len(s.members)
			s.members = append(s.members, cur)
			stretchOf[cur] = s
			cur = continuingSuccessor(b, next[cur], continueFrom)
		}
		if s.endKind == "" {
			s.endKind = endKindOf(b, s, next, sideloads)
		}
		stretches = append(stretches, s)
		return s
	}
	for _, id := range belts {
		if _, ok := stretchOf[id]; ok {
			continue
		}
		if _, continues := continueFrom[id]; continues {
			continue
		}
		walk(id)
	}
	// Belts left over are loops with no head; start each at its first belt.
	for _, id := range belts {
		if _, ok := stretchOf[id]; ok {
			continue
		}
		walk(id)
	}

	b.emitStretches(stretches, stretchOf, sideloads, next)
	return nil
}

func stretchID(belt graphBelt) string {
	key := belt.id
	if belt.hasUnit {
		key = strconv.FormatInt(belt.unit, 10)
	}
	return belt.surface + ":stretch:" + key
}

// continuingSuccessor is the one successor that continues this run: the belt
// whose continuing predecessor is the belt we are standing on.
func continuingSuccessor(b *graphBuilder, links []graphBeltLink, continueFrom map[string]string) string {
	for _, l := range links {
		if to, ok := b.beltOf(l.to); !ok || to.typ == "splitter" {
			continue
		}
		if continueFrom[l.to] == l.from {
			return l.to
		}
	}
	return ""
}

// endKindOf classifies how a run ends: into a splitter, by loading onto another
// run from the side, into a loader/chest, or at nothing.
func endKindOf(b *graphBuilder, s *graphStretch, next map[string][]graphBeltLink, sideloads map[string][]graphSideload) string {
	tail := s.members[len(s.members)-1]
	for _, l := range next[tail] {
		if to, ok := b.beltOf(l.to); ok && to.typ == "splitter" {
			return "splitter"
		}
		if _, ok := b.beltOf(l.to); !ok {
			return "loader"
		}
	}
	for _, join := range sideloads {
		for _, sl := range join {
			if sl.from == tail {
				return "sideload"
			}
		}
	}
	return "deadend"
}

// emitStretches writes the derived nodes and every relationship that touches a
// stretch. Inserters, drills, loaders and splitter ports are the only transfer
// endpoints; machines and chests stay reachable through their own inserter.
func (b *graphBuilder) emitStretches(stretches []*graphStretch, stretchOf map[string]*graphStretch,
	sideloads map[string][]graphSideload, next map[string][]graphBeltLink) {
	sort.Slice(stretches, func(i, j int) bool { return stretches[i].id < stretches[j].id })
	for _, s := range stretches {
		head, _ := b.beltOf(s.members[0])
		tail, _ := b.beltOf(s.members[len(s.members)-1])
		props := map[string]any{
			"id": s.id, "surface": s.surface, "force": b.force, "snapshot": b.snapshot,
			"scope": b.scope(s.surface), "belts": len(s.members), "endKind": s.endKind,
		}
		undergrounds, itemsLeft, itemsRight, leftCount, rightCount := 0, map[string]int64{}, map[string]int64{}, int64(0), int64(0)
		mixedLeft, mixedRight := false, false
		for _, id := range s.members {
			n := b.owned[id]
			if n == nil {
				continue
			}
			if typ, _ := n.Props["type"].(string); typ == "underground-belt" {
				undergrounds++
			}
			sumItems(itemsLeft, n.Props["left"])
			sumItems(itemsRight, n.Props["right"])
			if v, ok := n.Props["leftCount"].(int64); ok {
				leftCount += v
			}
			if v, ok := n.Props["rightCount"].(int64); ok {
				rightCount += v
			}
			if v, ok := n.Props["mixedLeft"].(bool); ok && v {
				mixedLeft = true
			}
			if v, ok := n.Props["mixedRight"].(bool); ok && v {
				mixedRight = true
			}
		}
		props["undergrounds"] = undergrounds
		props["leftCount"] = leftCount
		props["rightCount"] = rightCount
		if len(itemsLeft) > 0 {
			props["itemsLeft"] = itemList(itemsLeft)
		}
		if len(itemsRight) > 0 {
			props["itemsRight"] = itemList(itemsRight)
		}
		if mixedLeft {
			props["mixedLeft"] = true
		}
		if mixedRight {
			props["mixedRight"] = true
		}
		addBeltRef(props, "head", head)
		addBeltRef(props, "tail", tail)
		if b.derived == nil {
			b.derived = map[string]*graphNodeOut{}
		}
		b.derived[s.id] = &graphNodeOut{ID: s.id, Labels: []string{beltStretchLabel}, Props: props}

		for id, index := range s.index {
			b.edge("IN_STRETCH", id, "Entity", s.id, beltStretchLabel, "", map[string]any{"index": index}, s.surface)
		}
	}

	// Stretch-to-stretch flow: splitter ports and side-loads. One edge type and
	// one direction keeps "walk upstream through any join" a single *0..N hop.
	for _, s := range stretches {
		if s.endKind != "splitter" {
			continue
		}
		tail := s.members[len(s.members)-1]
		for _, l := range next[tail] {
			splitter, ok := b.beltOf(l.to)
			if !ok || splitter.typ != "splitter" {
				continue
			}
			for _, out := range next[splitter.id] {
				target, ok := stretchOf[out.to]
				if !ok || target.id == s.id {
					continue
				}
				b.edge("FLOWS_INTO", s.id, beltStretchLabel, target.id, beltStretchLabel, "splitter|"+out.fromPort,
					map[string]any{"via": "splitter", "viaUnit": splitter.unit, "fromPort": out.fromPort,
						"fromPortIndex": out.fromPortIndex, "atUnit": out.to}, s.surface)
			}
		}
	}
	for belt, joins := range sideloads {
		beltInfo, ok := b.beltOf(belt)
		if !ok {
			continue
		}
		target, ok := stretchOf[belt]
		if !ok {
			continue
		}
		for _, sl := range joins {
			source, ok := stretchOf[sl.from]
			if !ok || source.id == target.id {
				continue
			}
			b.edge("FLOWS_INTO", source.id, beltStretchLabel, target.id, beltStretchLabel, "sideload|"+belt,
				map[string]any{"via": "sideload", "atUnit": beltInfo.unit, "side": sl.side}, beltInfo.surface)
			// The side-loading tail belt is the transferring entity, never the
			// machine behind it.
			b.edge("FEEDS_STRETCH", sl.from, "Entity", target.id, beltStretchLabel, "sideload|"+sl.from,
				map[string]any{"via": "sideload", "atUnit": beltInfo.unit, "side": sl.side}, beltInfo.surface)
		}
	}
	// Splitter ports are transferring endpoints in both directions.
	for _, l := range b.beltLinks() {
		from, okFrom := b.beltOf(l.from)
		to, okTo := b.beltOf(l.to)
		if okFrom && from.typ == "splitter" && okTo {
			if s, ok := stretchOf[l.to]; ok {
				b.edge("FEEDS_STRETCH", l.from, "Entity", s.id, beltStretchLabel, "port|"+l.fromPort,
					map[string]any{"via": "splitter", "fromPort": l.fromPort, "fromPortIndex": l.fromPortIndex, "atUnit": to.unit}, to.surface)
			}
		}
		if okTo && to.typ == "splitter" && okFrom {
			if s, ok := stretchOf[l.from]; ok {
				b.edge("TAKES_FROM_STRETCH", l.to, "Entity", s.id, beltStretchLabel, "port|"+l.toPort,
					map[string]any{"via": "splitter", "toPort": l.toPort, "toPortIndex": l.toPortIndex, "atUnit": from.unit}, from.surface)
			}
		}
	}

	// Inserter and drill transfers onto and off member belts.
	for _, e := range b.edges {
		var beltID string
		var targetType, transferType string
		switch e.Type {
		case "DROPS_TO":
			if _, ok := b.beltOf(e.To); !ok {
				continue
			}
			beltID, targetType, transferType = e.To, "FEEDS_STRETCH", ""
		case "DROPS_ON":
			if _, ok := b.beltOf(e.To); !ok {
				continue
			}
			beltID, targetType = e.To, "FEEDS_STRETCH"
		case "PICKS_FROM":
			// finish() stores PICKS_FROM as inserter -> source, so the belt is To.
			if _, ok := b.beltOf(e.To); !ok {
				continue
			}
			beltID, targetType, transferType = e.To, "TAKES_FROM_STRETCH", e.From
		default:
			continue
		}
		s, ok := stretchOf[beltID]
		if !ok {
			continue
		}
		belt, _ := b.beltOf(beltID)
		props := map[string]any{"atUnit": belt.unit, "index": s.index[beltID]}
		if side, _ := e.Props["side"].(string); side != "" {
			props["side"] = side
		}
		who := e.From
		if e.Type == "PICKS_FROM" {
			who = transferType
		}
		if who == "" {
			continue
		}
		b.edge(targetType, who, "Entity", s.id, beltStretchLabel, "", props, belt.surface)
	}
}

func addBeltRef(props map[string]any, prefix string, belt graphBelt) {
	if belt.id == "" {
		return
	}
	props[prefix+"Unit"] = belt.unit
	props[prefix+"X"] = belt.x
	props[prefix+"Y"] = belt.y
}

func sumItems(acc map[string]int64, value any) {
	list, ok := value.([]string)
	if !ok {
		return
	}
	for _, entry := range list {
		name, count := entry, int64(0)
		if i := strings.LastIndex(entry, ":"); i >= 0 {
			name = entry[:i]
			if n, err := strconv.ParseInt(entry[i+1:], 10, 64); err == nil {
				count = n
			}
		}
		acc[name] += count
	}
}

func itemList(counts map[string]int64) []string {
	out := make([]string, 0, len(counts))
	for name, count := range counts {
		out = append(out, fmt.Sprintf("%s:%d", name, count))
	}
	sort.Strings(out)
	return out
}
