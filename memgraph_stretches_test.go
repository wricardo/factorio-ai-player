package main

import (
	"context"
	"strings"
	"testing"
)

func stretchTestNode(id string, head int, belts int, end string, left, right []any, lc, rc int) map[string]any {
	p := map[string]any{"id": id, "headUnit": int64(head), "belts": int64(belts), "endKind": end, "leftCount": int64(lc), "rightCount": int64(rc), "headX": 0.5, "headY": 0.5}
	if left != nil {
		p["itemsLeft"] = left
	}
	if right != nil {
		p["itemsRight"] = right
	}
	return map[string]any{"id": id, "labels": []any{"BeltStretch"}, "properties": p}
}

func stretchTestReader(tiles [][]any) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		switch {
		case strings.Contains(in.Cypher, "RETURN s ORDER BY"):
			return &MemgraphQueryResult{Rows: [][]any{
				{stretchTestNode("n:stretch:1", 1, 3, "deadend", []any{"iron-plate:30"}, nil, 30, 0)},
				{stretchTestNode("n:stretch:2", 2, 2, "sideload", []any{"coal:5", "iron-ore:4"}, []any{"coal:9"}, 9, 9)},
				{stretchTestNode("n:stretch:3", 3, 1, "deadend", nil, nil, 0, 0)},
				{stretchTestNode("n:stretch:4", 4, 2, "deadend", []any{"iron-plate:2"}, nil, 2, 0)},
			}}, nil
		case strings.Contains(in.Cypher, "FLOWS_INTO"):
			return &MemgraphQueryResult{Rows: [][]any{{"n:stretch:2"}}}, nil
		case strings.Contains(in.Cypher, "UNION"):
			// stretch 1 is picked by inserter 7 both ways: counts once
			return &MemgraphQueryResult{Rows: [][]any{{"n:stretch:1", int64(7)}, {"n:stretch:1", int64(7)}}}, nil
		default:
			return &MemgraphQueryResult{Rows: tiles}, nil
		}
	}
}

