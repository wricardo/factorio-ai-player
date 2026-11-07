package main

import (
	"context"
	"strings"
	"testing"
)

func areaTestNode(id, kind string, unit int, name, status string) map[string]any {
	props := map[string]any{"id": id, "unit": int64(unit), "name": name}
	if status != "" {
		props["status"] = status
	}
	return map[string]any{"id": id, "labels": []any{"Entity", kind}, "properties": props}
}

func TestValidateAreaGraphInput(t *testing.T) {
	ok := MemgraphAreaGraphInput{Surface: "nauvis", Area: &FactoryAreaInput{LeftTopX: -10, LeftTopY: -10, RightBottomX: 10, RightBottomY: 10}, MaxNodes: 10}
	if err := validateAreaGraphInput(ok); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*MemgraphAreaGraphInput){
		"area":     func(i *MemgraphAreaGraphInput) { i.Area = nil },
		"positive": func(i *MemgraphAreaGraphInput) { i.Area = &FactoryAreaInput{RightBottomX: -1, RightBottomY: 1} },
		"at most":  func(i *MemgraphAreaGraphInput) { i.Area = &FactoryAreaInput{RightBottomX: 2000, RightBottomY: 10} },
		"surface":  func(i *MemgraphAreaGraphInput) { i.Surface = "x'}) DELETE" },
		"maxNodes": func(i *MemgraphAreaGraphInput) { i.MaxNodes = 0 },
	} {
		bad := ok
		mut(&bad)
		if err := validateAreaGraphInput(bad); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

// TestAreaGraph pins components (largest first, undirected), the in/out
// boundary direction, and truncation when the box holds more than maxNodes.
func TestAreaGraph(t *testing.T) {
	nodes := [][]any{
		{areaTestNode("n:1", "Machine", 1, "stone-furnace", "item_ingredient_shortage")},
		{areaTestNode("n:2", "Inserter", 2, "inserter", "")},
		{areaTestNode("n:3", "Belt", 3, "transport-belt", "")},
		{areaTestNode("n:4", "Chest", 4, "wooden-chest", "")},
		{areaTestNode("n:5", "Machine", 5, "boiler", "working")},
	}
	read := func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		switch {
		case strings.Contains(in.Cypher, "RETURN a ORDER BY"):
			return &MemgraphQueryResult{Rows: nodes}, nil
		case strings.Contains(in.Cypher, "startNode"):
			return &MemgraphQueryResult{Rows: [][]any{
				{"n:3", false, "BELT_OUT", areaTestNode("o:9", "Belt", 9, "transport-belt", "")},
				{"n:3", true, "BELT_OUT", areaTestNode("o:10", "Belt", 10, "transport-belt", "")},
			}}, nil
		default:
			return &MemgraphQueryResult{Rows: [][]any{{"n:2", "DROPS_TO", "n:1"}, {"n:2", "PICKS_FROM", "n:3"}}}, nil
		}
	}
	in := MemgraphAreaGraphInput{Surface: "nauvis", Area: &FactoryAreaInput{RightBottomX: 5, RightBottomY: 5}, IncludeBoundary: true, MaxNodes: 10}
	got, err := areaGraph(context.Background(), read, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.NodeCount != 5 || got.Summary.EdgeCount != 2 || got.Summary.ComponentCount != 3 || got.Truncated {
		t.Fatalf("summary = %+v truncated=%v", got.Summary, got.Truncated)
	}
	if c := got.Components[0]; c.Size != 3 || len(c.NodeIds) != 3 || !strings.Contains(c.Description, "1 stone-furnace") {
		t.Fatalf("largest component = %+v", c)
	}
	if got.Components[1].Size != 1 || got.Components[2].Size != 1 {
		t.Fatalf("singletons = %+v %+v", got.Components[1], got.Components[2])
	}
	if len(got.Boundary) != 2 || got.Boundary[0].Direction != MemgraphBoundaryDirectionIn || got.Boundary[1].Direction != MemgraphBoundaryDirectionOut {
		t.Fatalf("boundary = %+v", got.Boundary)
	}
	if len(got.Summary.Attention) != 1 || got.Summary.Attention[0].Node.ID != "n:1" {
		t.Fatalf("attention = %+v", got.Summary.Attention)
	}
	if !strings.HasSuffix(got.Outline, "boundary: 1 edges in, 1 out") {
		t.Fatalf("outline = %q", got.Outline)
	}

	in.MaxNodes = 3
	cut, err := areaGraph(context.Background(), read, in)
	if err != nil || !cut.Truncated || len(cut.Nodes) != 3 {
		t.Fatalf("truncation: err=%v got=%+v", err, cut)
	}
	empty := func(context.Context, MemgraphQueryInput) (*MemgraphQueryResult, error) {
		return &MemgraphQueryResult{}, nil
	}
	none, err := areaGraph(context.Background(), empty, in)
	if err != nil || none.Outline != "no entities in the area" || none.Summary.ComponentCount != 0 {
		t.Fatalf("empty area: err=%v got=%+v", err, none)
	}
}
