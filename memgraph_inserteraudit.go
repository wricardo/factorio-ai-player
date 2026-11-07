package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// memgraph.inserterAudit: per-inserter topology defects, one pass over every
// inserter that has both a PICKS_FROM and a DROPS_TO edge.
//
//   - SELF_LOOP (BLOCKER): source and destination are the same node.
//   - VOID_PICKUP (CONCERN): the source is an infinity chest with no filters
//     (it provides no items) whose removeUnfilteredItems flag is true. The flag is exported on :Chest nodes
//     by the graph exporter; a chest without the property (an older snapshot,
//     or an engine that did not report it) is never flagged.
//   - BELT_TO_BELT (INFO): source and destination are both :Belt.
//   - DEAD_SOURCE (CONCERN): the source is a :Belt that no BELT_OUT, DROPS_ON
//     or DROPS_TO edge feeds and whose sampled lanes are empty.
//   - FILTER_MISMATCH (CONCERN): the inserter has filters and, given its
//     filterMode, they can never match what the source can provide. What a
//     source can provide is: a machine's recipe products, a chest's CONTAINS and
//     PRODUCES items, the sampled lane items of a belt's stretch. A source that
//     provides nothing known (an empty belt or chest, a machine with no recipe,
//     any other kind of source) is never flagged, because that is an empty
//     source, not a mismatch. A whitelist mismatches when none of the provided
//     items is listed; a blacklist when every provided item is listed.
//
// Topology and sampled contents only: lane contents are a point sample and a
// belt's stretch can carry items the sample missed.

const (
	inserterAuditArmsCypher = `MATCH (i:Inserter) WHERE i.surface = $surface
MATCH (i)-[:PICKS_FROM]->(s)
MATCH (i)-[:DROPS_TO]->(d)
RETURN i, s, d ORDER BY i.unit`

	// inserterAuditUnfedBeltsCypher returns the units of the belts in $units
	// that nothing feeds: no belt flows into them and nothing drops onto them.
	inserterAuditUnfedBeltsCypher = `MATCH (b:Belt) WHERE b.surface = $surface AND b.unit IN $units
AND NOT ()-[:BELT_OUT]->(b) AND NOT ()-[:DROPS_ON]->(b) AND NOT ()-[:DROPS_TO]->(b)
RETURN b.unit AS unit`

	inserterAuditBeltStretchCypher = `MATCH (b:Belt)-[:IN_STRETCH]->(s:BeltStretch) WHERE b.surface = $surface AND b.unit IN $units
RETURN b.unit AS unit, s.id AS stretch`

	// inserterAuditListedItems caps the item names spelled out in a message.
	inserterAuditListedItems = 8
)

// inserterAuditArm is one decoded inserter with its pick and drop nodes.
type inserterAuditArm struct {
	inserter, source, dest                *MemgraphTraceNode
	inserterProps, sourceProps, destProps map[string]any
}

func inserterAuditDecodeNode(v any) (*MemgraphTraceNode, map[string]any, error) {
	n, err := traceNodeFromAny(v)
	if err != nil {
		return nil, nil, err
	}
	m, _ := v.(map[string]any)
	props, _ := m["properties"].(map[string]any)
	return n, props, nil
}

