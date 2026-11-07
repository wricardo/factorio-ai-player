package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	stretchesMaxLimit  = 1000
	stretchesMaxRows   = 10000
	stretchesDefaultTO = supplyChainTimeoutMs
)

var stretchItemPattern = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)

const stretchesCypher = `MATCH (s:BeltStretch) WHERE s.surface = $surface RETURN s ORDER BY s.headUnit`

// stretchFlowsCypher lists stretches that flow into another stretch (the loading
// side of a splitter or side-load). The edge points from the loading run.
const stretchFlowsCypher = `MATCH (s:BeltStretch)-[:FLOWS_INTO]->(:BeltStretch) WHERE s.surface = $surface RETURN DISTINCT s.id`

// stretchPickersCypher returns (stretch id, inserter unit) pairs for both ways
// the graph links an inserter to a stretch; pairs are de-duplicated in Go.
const stretchPickersCypher = `MATCH (i:Inserter)-[:TAKES_FROM_STRETCH]->(s:BeltStretch) WHERE s.surface = $surface RETURN s.id, i.unit
UNION
MATCH (i:Inserter)-[:PICKS_FROM]->(:Belt)-[:IN_STRETCH]->(s:BeltStretch) WHERE s.surface = $surface RETURN s.id, i.unit`

func validateStretchesInput(in MemgraphBeltStretchesInput) error {
	if !memgraphSurfacePattern.MatchString(in.Surface) {
		return fmt.Errorf("surface %q must be 1-64 letters, digits, '.', '_' or '-'", in.Surface)
	}
	if in.Item != nil && !stretchItemPattern.MatchString(*in.Item) {
		return fmt.Errorf("item %q must match %s", *in.Item, stretchItemPattern)
	}
	if (in.NearX == nil) != (in.NearY == nil) {
		return fmt.Errorf("nearX and nearY must be given together")
	}
	for _, p := range []*float64{in.NearX, in.NearY} {
		if p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0)) {
			return fmt.Errorf("near must be finite")
		}
	}
	if in.Limit < 1 || in.Limit > stretchesMaxLimit {
		return fmt.Errorf("limit must be between 1 and %d", stretchesMaxLimit)
	}
	if e := in.EndKind; e != nil && *e != MemgraphStretchEndSplitter && *e != MemgraphStretchEndSideload && *e != MemgraphStretchEndDeadend {
		return fmt.Errorf("endKind %q is not SPLITTER, SIDELOAD or DEADEND", *e)
	}
	return nil
}

// stretchRow is a decoded :BeltStretch plus the edge facts a finding needs.
type stretchRow struct {
	MemgraphStretch
	tileItems map[string]int
}

func stretchProps(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a stretch node, got %T", v)
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("stretch node has no properties")
	}
	return props, nil
}

func stretchInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// parseLane turns ["iron-plate:36", ...] into counts, most common first.
func parseLane(v any) []*MemgraphCount {
	list, _ := v.([]any)
	out := make([]*MemgraphCount, 0, len(list))
	for _, e := range list {
		s, _ := e.(string)
		i := strings.LastIndex(s, ":")
		if i <= 0 {
			continue
		}
		n, err := strconv.Atoi(s[i+1:])
		if err != nil {
			continue
		}
		out = append(out, &MemgraphCount{Name: s[:i], Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func decodeStretch(v any) (*stretchRow, error) {
	p, err := stretchProps(v)
	if err != nil {
		return nil, err
	}
	id, _ := p["id"].(string)
	if id == "" {
		return nil, fmt.Errorf("stretch without id")
	}
	s := &stretchRow{tileItems: map[string]int{}}
	s.ID = id
	s.HeadUnit = strconv.Itoa(stretchInt(p["headUnit"]))
	if t, ok := p["tailUnit"]; ok && t != nil {
		tu := strconv.Itoa(stretchInt(t))
		s.TailUnit = &tu
	}
	s.X, s.Y = optionalFloat(p["headX"]), optionalFloat(p["headY"])
	s.Tiles = stretchInt(p["belts"])
	switch k, _ := p["endKind"].(string); k {
	case "splitter":
		s.EndKind = MemgraphStretchEndSplitter
	case "sideload":
		s.EndKind = MemgraphStretchEndSideload
	default:
		s.EndKind = MemgraphStretchEndDeadend
	}
	s.Left, s.Right = parseLane(p["itemsLeft"]), parseLane(p["itemsRight"])
	s.LeftCount, s.RightCount = stretchInt(p["leftCount"]), stretchInt(p["rightCount"])
	s.Mixed = len(s.Left) > 1 || len(s.Right) > 1
	for _, lane := range [][]*MemgraphCount{s.Left, s.Right} {
		for _, c := range lane {
			s.tileItems[c.Name] += c.Count
		}
	}
	return s, nil
}

func (s *stretchRow) carries() bool { return len(s.tileItems) > 0 }

func (s *stretchRow) noConsumer() bool {
	return s.carries() && !s.FlowsIntoStretch && s.Pickers == 0
}

func stretchMatches(s *stretchRow, in MemgraphBeltStretchesInput) bool {
	if in.Item != nil {
		if _, ok := s.tileItems[*in.Item]; !ok {
			return false
		}
	}
	if in.MixedOnly && !s.Mixed {
		return false
	}
	return in.EndKind == nil || s.EndKind == *in.EndKind
}

// loadStretches reads, decodes and filters the stretches of a surface, with
// downstream-flow and picker facts attached. Order is head unit.
func loadStretches(ctx context.Context, read memgraphReader, in MemgraphBeltStretchesInput) ([]*stretchRow, *MemgraphSnapshotInfo, bool, error) {
	params := map[string]any{"surface": in.Surface}
	res, err := read(ctx, MemgraphQueryInput{Cypher: stretchesCypher, Params: params, Limit: stretchesMaxRows, TimeoutMs: stretchesDefaultTO})
	if err != nil {
		return nil, nil, false, fmt.Errorf("belt stretches: %w", err)
	}
	truncated := res.Truncated
	all := make([]*stretchRow, 0, len(res.Rows))
	byID := map[string]*stretchRow{}
	for _, row := range res.Rows {
		if len(row) < 1 {
			return nil, nil, false, fmt.Errorf("stretch row is empty")
		}
		s, err := decodeStretch(row[0])
		if err != nil {
			return nil, nil, false, err
		}
		all = append(all, s)
		byID[s.ID] = s
	}
	if len(all) > 0 {
		fres, err := read(ctx, MemgraphQueryInput{Cypher: stretchFlowsCypher, Params: params, Limit: stretchesMaxRows, TimeoutMs: stretchesDefaultTO})
		if err != nil {
			return nil, nil, false, fmt.Errorf("stretch flows: %w", err)
		}
		for _, row := range fres.Rows {
			if id, _ := row[0].(string); byID[id] != nil {
				byID[id].FlowsIntoStretch = true
			}
		}
		pres, err := read(ctx, MemgraphQueryInput{Cypher: stretchPickersCypher, Params: params, Limit: stretchesMaxRows, TimeoutMs: stretchesDefaultTO})
		if err != nil {
			return nil, nil, false, fmt.Errorf("stretch pickers: %w", err)
		}
		seen := map[string]bool{}
		for _, row := range pres.Rows {
			if len(row) < 2 {
				continue
			}
			id, _ := row[0].(string)
			key := id + "|" + fmt.Sprint(row[1])
			if byID[id] != nil && !seen[key] {
				seen[key] = true
				byID[id].Pickers++
			}
		}
		if fres.Truncated || pres.Truncated {
			truncated = true
		}
	}
	out := make([]*stretchRow, 0, len(all))
	for _, s := range all {
		if stretchMatches(s, in) {
			out = append(out, s)
		}
	}
	return out, res.Snapshot, truncated, nil
}

func beltStretches(ctx context.Context, read memgraphReader, in MemgraphBeltStretchesInput) (*MemgraphBeltStretches, error) {
	if err := validateStretchesInput(in); err != nil {
		return nil, err
	}
	rows, snap, truncated, err := loadStretches(ctx, read, in)
	if err != nil {
		return nil, err
	}
	sum := &MemgraphStretchSummary{StretchCount: len(rows)}
	items := map[string]int{}
	out := &MemgraphBeltStretches{Stretches: []*MemgraphStretch{}, Attention: []*MemgraphStretchFinding{}, Truncated: truncated, Snapshot: snap}
	for _, s := range rows {
		sum.TileCount += s.Tiles
		if s.carries() {
			sum.CarryingCount++
		}
		for name, n := range s.tileItems {
			items[name] += n
		}
		if s.Mixed {
			sum.MixedCount++
			out.Attention = append(out.Attention, &MemgraphStretchFinding{StretchID: s.ID, Reason: MemgraphStretchReasonMixedLane, Detail: stretchLaneText(s)})
		}
		if s.noConsumer() {
			sum.NoConsumerCount++
			out.Attention = append(out.Attention, &MemgraphStretchFinding{StretchID: s.ID, Reason: MemgraphStretchReasonNoConsumer,
				Detail: fmt.Sprintf("%d items, no inserter picks from it and it flows into no other stretch", s.LeftCount+s.RightCount)})
		}
		if in.NearX != nil && s.X != nil && s.Y != nil {
			d := math.Hypot(*s.X-*in.NearX, *s.Y-*in.NearY)
			s.Distance = &d
		}
	}
	sum.Items = stretchItemCounts(items)
	out.Summary = sum
	if in.NearX != nil {
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i].Distance, rows[j].Distance
			if (a == nil) != (b == nil) {
				return b == nil
			}
			return a != nil && *a < *b
		})
	}
	if len(rows) > in.Limit {
		rows, out.Truncated = rows[:in.Limit], true
	}
	lines := make([]string, len(rows))
	for i, s := range rows {
		st := s.MemgraphStretch
		out.Stretches = append(out.Stretches, &st)
		lines[i] = stretchLine(s)
	}
	out.Outline = strings.Join(lines, "\n")
	if out.Outline == "" {
		out.Outline = "no belt stretches matched"
	}
	return out, nil
}

func stretchItemCounts(m map[string]int) []*MemgraphCount {
	out := make([]*MemgraphCount, 0, len(m))
	for k, v := range m {
		out = append(out, &MemgraphCount{Name: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func stretchLaneText(s *stretchRow) string {
	lane := func(l []*MemgraphCount) string {
		if len(l) == 0 {
			return "empty"
		}
		parts := make([]string, len(l))
		for i, c := range l {
			parts[i] = fmt.Sprintf("%s %d", c.Name, c.Count)
		}
		return strings.Join(parts, ", ")
	}
	return "L: " + lane(s.Left) + " | R: " + lane(s.Right)
}

func stretchLine(s *stretchRow) string {
	end := strings.ToLower(string(s.EndKind))
	line := fmt.Sprintf("%s [%d belts -> %s] %s", s.HeadUnit, s.Tiles, end, stretchLaneText(s))
	if s.Mixed {
		line += " MIXED"
	}
	if s.noConsumer() {
		line += " NO CONSUMER"
	}
	return line
}
