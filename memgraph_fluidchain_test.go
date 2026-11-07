package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

type fcNode struct {
	unit   int64
	name   string
	labels []string
	// fluids per recipe relation: "PRODUCES"/"CONSUMES" -> fluid items.
	produces, consumes bool
}

// fcGraph is a fake memgraph holding FluidEntity/Machine nodes and FLUID_LINK
// edges. It answers the four statements fluidChain issues, with a real BFS and
// the reader's row-cap truncation.
type fcGraph struct {
	nodes map[int64]fcNode
	edges [][2]int64
	reads map[string]int
}

func fcID(u int64) string { return "nauvis:" + strconv.FormatInt(u, 10) }

func (g *fcGraph) bolt(u int64) map[string]any {
	n := g.nodes[u]
	labels := []any{"Entity"}
	for _, l := range n.labels {
		labels = append(labels, l)
	}
	return map[string]any{"id": "e" + fcID(u), "labels": labels, "properties": map[string]any{"id": fcID(u), "unit": u, "name": n.name}}
}

func (g *fcGraph) adj(u int64) []int64 {
	var out []int64
	for _, e := range g.edges {
		if e[0] == u {
			out = append(out, e[1])
		} else if e[1] == u {
			out = append(out, e[0])
		}
	}
	return out
}

func (g *fcGraph) reader(t *testing.T) memgraphReader {
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		p, _ := in.Params.(map[string]any)
		switch in.Cypher {
		case fluidChainRootCypher:
			g.reads["root"]++
			u := p["unit"].(int64)
			if _, ok := g.nodes[u]; !ok {
				return &MemgraphQueryResult{}, nil
			}
			return &MemgraphQueryResult{Rows: [][]any{{g.bolt(u), int64(len(g.adj(u)))}}}, nil
		case fluidChainWalkCypher:
			g.reads["walk"]++
			root := p["unit"].(int64)
			dist := map[int64]int{root: 0}
			queue := []int64{root}
			var order []int64
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				// neighbours visited in unit order for a stable fixture
				nb := g.adj(cur)
				for i := range nb {
					for j := i + 1; j < len(nb); j++ {
						if nb[j] < nb[i] {
							nb[i], nb[j] = nb[j], nb[i]
						}
					}
				}
				for _, x := range nb {
					if _, ok := dist[x]; !ok {
						dist[x] = dist[cur] + 1
						queue = append(queue, x)
						order = append(order, x)
					}
				}
			}
			res := &MemgraphQueryResult{Rows: [][]any{}}
			for _, u := range order {
				if len(res.Rows) == in.Limit {
					res.Truncated = true
					break
				}
				res.Rows = append(res.Rows, []any{g.bolt(u), int64(dist[u])})
			}
			return res, nil
		case fluidChainLinksCypher:
			g.reads["links"]++
			res := &MemgraphQueryResult{Rows: [][]any{}}
			for _, id := range p["ids"].([]any) {
				u, _ := strconv.ParseInt(strings.TrimPrefix(id.(string), "nauvis:"), 10, 64)
				for _, x := range g.adj(u) {
					res.Rows = append(res.Rows, []any{fcID(u), fcID(x)})
				}
			}
			return res, nil
		case fluidChainFluidsCypher:
			g.reads["fluids"]++
			res := &MemgraphQueryResult{Rows: [][]any{}}
			for _, id := range p["ids"].([]any) {
				u, _ := strconv.ParseInt(strings.TrimPrefix(id.(string), "nauvis:"), 10, 64)
				n := g.nodes[u]
				if n.produces {
					res.Rows = append(res.Rows, []any{id, "PRODUCES", "petroleum-gas"})
				}
				if n.consumes {
					res.Rows = append(res.Rows, []any{id, "CONSUMES", "crude-oil"})
				}
			}
			return res, nil
		}
		t.Fatalf("unexpected statement: %s", in.Cypher)
		return nil, nil
	}
}

func fcPipe(u int64) fcNode { return fcNode{unit: u, name: "pipe", labels: []string{"FluidEntity"}} }

func fcFixture(nodes []fcNode, edges ...[2]int64) *fcGraph {
	g := &fcGraph{nodes: map[int64]fcNode{}, edges: edges, reads: map[string]int{}}
	for _, n := range nodes {
		g.nodes[n.unit] = n
	}
	return g
}