// inserterAuditFilters returns the item names an inserter filters on. The
// exporter writes {index, name, quality} entries; plain strings are accepted
// too.
func inserterAuditFilters(props map[string]any) []string {
	raw, _ := props["filters"].([]any)
	seen := map[string]bool{}
	var out []string
	for _, e := range raw {
		name := ""
		switch f := e.(type) {
		case string:
			name = f
		case map[string]any:
			name, _ = f["name"].(string)
		}
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// inserterAuditBeltEmpty reports whether a belt's sampled lanes hold nothing.
func inserterAuditBeltEmpty(props map[string]any) bool {
	if stretchInt(props["leftCount"])+stretchInt(props["rightCount"]) > 0 {
		return false
	}
	for _, k := range []string{"left", "right", "lanes"} {
		if l, _ := props[k].([]any); len(l) > 0 {
			return false
		}
	}
	return true
}

func inserterAuditList(items []string) string {
	if len(items) > inserterAuditListedItems {
		return strings.Join(items[:inserterAuditListedItems], ", ") + fmt.Sprintf(" and %d more", len(items)-inserterAuditListedItems)
	}
	return strings.Join(items, ", ")
}

func inserterAuditUnits(nodes map[string]*MemgraphTraceNode) []any {
	units := make([]any, 0, len(nodes))
	for _, n := range nodes {
		units = append(units, int64(unitOf(n)))
	}
	sort.Slice(units, func(i, j int) bool { return units[i].(int64) < units[j].(int64) })
	return units
}

// inserterAuditMismatch applies the filter rule documented at the top of the
// file. provided is what the source can put in the inserter's hand.
// inserterAuditVoidChest reports an arm whose source is an infinity chest with
// removeUnfilteredItems true. A chest that also has filters is a normal supply
// chest, so VOID_PICKUP additionally requires the chest to provide no items.
func inserterAuditVoidChest(a *inserterAuditArm) bool {
	return a.source.Kind == "Chest" && a.sourceProps["type"] == "infinity-container" && a.sourceProps["removeUnfilteredItems"] == true
}

func inserterAuditMismatch(filters, provided []string, blacklist bool) bool {
	if len(filters) == 0 || len(provided) == 0 {
		return false
	}
	listed := map[string]bool{}
	for _, f := range filters {
		listed[f] = true
	}
	for _, p := range provided {
		if listed[p] != blacklist {
			return false // whitelist: p is listed; blacklist: p is not listed
		}
	}
	return true
}

func inserterAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return nil, fmt.Errorf("surface %q is not a valid surface name", in.Surface)
	}
	params := map[string]any{"surface": in.Surface}
	res, err := read(ctx, MemgraphQueryInput{Cypher: inserterAuditArmsCypher, Params: params, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, fmt.Errorf("inserter arms: %w", err)
	}
	truncated := res.Truncated
	var arms []*inserterAuditArm
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) < 3 {
			return nil, fmt.Errorf("inserter row has %d columns, want 3", len(row))
		}
		a := &inserterAuditArm{}
		if a.inserter, a.inserterProps, err = inserterAuditDecodeNode(row[0]); err != nil {
			return nil, err
		}
		if a.source, a.sourceProps, err = inserterAuditDecodeNode(row[1]); err != nil {
			return nil, err
		}
		if a.dest, a.destProps, err = inserterAuditDecodeNode(row[2]); err != nil {
			return nil, err
		}
		key := a.inserter.ID + "|" + a.source.ID + "|" + a.dest.ID
		if !seen[key] {
			seen[key] = true
			arms = append(arms, a)
		}
	}

	// Sources whose facts a later check needs, collected once.
	filtered := func(a *inserterAuditArm) []string {
		if use, ok := a.inserterProps["useFilters"].(bool); ok && !use {
			return nil
		}
		return inserterAuditFilters(a.inserterProps)
	}
	beltSources := map[string]*MemgraphTraceNode{}   // every belt an inserter picks from
	filterBelts := map[string]*MemgraphTraceNode{}   // belts picked from by a filtered inserter
	filterSources := map[string]*MemgraphTraceNode{} // machines and chests picked from by a filtered inserter
	for _, a := range arms {
		if a.source.Kind == "Belt" && a.source.ID != a.dest.ID {
			beltSources[a.source.ID] = a.source
		}
		if inserterAuditVoidChest(a) {
			filterSources[a.source.ID] = a.source // its items tell a void chest from a filtered supply chest
		}
		if len(filtered(a)) == 0 {
			continue
		}
		switch a.source.Kind {
		case "Belt":
			filterBelts[a.source.ID] = a.source
		case "Machine", "Chest":
			filterSources[a.source.ID] = a.source
		}
	}

	unfed := map[int64]bool{}
	if len(beltSources) > 0 {
		r, err := read(ctx, MemgraphQueryInput{Cypher: inserterAuditUnfedBeltsCypher,
			Params: map[string]any{"surface": in.Surface, "units": inserterAuditUnits(beltSources)}, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
		if err != nil {
			return nil, fmt.Errorf("unfed belts: %w", err)
		}
		truncated = truncated || r.Truncated
		for _, row := range r.Rows {
			if len(row) > 0 {
				unfed[int64(stretchInt(row[0]))] = true
			}
		}
	}

	machineChestItems := map[int][]string{}
	if len(filterSources) > 0 {
		var nodes []*MemgraphTraceNode
		for _, n := range filterSources {
			nodes = append(nodes, n)
		}
		machineChestItems, err = removalImpactItems(ctx, read, removalImpactProducesCypher, in.Surface, nodes)
		if err != nil {
			return nil, fmt.Errorf("source items: %w", err)
		}
	}

	beltItems := map[int64][]string{} // belt unit -> items of its stretch
	if len(filterBelts) > 0 {
		r, err := read(ctx, MemgraphQueryInput{Cypher: inserterAuditBeltStretchCypher,
			Params: map[string]any{"surface": in.Surface, "units": inserterAuditUnits(filterBelts)}, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
		if err != nil {
			return nil, fmt.Errorf("belt stretches: %w", err)
		}
		truncated = truncated || r.Truncated
		stretches, _, stretchTrunc, err := loadStretches(ctx, read, MemgraphBeltStretchesInput{Surface: in.Surface})
		if err != nil {
			return nil, err
		}
		truncated = truncated || stretchTrunc
		byID := map[string]*stretchRow{}
		for _, s := range stretches {
			byID[s.ID] = s
		}
		for _, row := range r.Rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[1].(string)
			s := byID[id]
			if s == nil {
				continue
			}
			items := make([]string, 0, len(s.tileItems))
			for name := range s.tileItems {
				items = append(items, name)
			}
			sort.Strings(items)
			beltItems[int64(stretchInt(row[0]))] = items
		}
	}

	var findings []*MemgraphAuditFinding
	add := func(a *inserterAuditArm, code string, sev MemgraphAuditSeverity, msg string) {
		related := []*MemgraphTraceNode{a.source}
		if a.dest.ID != a.source.ID {
			related = append(related, a.dest)
		}
		findings = append(findings, &MemgraphAuditFinding{Code: code, Severity: sev, Node: a.inserter, Message: msg, Related: related})
	}
	for _, a := range arms {
		self := a.source.ID == a.dest.ID
		if self {
			add(a, "SELF_LOOP", MemgraphAuditSeverityBlocker,
				fmt.Sprintf("picks from and drops into the same %s %s, so it moves items nowhere", a.source.Kind, traceLabel(a.source, false)))
		}
		if inserterAuditVoidChest(a) && len(machineChestItems[unitOf(a.source)]) == 0 {
			add(a, "VOID_PICKUP", MemgraphAuditSeverityConcern,
				fmt.Sprintf("picks from infinity chest %s, which has no filters and removes every item put in it, so it never holds anything to pick", traceLabel(a.source, false)))
		}
		if !self && a.source.Kind == "Belt" && a.dest.Kind == "Belt" {
			add(a, "BELT_TO_BELT", MemgraphAuditSeverityInfo,
				fmt.Sprintf("moves items from belt %s to belt %s, usually a workaround tap", traceLabel(a.source, false), traceLabel(a.dest, false)))
		}
		if a.source.Kind == "Belt" && unfed[int64(unitOf(a.source))] && inserterAuditBeltEmpty(a.sourceProps) {
			add(a, "DEAD_SOURCE", MemgraphAuditSeverityConcern,
				fmt.Sprintf("picks from belt %s, which no belt feeds and nothing drops onto, and whose lanes are empty in the snapshot", traceLabel(a.source, false)))
		}
		filters := filtered(a)
		if len(filters) == 0 {
			continue
		}
		var provided []string
		switch a.source.Kind {
		case "Machine", "Chest":
			provided = machineChestItems[unitOf(a.source)]
		case "Belt":
			provided = beltItems[int64(unitOf(a.source))]
		}
		blacklist := a.inserterProps["filterMode"] == "blacklist"
		provided = inserterAuditDedupe(provided)
		if inserterAuditMismatch(filters, provided, blacklist) {
			mode := "whitelist"
			effect := "none of them is something the source can provide"
			if blacklist {
				mode = "blacklist"
				effect = "every item the source can provide is listed"
			}
			add(a, "FILTER_MISMATCH", MemgraphAuditSeverityConcern,
				fmt.Sprintf("%s filters %s never pick anything: %s. %s %s can provide: %s", mode, inserterAuditList(filters), effect,
					a.source.Kind, traceLabel(a.source, false), inserterAuditList(provided)))
		}
	}
	return finishAudit(findings, in.Limit, truncated, res.Snapshot), nil
}

func inserterAuditDedupe(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, it := range items {
		if it != "" && !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	sort.Strings(out)
	return out
}