// TestBeltStretches pins the finding rules: mixed lane means 2+ items on one
// lane; NO_CONSUMER needs items, no downstream stretch and no picker, so a
// picked dead end and a stretch that flows into another are not findings.
func TestBeltStretches(t *testing.T) {
	in := MemgraphBeltStretchesInput{Surface: "nauvis", Limit: 10}
	got, err := beltStretches(context.Background(), stretchTestReader(nil), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.StretchCount != 4 || got.Summary.CarryingCount != 3 || got.Summary.MixedCount != 1 || got.Summary.NoConsumerCount != 1 {
		t.Fatalf("summary = %+v", got.Summary)
	}
	reasons := map[string]MemgraphStretchReason{}
	for _, f := range got.Attention {
		reasons[f.StretchID] = f.Reason
	}
	if reasons["n:stretch:2"] != MemgraphStretchReasonMixedLane || reasons["n:stretch:4"] != MemgraphStretchReasonNoConsumer || len(reasons) != 2 {
		t.Fatalf("attention = %v (stretch 1 is picked, stretch 2 flows on)", reasons)
	}
	if got.Stretches[0].Pickers != 1 {
		t.Fatalf("inserter seen twice must count once, got %d", got.Stretches[0].Pickers)
	}
	if got.Summary.Items[0].Name != "iron-plate" || got.Summary.Items[0].Count != 32 {
		t.Fatalf("items = %+v", got.Summary.Items)
	}

	for name, f := range map[string]func(*MemgraphBeltStretchesInput){
		"item":      func(i *MemgraphBeltStretchesInput) { s := "coal"; i.Item = &s },
		"mixedOnly": func(i *MemgraphBeltStretchesInput) { i.MixedOnly = true },
		"endKind":   func(i *MemgraphBeltStretchesInput) { e := MemgraphStretchEndSideload; i.EndKind = &e },
	} {
		c := in
		f(&c)
		r, err := beltStretches(context.Background(), stretchTestReader(nil), c)
		if err != nil || r.Summary.StretchCount != 1 || r.Stretches[0].ID != "n:stretch:2" {
			t.Fatalf("%s filter: err=%v got=%+v", name, err, r)
		}
	}
	cut := in
	cut.Limit = 2
	r, _ := beltStretches(context.Background(), stretchTestReader(nil), cut)
	if !r.Truncated || len(r.Stretches) != 2 || r.Summary.StretchCount != 4 {
		t.Fatalf("limit must cut rows but not the summary: %+v", r)
	}
	for name, f := range map[string]func(*MemgraphBeltStretchesInput){
		"near":    func(i *MemgraphBeltStretchesInput) { x := 1.0; i.NearX = &x },
		"limit":   func(i *MemgraphBeltStretchesInput) { i.Limit = 0 },
		"item":    func(i *MemgraphBeltStretchesInput) { s := "Bad Item"; i.Item = &s },
		"surface": func(i *MemgraphBeltStretchesInput) { i.Surface = "x'}" },
	} {
		c := in
		f(&c)
		if _, err := beltStretches(context.Background(), stretchTestReader(nil), c); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}

func TestBeltStretchMap(t *testing.T) {
	tiles := [][]any{
		{"n:stretch:1", 0.5, 0.5}, {"n:stretch:1", 1.5, 0.5}, {"n:stretch:1", 2.5, 0.5},
		{"n:stretch:2", 0.5, 2.5}, {"n:stretch:2", 1.5, 2.5},
		{"n:stretch:3", 5.5, 2.5},
		{"n:stretch:1", 40.5, 0.5}, // outside the area: must not be drawn
	}
	in := MemgraphBeltStretchMapInput{Mode: MemgraphStretchMapModeItem, Area: &FactoryAreaInput{RightBottomX: 8, RightBottomY: 3}}
	m, err := beltStretchMap(context.Background(), stretchTestReader(tiles), in)
	if err != nil {
		t.Fatal(err)
	}
	// stretch 1 carries iron-plate (a), 2 is mixed (#), 3 is empty (.)
	want := []string{"aaa", "", "##   ."}
	if strings.Join(m.Rows, "|") != strings.Join(want, "|") || m.Columns != 8 || m.OriginX != 0 || m.OriginY != 0 {
		t.Fatalf("rows = %q cols=%d origin=(%v,%v)", m.Rows, m.Columns, m.OriginX, m.OriginY)
	}
	chars := map[string]string{}
	for _, l := range m.Legend {
		chars[l.Char] = l.Label
	}
	if chars["a"] != "iron-plate" || chars["#"] != "mixed lane" || chars["."] != "empty stretch" {
		t.Fatalf("legend = %+v", m.Legend)
	}
	in.Mode = MemgraphStretchMapModeStretch
	s, _ := beltStretchMap(context.Background(), stretchTestReader(tiles), in)
	if s.Rows[0] != "AAA" || s.Rows[2] != "BB   C" {
		t.Fatalf("stretch mode rows = %q", s.Rows)
	}
}

// TestBeltStretchMapArea pins the required, bounded area and that the grid
// spans exactly the box (non-integer corners, margins kept).
func TestBeltStretchMapArea(t *testing.T) {
	tiles := [][]any{{"n:stretch:1", 0.5, 0.5}, {"n:stretch:1", 1.5, 0.5}, {"n:stretch:2", 4.5, 0.5}}
	in := MemgraphBeltStretchMapInput{Mode: MemgraphStretchMapModeItem, Area: &FactoryAreaInput{LeftTopX: -1, LeftTopY: 0, RightBottomX: 3, RightBottomY: 2}}
	m, err := beltStretchMap(context.Background(), stretchTestReader(tiles), in)
	if err != nil {
		t.Fatal(err)
	}
	if m.OriginX != -1 || m.OriginY != 0 || m.Columns != 4 || len(m.Rows) != 2 || m.Rows[0] != " aa" {
		t.Fatalf("origin=(%v,%v) cols=%d rows=%q", m.OriginX, m.OriginY, m.Columns, m.Rows)
	}
	for name, area := range map[string]*FactoryAreaInput{
		"area is required": nil,
		"positive size":    {RightBottomX: -1, RightBottomY: 1},
		"per side":         {RightBottomX: 2000, RightBottomY: 10},
		"in total":         {RightBottomX: 1000, RightBottomY: 300},
	} {
		bad := in
		bad.Area = area
		if _, err := beltStretchMap(context.Background(), stretchTestReader(tiles), bad); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
}