func fcRun(t *testing.T, g *fcGraph, unit string, max int) (*MemgraphFluidChain, error) {
	t.Helper()
	return fluidChain(context.Background(), g.reader(t), MemgraphFluidChainInput{UnitNumber: unit, Surface: "nauvis", MaxNodes: max})
}

func fcIDs(ns []*MemgraphTraceNode) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.ID)
	}
	return out
}

func fcCodes(c *MemgraphFluidChain) map[string][]string {
	out := map[string][]string{}
	for _, f := range c.Findings {
		out[f.Code] = append(out[f.Code], f.Node.ID)
	}
	return out
}

func TestFluidChainBFSOrderAndLinkDedupe(t *testing.T) {
	// 1 - 2 - 3 - 4 ; 2 - 5 ; duplicate edge 2-3 (two ports) and a cycle 3-5.
	g := fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3), fcPipe(4), fcPipe(5)},
		[2]int64{1, 2}, [2]int64{2, 3}, [2]int64{3, 2}, [2]int64{3, 4}, [2]int64{2, 5}, [2]int64{3, 5})
	c, err := fcRun(t, g, "1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fcIDs(c.Nodes), ","); got != "nauvis:1,nauvis:2,nauvis:3,nauvis:5,nauvis:4" {
		t.Fatalf("nodes = %s", got)
	}
	if c.Root.ID != "nauvis:1" || c.Truncated {
		t.Fatalf("root %s truncated %v", c.Root.ID, c.Truncated)
	}
	var links []string
	for _, l := range c.Links {
		links = append(links, l.From+">"+l.To)
	}
	want := "nauvis:1>nauvis:2,nauvis:2>nauvis:3,nauvis:2>nauvis:5,nauvis:3>nauvis:5,nauvis:3>nauvis:4"
	if got := strings.Join(links, ","); got != want {
		t.Fatalf("links = %s, want %s", got, want)
	}
}

func TestFluidChainMaxNodesTruncation(t *testing.T) {
	g := fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3), fcPipe(4)}, [2]int64{1, 2}, [2]int64{2, 3}, [2]int64{3, 4})
	c, err := fcRun(t, g, "1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Truncated || strings.Join(fcIDs(c.Nodes), ",") != "nauvis:1,nauvis:2,nauvis:3" {
		t.Fatalf("truncated %v nodes %v", c.Truncated, fcIDs(c.Nodes))
	}
	for _, l := range c.Links {
		if l.To == "nauvis:4" || l.From == "nauvis:4" {
			t.Fatalf("link to a cut node: %+v", l)
		}
	}
	// Exactly maxNodes entities is not a cut.
	c, err = fcRun(t, g, "1", 4)
	if err != nil || c.Truncated || len(c.Nodes) != 4 {
		t.Fatalf("full fit: err %v truncated %v nodes %d", err, c.Truncated, len(c.Nodes))
	}
	// maxNodes 1 keeps only the root and says it cut something.
	c, err = fcRun(t, g, "1", 1)
	if err != nil || !c.Truncated || len(c.Nodes) != 1 {
		t.Fatalf("max 1: err %v truncated %v nodes %d", err, c.Truncated, len(c.Nodes))
	}
}

