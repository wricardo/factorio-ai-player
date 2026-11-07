package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// fuelAuditFuels is the fixed fuel list. The export does not carry a burner's
// accepted fuel categories, so this is a heuristic, not the engine's answer.
var fuelAuditFuels = map[string]bool{
	"coal": true, "wood": true, "solid-fuel": true, "rocket-fuel": true, "nuclear-fuel": true,
}

// Burner-ness comes from data: the exporter writes fuelCount (always paired
// with an empty or listed fuel inventory) only for entities that expose a fuel
// inventory, so electric machines never carry it. fuelCount = 0 is an empty
// fuel slot at the snapshot tick, a sample and not proof of a missing feed.
const fuelAuditEmptyCypher = `MATCH (m:Entity)
WHERE m.surface = $surface AND m.fuelCount = 0
RETURN m ORDER BY m.unit`

// One row per feeding inserter and source item list: the empty burner id, the
// inserter, its pick-up source, a recipe product of the source (machines) and
// the belt stretch lane lists the source belt sits in.
const fuelAuditFeedersCypher = `MATCH (m:Entity)<-[:DROPS_TO]-(i:Inserter)
WHERE m.surface = $surface AND m.fuelCount = 0
OPTIONAL MATCH (i)-[:PICKS_FROM]->(s)
OPTIONAL MATCH (s)-[:PRODUCES]->(p:Item)
OPTIONAL MATCH (s)-[:IN_STRETCH]->(st:BeltStretch)
RETURN m.id, i, s, p.name, st.itemsLeft, st.itemsRight`

const fuelAuditHeuristicNote = "The empty fuel slot is a sample at the snapshot tick, and the fuel list (coal, wood, solid-fuel, rocket-fuel, nuclear-fuel) is a heuristic because the export does not carry accepted fuel categories."

type fuelAuditFeeder struct {
	inserter *MemgraphTraceNode
	source   *MemgraphTraceNode
	fuels    map[string]bool
}

// fuelAuditItemName extracts the item from "name:count" or "name@quality:count".
func fuelAuditItemName(entry string) string {
	name, _, _ := strings.Cut(entry, ":")
	name, _, _ = strings.Cut(name, "@")
	return name
}

func fuelAuditAddList(dst map[string]bool, v any) {
	var entries []string
	switch x := v.(type) {
	case []string:
		entries = x
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				entries = append(entries, s)
			}
		}
	}
	for _, e := range entries {
		if fuelAuditFuels[fuelAuditItemName(e)] {
			dst[fuelAuditItemName(e)] = true
		}
	}
}

// fuelAuditLanes reads "index|name|side|item:count+item:count" lane entries.
func fuelAuditLanes(dst map[string]bool, v any) {
	var lanes []string
	switch x := v.(type) {
	case []string:
		lanes = x
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				lanes = append(lanes, s)
			}
		}
	}
	for _, l := range lanes {
		parts := strings.SplitN(l, "|", 4)
		if len(parts) == 4 {
			fuelAuditAddList(dst, strings.Split(parts[3], "+"))
		}
	}
}

func fuelAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	params := map[string]any{"surface": in.Surface}
	empty, err := read(ctx, MemgraphQueryInput{Cypher: fuelAuditEmptyCypher, Params: params, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, fmt.Errorf("fuel audit: empty burners: %w", err)
	}
	feeders, err := read(ctx, MemgraphQueryInput{Cypher: fuelAuditFeedersCypher, Params: params, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
	if err != nil {
		return nil, fmt.Errorf("fuel audit: feeders: %w", err)
	}
	snap := empty.Snapshot
	if snap == nil {
		snap = feeders.Snapshot
	}
	return fuelAuditAssemble(empty.Rows, feeders.Rows, empty.Truncated || feeders.Truncated, in.Limit, snap)
}

func fuelAuditAssemble(emptyRows, feederRows [][]any, truncated bool, limit int, snap *MemgraphSnapshotInfo) (*MemgraphAudit, error) {
	byMachine := map[string]map[string]*fuelAuditFeeder{}
	for _, row := range feederRows {
		if len(row) < 6 {
			return nil, fmt.Errorf("fuel audit: feeder row has %d columns, want 6", len(row))
		}
		mid, _ := row[0].(string)
		ins, err := traceNodeFromAny(row[1])
		if err != nil {
			return nil, fmt.Errorf("fuel audit: feeding inserter: %w", err)
		}
		f := byMachine[mid][ins.ID]
		if f == nil {
			f = &fuelAuditFeeder{inserter: ins, fuels: map[string]bool{}}
			if byMachine[mid] == nil {
				byMachine[mid] = map[string]*fuelAuditFeeder{}
			}
			byMachine[mid][ins.ID] = f
		}
		if row[2] != nil {
			src, err := traceNodeFromAny(row[2])
			if err != nil {
				return nil, fmt.Errorf("fuel audit: feeder source: %w", err)
			}
			f.source = src
			props, _ := row[2].(map[string]any)["properties"].(map[string]any)
			for _, k := range []string{"items", "left", "right", "output"} {
				fuelAuditAddList(f.fuels, props[k])
			}
			fuelAuditLanes(f.fuels, props["lanes"])
		}
		if name, ok := row[3].(string); ok && fuelAuditFuels[name] {
			f.fuels[name] = true
		}
		fuelAuditAddList(f.fuels, row[4])
		fuelAuditAddList(f.fuels, row[5])
	}

	var findings []*MemgraphAuditFinding
	for _, row := range emptyRows {
		if len(row) < 1 {
			return nil, fmt.Errorf("fuel audit: empty burner row has no columns")
		}
		m, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, fmt.Errorf("fuel audit: burner: %w", err)
		}
		fs := byMachine[m.ID]
		if len(fs) == 0 {
			findings = append(findings, &MemgraphAuditFinding{
				Code: "NO_FUEL_ARM", Severity: MemgraphAuditSeverityBlocker, Node: m, Related: []*MemgraphTraceNode{},
				Message: fmt.Sprintf("%s has an empty fuel slot and no inserter drops into it. %s", traceLabel(m, false), fuelAuditHeuristicNote),
			})
			continue
		}
		ids := make([]string, 0, len(fs))
		for id := range fs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var related []*MemgraphTraceNode
		var seen = map[string]bool{}
		var fuels []string
		for _, id := range ids {
			f := fs[id]
			if len(f.fuels) == 0 {
				continue
			}
			related = append(related, f.inserter)
			if f.source != nil {
				related = append(related, f.source)
			}
			for name := range f.fuels {
				if !seen[name] {
					seen[name] = true
					fuels = append(fuels, name)
				}
			}
		}
		if len(related) == 0 {
			continue
		}
		sort.Strings(fuels)
		findings = append(findings, &MemgraphAuditFinding{
			Code: "FUEL_NOT_ARRIVING", Severity: MemgraphAuditSeverityConcern, Node: m, Related: related,
			Message: fmt.Sprintf("%s has an empty fuel slot although a feeding inserter picks from a source carrying %s. %s", traceLabel(m, false), strings.Join(fuels, ", "), fuelAuditHeuristicNote),
		})
	}
	return finishAudit(findings, limit, truncated, snap), nil
}
