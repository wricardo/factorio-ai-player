package main

import (
	"context"
	"strings"
	"testing"
)

func routeBetweenTestReader(t *testing.T, nodes map[int64]map[string]any, routeRows [][]any, truncated bool, calls *[]MemgraphQueryInput) memgraphReader {
	t.Helper()
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		*calls = append(*calls, in)
		if strings.HasPrefix(in.Cypher, "MATCH (r {unit: $unit, surface: $surface}) RETURN") {
			n, ok := nodes[in.Params.(map[string]any)["unit"].(int64)]
			if !ok {
				return &MemgraphQueryResult{}, nil
			}
			return &MemgraphQueryResult{Rows: [][]any{{n}}}, nil
		}
		return &MemgraphQueryResult{Rows: routeRows, Truncated: truncated}, nil
	}
}

func routeBetweenFixture() map[int64]map[string]any {
	return map[int64]map[string]any{
		346: testBoltNode("nauvis:346", "Machine", 346, map[string]any{"name": "assembling-machine-1"}),
		370: testBoltNode("nauvis:370", "Machine", 370, map[string]any{"name": "assembling-machine-1"}),
		383: testBoltNode("nauvis:383", "Inserter", 383, map[string]any{"name": "inserter"}),
		384: testBoltNode("nauvis:384", "Belt", 384, map[string]any{"name": "transport-belt"}),
		396: testBoltNode("nauvis:396", "Inserter", 396, map[string]any{"name": "inserter"}),
	}
}

func TestRouteBetweenValidation(t *testing.T) {
	ok := MemgraphRouteBetweenInput{From: "1", To: "2", Surface: "nauvis", MaxHops: 10, MaxRoutes: 10}
	cases := map[string]func(*MemgraphRouteBetweenInput){
		"bad from":    func(in *MemgraphRouteBetweenInput) { in.From = "abc" },
		"bad to":      func(in *MemgraphRouteBetweenInput) { in.To = "-3" },
		"same":        func(in *MemgraphRouteBetweenInput) { in.To = in.From },
		"surface":     func(in *MemgraphRouteBetweenInput) { in.Surface = "a b;" },
		"hops low":    func(in *MemgraphRouteBetweenInput) { in.MaxHops = 0 },
		"hops high":   func(in *MemgraphRouteBetweenInput) { in.MaxHops = 17 },
		"routes low":  func(in *MemgraphRouteBetweenInput) { in.MaxRoutes = 0 },
		"routes high": func(in *MemgraphRouteBetweenInput) { in.MaxRoutes = 101 },
	}
	for name, mut := range cases {
		in := ok
		mut(&in)
		var calls []MemgraphQueryInput
		if _, err := routeBetween(context.Background(), routeBetweenTestReader(t, nil, nil, false, &calls), in); err == nil {
			t.Errorf("%s: want error", name)
		}
		if len(calls) != 0 {
			t.Errorf("%s: validation must precede any read", name)
		}
	}
}

func TestRouteBetweenCypherShape(t *testing.T) {
	q := routeBetweenCypher(4)
	if got := strings.Count(q, "UNWIND (CASE WHEN size(ps)"); got != 4 {
		t.Fatalf("hop blocks = %d, want 4", got)
	}
	if !strings.Contains(q, "cur.id = $toId") || strings.Contains(q, "370") {
		t.Fatalf("target must be a parameter:\n%s", q)
	}
	if err := validateReadOnlyCypher(q); err != nil {
		t.Fatalf("guard rejected: %v", err)
	}
}

func TestRouteBetweenConnected(t *testing.T) {
	n := routeBetweenFixture()
	row := []any{int64(1), []any{n[346], n[383], n[384], n[396], n[370]}}
	var calls []MemgraphQueryInput
	out, err := routeBetween(context.Background(), routeBetweenTestReader(t, n, [][]any{row}, true, &calls),
		MemgraphRouteBetweenInput{From: "346", To: "370", Surface: "nauvis", MaxHops: 10, MaxRoutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Connected || !out.Truncated || len(out.Routes) != 1 {
		t.Fatalf("got %+v", out)
	}
	steps := out.Routes[0].Steps
	if steps[0] != "nauvis:346" || steps[len(steps)-1] != "nauvis:370" || len(out.Nodes) != 5 {
		t.Fatalf("steps %v nodes %d", steps, len(out.Nodes))
	}
	last := calls[len(calls)-1]
	lp, _ := last.Params.(map[string]any)
	if lp["toId"] != "nauvis:370" || lp["unit"] != int64(346) || last.Limit != 5 {
		t.Fatalf("params %+v limit %d", lp, last.Limit)
	}
	if !strings.Contains(out.Outline, "->") {
		t.Fatalf("outline %q", out.Outline)
	}
}

func TestRouteBetweenNotConnected(t *testing.T) {
	var calls []MemgraphQueryInput
	out, err := routeBetween(context.Background(), routeBetweenTestReader(t, routeBetweenFixture(), nil, false, &calls),
		MemgraphRouteBetweenInput{From: "370", To: "346", Surface: "nauvis", MaxHops: 10, MaxRoutes: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out.Connected || len(out.Routes) != 0 || len(out.Nodes) != 0 || out.Outline == "" {
		t.Fatalf("got %+v", out)
	}
}

func TestRouteBetweenRejectsRootKindAndMissing(t *testing.T) {
	var calls []MemgraphQueryInput
	r := routeBetweenTestReader(t, routeBetweenFixture(), nil, false, &calls)
	in := MemgraphRouteBetweenInput{From: "346", To: "383", Surface: "nauvis", MaxHops: 3, MaxRoutes: 1}
	if _, err := routeBetween(context.Background(), r, in); err == nil || !strings.Contains(err.Error(), "Inserter") {
		t.Fatalf("want inserter rejection, got %v", err)
	}
	in.From, in.To = "999", "346"
	if _, err := routeBetween(context.Background(), r, in); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("want missing node, got %v", err)
	}
}