func TestFluidChainDeadEndPipe(t *testing.T) {
	// 1 - 2 - 3 - 4: pipes 1 and 4 have one link, 2 and 3 have two.
	g := fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3), fcPipe(4)}, [2]int64{1, 2}, [2]int64{2, 3}, [2]int64{3, 4})
	c, err := fcRun(t, g, "2", 50)
	if err != nil {
		t.Fatal(err)
	}
	codes := fcCodes(c)
	if got := strings.Join(codes["DEAD_END_PIPE"], ","); got != "nauvis:1,nauvis:4" {
		t.Fatalf("dead ends = %s", got)
	}
	for _, f := range c.Findings {
		if f.Code == "DEAD_END_PIPE" && f.Severity != MemgraphAuditSeverityInfo {
			t.Fatalf("severity %s", f.Severity)
		}
	}
	// A ring has no dead end; a truncated walk counts links past the cut.
	ring := fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3)}, [2]int64{1, 2}, [2]int64{2, 3}, [2]int64{3, 1})
	c, _ = fcRun(t, ring, "1", 50)
	if len(fcCodes(c)["DEAD_END_PIPE"]) != 0 {
		t.Fatalf("ring reported a dead end: %v", fcCodes(c))
	}
	c, _ = fcRun(t, g, "1", 2) // nodes 1,2; pipe 2 still has a link to 3
	if got := fcCodes(c)["DEAD_END_PIPE"]; len(got) != 1 || got[0] != "nauvis:1" {
		t.Fatalf("cut walk dead ends = %v", got)
	}
	// Only pipes: a tank with one link is not a dead-end pipe.
	tank := fcFixture([]fcNode{fcPipe(1), {unit: 2, name: "storage-tank", labels: []string{"FluidEntity"}}}, [2]int64{1, 2})
	c, _ = fcRun(t, tank, "1", 50)
	if got := fcCodes(c)["DEAD_END_PIPE"]; len(got) != 1 || got[0] != "nauvis:1" {
		t.Fatalf("tank case = %v", got)
	}
}

func TestFluidChainIsolatedRoot(t *testing.T) {
	g := fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3)}, [2]int64{2, 3})
	c, err := fcRun(t, g, "1", 50)
	if err != nil {
		t.Fatal(err)
	}
	f := c.Findings
	if len(f) != 1 || f[0].Code != "ISOLATED_FLUID_ENTITY" || f[0].Severity != MemgraphAuditSeverityConcern || f[0].Node.ID != "nauvis:1" {
		t.Fatalf("findings = %+v", f)
	}
	if len(c.Nodes) != 1 || c.Truncated || len(c.Links) != 0 || g.reads["walk"] != 0 {
		t.Fatalf("nodes %d truncated %v links %d walks %d", len(c.Nodes), c.Truncated, len(c.Links), g.reads["walk"])
	}
	c, _ = fcRun(t, g, "2", 50)
	if _, ok := fcCodes(c)["ISOLATED_FLUID_ENTITY"]; ok {
		t.Fatal("linked root flagged isolated")
	}
}

func TestFluidChainNoSource(t *testing.T) {
	boiler := fcNode{unit: 3, name: "boiler", labels: []string{"FluidEntity", "PowerSource"}}
	pump := fcNode{unit: 4, name: "offshore-pump", labels: []string{"FluidEntity"}}
	plant := fcNode{unit: 5, name: "chemical-plant", labels: []string{"Machine"}, consumes: true}
	refinery := fcNode{unit: 6, name: "oil-refinery", labels: []string{"Machine"}, produces: true}

	// Sink and no source: BLOCKER on the root, sinks listed.
	g := fcFixture([]fcNode{fcPipe(1), fcPipe(2), boiler}, [2]int64{1, 2}, [2]int64{2, 3})
	c, err := fcRun(t, g, "1", 50)
	if err != nil {
		t.Fatal(err)
	}
	f := fcCodes(c)["NO_SOURCE"]
	if len(f) != 1 || f[0] != "nauvis:1" || len(c.Sinks) != 1 || c.Sinks[0].ID != "nauvis:3" || len(c.Sources) != 0 {
		t.Fatalf("no-source: %v sinks %v sources %v", f, fcIDs(c.Sinks), fcIDs(c.Sources))
	}
	if c.Findings[0].Code != "NO_SOURCE" || c.Findings[0].Severity != MemgraphAuditSeverityBlocker || len(c.Findings[0].Related) != 1 {
		t.Fatalf("worst finding first: %+v", c.Findings[0])
	}

	// An offshore pump is a source.
	g = fcFixture([]fcNode{fcPipe(1), fcPipe(2), boiler, pump}, [2]int64{1, 2}, [2]int64{2, 3}, [2]int64{1, 4})
	c, _ = fcRun(t, g, "1", 50)
	if _, ok := fcCodes(c)["NO_SOURCE"]; ok || len(c.Sources) != 1 || c.Sources[0].ID != "nauvis:4" {
		t.Fatalf("pump case: %v sources %v", fcCodes(c), fcIDs(c.Sources))
	}

	// A machine whose recipe produces a fluid is a source; one that consumes is a sink.
	g = fcFixture([]fcNode{fcPipe(1), plant, refinery}, [2]int64{1, 5}, [2]int64{1, 6})
	c, _ = fcRun(t, g, "1", 50)
	if _, ok := fcCodes(c)["NO_SOURCE"]; ok || len(c.Sinks) != 1 || c.Sinks[0].ID != "nauvis:5" || len(c.Sources) != 1 || c.Sources[0].ID != "nauvis:6" {
		t.Fatalf("machines: %v sinks %v sources %v", fcCodes(c), fcIDs(c.Sinks), fcIDs(c.Sources))
	}

	// Consumer only: blocker. No consumers, no sources: nothing.
	g = fcFixture([]fcNode{fcPipe(1), plant}, [2]int64{1, 5})
	c, _ = fcRun(t, g, "1", 50)
	if len(fcCodes(c)["NO_SOURCE"]) != 1 {
		t.Fatalf("consumer only: %v", fcCodes(c))
	}
	g = fcFixture([]fcNode{fcPipe(1), fcPipe(2), fcPipe(3)}, [2]int64{1, 2}, [2]int64{2, 3})
	c, _ = fcRun(t, g, "1", 50)
	if _, ok := fcCodes(c)["NO_SOURCE"]; ok {
		t.Fatal("pipes alone flagged NO_SOURCE")
	}

	// A cut walk may have missed the source: no NO_SOURCE claim.
	g = fcFixture([]fcNode{fcPipe(1), boiler, pump}, [2]int64{1, 3}, [2]int64{1, 4})
	c, _ = fcRun(t, g, "1", 2) // keeps root + boiler (unit order), cuts the pump
	if !c.Truncated {
		t.Fatal("expected a cut")
	}
	if _, ok := fcCodes(c)["NO_SOURCE"]; ok {
		t.Fatal("NO_SOURCE reported on a truncated walk")
	}
}

func TestFluidChainMachineRoot(t *testing.T) {
	plant := fcNode{unit: 5, name: "chemical-plant", labels: []string{"Machine"}, consumes: true}
	g := fcFixture([]fcNode{fcPipe(1), plant, {unit: 9, name: "assembling-machine-2", labels: []string{"Machine"}}, {unit: 8, name: "transport-belt", labels: []string{"Belt"}}},
		[2]int64{1, 5})
	c, err := fcRun(t, g, "5", 50)
	if err != nil || c.Root.ID != "nauvis:5" || len(c.Nodes) != 2 {
		t.Fatalf("machine root: err %v %+v", err, c)
	}
	if _, err := fcRun(t, g, "9", 50); err == nil || !strings.Contains(err.Error(), "no fluid link") {
		t.Fatalf("unlinked machine: %v", err)
	}
	if _, err := fcRun(t, g, "8", 50); err == nil || !strings.Contains(err.Error(), "Belt") {
		t.Fatalf("belt root: %v", err)
	}
	if _, err := fcRun(t, g, "77", 50); err == nil || !strings.Contains(err.Error(), "no node with unit 77") {
		t.Fatalf("missing root: %v", err)
	}
}

func TestFluidChainInputValidation(t *testing.T) {
	g := fcFixture([]fcNode{fcPipe(1)})
	for _, tc := range []struct {
		unit    string
		surface string
		max     int
	}{
		{"1", "nauvis", 0}, {"1", "nauvis", fluidChainMaxNodes + 1}, {"x", "nauvis", 5}, {"0", "nauvis", 5}, {"1", "bad surface!", 5},
	} {
		_, err := fluidChain(context.Background(), g.reader(t), MemgraphFluidChainInput{UnitNumber: tc.unit, Surface: tc.surface, MaxNodes: tc.max})
		if err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	if _, err := fcRun(t, g, "1", fluidChainMaxNodes); err != nil {
		t.Fatalf("max boundary rejected: %v", err)
	}
	if g.reads["root"] != 1 {
		t.Fatalf("invalid inputs reached the reader: %v", g.reads)
	}
}

func TestFluidChainStatementsPassReadOnlyGuard(t *testing.T) {
	for _, q := range []string{fluidChainRootCypher, fluidChainWalkCypher, fluidChainLinksCypher, fluidChainFluidsCypher} {
		if err := validateReadOnlyCypher(q); err != nil {
			t.Fatalf("rejected: %v\n%s", err, q)
		}
	}
}
