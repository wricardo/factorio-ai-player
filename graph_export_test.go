package main

import (
	"strings"
	"testing"
)

// Every non-empty chunk must land in exactly one leaf and leaves must never
// overlap, or the census reconciliation could not prove completeness.
func TestPlanGraphLeavesPartitionsChunks(t *testing.T) {
	var chunks []graphChunk
	// A dense block that must split, sparse chunks across the origin (negative
	// coordinates align by floor division), and one hot chunk over the limit.
	for x := -3; x < 5; x++ {
		for y := -2; y < 6; y++ {
			chunks = append(chunks, graphChunk{X: x, Y: y, N: 40})
		}
	}
	chunks = append(chunks, graphChunk{X: -17, Y: 30, N: 5}, graphChunk{X: 40, Y: -9, N: 1}, graphChunk{X: 12, Y: 12, N: 900})
	leaves := planGraphLeaves(chunks, 100)

	owner := map[[2]int]int{}
	for i, l := range leaves {
		if l.Size < 1 || l.Size > graphRootChunks {
			t.Fatalf("leaf %d size %d", i, l.Size)
		}
		if l.Count > 100 && l.Size != 1 {
			t.Fatalf("leaf %+v exceeds the limit but could still split", l)
		}
		for x := l.CX; x < l.CX+l.Size; x++ {
			for y := l.CY; y < l.CY+l.Size; y++ {
				if prev, taken := owner[[2]int{x, y}]; taken {
					t.Fatalf("chunk %d,%d in leaves %d and %d", x, y, prev, i)
				}
				owner[[2]int{x, y}] = i
			}
		}
	}
	for _, c := range chunks {
		i, ok := owner[[2]int{c.X, c.Y}]
		if !ok {
			t.Fatalf("chunk %+v is in no leaf", c)
		}
		lx, ly, rx, ry := leaves[i].tiles()
		cx, cy := float64(c.X*graphChunkTiles), float64(c.Y*graphChunkTiles)
		if cx < lx || cy < ly || cx+graphChunkTiles > rx || cy+graphChunkTiles > ry {
			t.Fatalf("chunk %+v outside its leaf tiles %v,%v-%v,%v", c, lx, ly, rx, ry)
		}
	}
}

func TestReconcileGraphSurface(t *testing.T) {
	for _, tc := range []struct {
		before, after, exported int
		want                    string
	}{
		{10, 10, 10, graphStatusComplete},
		{10, 11, 10, graphStatusDrift},
		{10, 9, 10, graphStatusDrift},
		{10, 10, 9, graphStatusIncomplete},
		{10, 10, 11, graphStatusIncomplete},
	} {
		if got, _ := reconcileGraphSurface(tc.before, tc.after, tc.exported); got != tc.want {
			t.Errorf("reconcile(%d,%d,%d)=%s, want %s", tc.before, tc.after, tc.exported, got, tc.want)
		}
	}
	if worseGraphStatus(graphStatusDrift, graphStatusComplete) != graphStatusDrift ||
		worseGraphStatus(graphStatusDrift, graphStatusIncomplete) != graphStatusIncomplete {
		t.Fatal("status ordering")
	}
}

func graphTestRef(id, typ string) graphRef {
	return graphRef{ID: id, Name: typ, Type: typ, Surface: "nauvis", Force: "player"}
}

func graphTestEdges(t *testing.T, b *graphBuilder) map[string]*graphEdgeOut {
	t.Helper()
	if err := b.finish(); err != nil {
		t.Fatal(err)
	}
	return b.edges
}

// Raw engine links arrive per owning entity, with pickups pointing at the arm
// and fluid and wire links reported from both ends; finish must turn them into
// one relationship each, oriented as the schema documents.
func TestGraphBuilderRoutesLinks(t *testing.T) {
	b := newGraphBuilder("snap", "player")
	payload := &graphLeafPayload{
		Nodes: []graphRawNode{
			{ID: "nauvis:1", Type: "transport-belt", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:2", Type: "inserter", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:3", Type: "furnace", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:4", Type: "mining-drill", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:5", Type: "pipe", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:6", Type: "pipe", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:7", Type: "constant-combinator", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:8", Type: "lamp", Surface: "nauvis", Force: "player"},
		},
		Refs: []graphRef{graphTestRef("nauvis:1", "transport-belt"), graphTestRef("nauvis:2", "inserter"), graphTestRef("nauvis:3", "furnace"),
			graphTestRef("nauvis:4", "mining-drill"), graphTestRef("nauvis:5", "pipe"), graphTestRef("nauvis:6", "pipe"),
			graphTestRef("nauvis:7", "constant-combinator"), graphTestRef("nauvis:8", "lamp")},
		Edges: []graphRawEdge{
			{Kind: "pickup", From: "nauvis:1", To: "nauvis:2"},
			{Kind: "drop", From: "nauvis:2", To: "nauvis:3"},
			{Kind: "drop", From: "nauvis:4", To: "nauvis:1"},
			{Kind: "fluid", From: "nauvis:6", To: "nauvis:5", FlowDirection: "output"},
			{Kind: "fluid", From: "nauvis:5", To: "nauvis:6", FlowDirection: "input-output"},
			{Kind: "fluid", From: "nauvis:6", To: "nauvis:5", FlowDirection: "output"},
			{Kind: "red", From: "nauvis:8", To: "nauvis:7", FromConnector: "circuit_red", ToConnector: "circuit_red"},
			{Kind: "red", From: "nauvis:7", To: "nauvis:8", FromConnector: "circuit_red", ToConnector: "circuit_red"},
		},
	}
	if err := b.add(payload, false); err != nil {
		t.Fatal(err)
	}
	edges := graphTestEdges(t, b)
	byType := map[string][]*graphEdgeOut{}
	for _, e := range edges {
		byType[e.Type] = append(byType[e.Type], e)
	}
	if e := byType["PICKS_FROM"]; len(e) != 1 || e[0].From != "nauvis:2" || e[0].To != "nauvis:1" {
		t.Fatalf("pickup must hang on the arm: %+v", e)
	}
	if e := byType["DROPS_TO"]; len(e) != 1 || e[0].From != "nauvis:2" || e[0].To != "nauvis:3" {
		t.Fatalf("inserter drop: %+v", e)
	}
	if e := byType["DROPS_ON"]; len(e) != 1 || e[0].From != "nauvis:4" {
		t.Fatalf("machine drop: %+v", e)
	}
	fluid := byType["FLUID_LINK"]
	if len(fluid) != 1 {
		t.Fatalf("mirrored fluid ports must fold into one edge, got %d", len(fluid))
	}
	f := fluid[0]
	if f.From != "nauvis:5" || f.To != "nauvis:6" || f.Props["dirA"] != "input-output" || f.Props["dirB"] != "output" || f.Props["ports"] != 2 {
		t.Fatalf("fluid edge %+v", f)
	}
	for k := range f.Props {
		if strings.HasPrefix(k, "seen:") {
			t.Fatalf("bookkeeping property %q leaked", k)
		}
	}
	if w := byType["CIRCUIT_WIRE"]; len(w) != 1 || w[0].From != "nauvis:7" || w[0].Props["color"] != "red" {
		t.Fatalf("wire seen from both ends must be one canonical edge: %+v", w)
	}
}

// A furnace that reports an empty fuel inventory must keep fuelCount 0 (so a
// query can tell "burner with no fuel" from "no fuel inventory"), and inserter
// drops onto a belt must carry the lane they land on.
func TestGraphBuilderKeepsContentsAndLane(t *testing.T) {
	zero, three := int64(0), int64(3)
	b := newGraphBuilder("snap", "player")
	payload := &graphLeafPayload{
		Nodes: []graphRawNode{
			{ID: "nauvis:1", Type: "furnace", Surface: "nauvis", Force: "player", FuelCount: &zero, InputCount: &three, Input: []string{"copper-ore:3"}},
			{ID: "nauvis:2", Type: "transport-belt", Surface: "nauvis", Force: "player", LeftCount: &three, Left: []string{"copper-ore:3"}, RightCount: &zero},
			{ID: "nauvis:3", Type: "inserter", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:4", Type: "electric-mining-drill", Surface: "nauvis", Force: "player"},
		},
		Refs: []graphRef{graphTestRef("nauvis:1", "furnace"), graphTestRef("nauvis:2", "transport-belt"), graphTestRef("nauvis:3", "inserter")},
		Edges: []graphRawEdge{
			{Kind: "pickup", From: "nauvis:2", To: "nauvis:3", Side: "left"},
			{Kind: "drop", From: "nauvis:3", To: "nauvis:2", Side: "right"},
			{Kind: "drop", From: "nauvis:3", To: "nauvis:1"},
		},
	}
	if err := b.add(payload, false); err != nil {
		t.Fatal(err)
	}
	furnace := b.owned["nauvis:1"].Props
	if v, ok := furnace["fuelCount"]; !ok || v != int64(0) {
		t.Fatalf("empty fuel inventory must stay explicit: %+v", furnace)
	}
	if _, ok := furnace["fuel"]; ok {
		t.Fatalf("empty list must not be stored: %+v", furnace)
	}
	if _, ok := b.owned["nauvis:4"].Props["fuelCount"]; ok {
		t.Fatal("entity without a fuel inventory must not gain fuelCount")
	}
	if belt := b.owned["nauvis:2"].Props; belt["rightCount"] != int64(0) || belt["left"] == nil {
		t.Fatalf("belt lanes: %+v", belt)
	}
	edges := graphTestEdges(t, b)
	for _, e := range edges {
		if e.Type == "PICKS_FROM" && e.Props["side"] != "left" {
			t.Fatalf("inserter standing side lost: %+v", e.Props)
		}
		if e.Type != "DROPS_TO" {
			continue
		}
		side, has := e.Props["side"]
		if e.To == "nauvis:2" && side != "right" {
			t.Fatalf("drop lane onto a belt lost: %+v", e.Props)
		}
		if e.To == "nauvis:1" && has {
			t.Fatalf("machine drop must not carry a side: %+v", e.Props)
		}
	}
}

func TestGraphBuilderRejectsUnknownKindAndDoubleOwnership(t *testing.T) {
	b := newGraphBuilder("snap", "player")
	node := graphRawNode{ID: "nauvis:1", Type: "pipe", Surface: "nauvis", Force: "player"}
	if err := b.add(&graphLeafPayload{Nodes: []graphRawNode{node}}, false); err != nil {
		t.Fatal(err)
	}
	if err := b.add(&graphLeafPayload{Nodes: []graphRawNode{node}}, false); err == nil {
		t.Fatal("an entity owned by two leaves must be an error")
	}
	b.raw = append(b.raw, graphRawEdge{Kind: "teleport", From: "nauvis:1", To: "nauvis:2"})
	if err := b.finish(); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("unknown link kind must fail, got %v", err)
	}
}

// Endpoints no leaf owns (neutral resources, other forces) become External
// stubs; owned endpoints never do.
func TestGraphBuilderExternalNodes(t *testing.T) {
	b := newGraphBuilder("snap", "player")
	if err := b.add(&graphLeafPayload{
		Nodes: []graphRawNode{{ID: "nauvis:4", Type: "mining-drill", Surface: "nauvis", Force: "player"}},
		Refs:  []graphRef{graphTestRef("nauvis:4", "mining-drill"), {ID: "nauvis:resource:iron-ore:1.5:2.5", Type: "resource", Name: "iron-ore", Surface: "nauvis", Force: "neutral"}},
		Edges: []graphRawEdge{{Kind: "mines", From: "nauvis:4", To: "nauvis:resource:iron-ore:1.5:2.5"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := b.finish(); err != nil {
		t.Fatal(err)
	}
	ext := b.externalNodes()
	if len(ext) != 1 || ext[0].ID != "nauvis:resource:iron-ore:1.5:2.5" {
		t.Fatalf("external nodes %+v", ext)
	}
	labels := strings.Join(ext[0].Labels, ",")
	if labels != "Entity,ResourceTile,External" {
		t.Fatalf("resource stub labels %s", labels)
	}
}

// Labels and relationship types are spliced into Cypher text, so anything
// outside the schema's fixed vocabulary must be refused.
func TestGraphQueriesRejectForeignIdentifiers(t *testing.T) {
	if _, err := graphNodeQuery([]string{"Entity", "Belt`) DETACH DELETE n //"}); err == nil {
		t.Fatal("label injection accepted")
	}
	if _, err := graphNodeQuery([]string{"Snapshot"}); err == nil {
		t.Fatal("node rows must be Entity or Network")
	}
	if _, err := graphEdgeQuery("DROP", "Entity", "Entity"); err == nil {
		t.Fatal("unknown relationship type accepted")
	}
	if _, err := graphEdgeQuery("BELT_OUT", "Snapshot", "Entity"); err == nil {
		t.Fatal("unknown endpoint label accepted")
	}
}

// A splitter reports its two output ports and each neighbouring belt reports the
// same link back without them; both orders must end as one edge carrying the
// port, never as two edges or as an edge whose port depends on arrival order.
func TestGraphBuilderMergesSplitterPorts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		first   graphRawEdge
		second  graphRawEdge
		wantKey string
	}{
		{
			name:    "port then plain",
			first:   graphRawEdge{Kind: "belt", From: "nauvis:10", To: "nauvis:11", FromPort: "left"},
			second:  graphRawEdge{Kind: "belt", From: "nauvis:10", To: "nauvis:11"},
			wantKey: "left",
		},
		{
			name:    "plain then port",
			first:   graphRawEdge{Kind: "belt", From: "nauvis:10", To: "nauvis:11"},
			second:  graphRawEdge{Kind: "belt", From: "nauvis:10", To: "nauvis:11", FromPort: "right"},
			wantKey: "right",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newGraphBuilder("snap", "player")
			if err := b.add(&graphLeafPayload{
				Nodes: []graphRawNode{
					{ID: "nauvis:10", Type: "splitter", Surface: "nauvis", Force: "player"},
					{ID: "nauvis:11", Type: "transport-belt", Surface: "nauvis", Force: "player"},
				},
				Edges: []graphRawEdge{tc.first, tc.second},
			}, false); err != nil {
				t.Fatal(err)
			}
			edges := graphTestEdges(t, b)
			belt := edges["BELT_OUT|nauvis:10|nauvis:11|belt"]
			if belt == nil {
				t.Fatalf("no belt edge: %v", edges)
			}
			if belt.Props["fromPort"] != tc.wantKey {
				t.Fatalf("fromPort=%v, want %s", belt.Props["fromPort"], tc.wantKey)
			}
			if _, ok := belt.Props["toPort"]; ok {
				t.Fatalf("toPort must stay absent on a splitter output: %v", belt.Props)
			}
			beltOut := 0
			for key := range edges {
				if strings.HasPrefix(key, "BELT_OUT|") {
					beltOut++
				}
			}
			if beltOut != 1 {
				t.Fatalf("ports must not split the edge: %d BELT_OUT edges", beltOut)
			}
		})
	}
}

// Geometry resolves both ports; when it cannot (a direction the engine reports
// oddly, a missing bounding box) the port is omitted rather than filled with
// the neighbour-table key, and the raw key stays in its own property.
func TestGraphBuilderKeepsPortVocabulary(t *testing.T) {
	b := newGraphBuilder("snap", "player")
	if err := b.add(&graphLeafPayload{
		Nodes: []graphRawNode{
			{ID: "nauvis:1", Type: "splitter", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:2", Type: "transport-belt", Surface: "nauvis", Force: "player"},
		},
		Edges: []graphRawEdge{{Kind: "belt", From: "nauvis:1", To: "nauvis:2", FromPortIndex: "2"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	e := graphTestEdges(t, b)["BELT_OUT|nauvis:1|nauvis:2|belt"]
	if e == nil {
		t.Fatal("no belt edge")
	}
	if _, ok := e.Props["fromPort"]; ok {
		t.Fatalf("an unresolved side must not become a port: %v", e.Props)
	}
	if e.Props["fromPortIndex"] != "2" {
		t.Fatalf("raw neighbour key=%v, want 2", e.Props["fromPortIndex"])
	}
}

// The Snapshot node carries the export's final read tick so readers can size
// graph staleness; a manifest written before the field existed must not decay
// into a false tick 0.
func TestGraphSnapshotPropsCarriesCaptureTick(t *testing.T) {
	m := &graphManifest{Snapshot: "snap-1", Force: "player", Status: "complete", Tick: 1147897,
		Surfaces: []graphSurfaceReport{{Surface: "nauvis", Scope: "nauvis|player", Status: "complete"}}}
	props := graphSnapshotProps(m, "/tmp/snap-1")
	if tick, ok := props["tick"].(float64); !ok || tick != 1147897 {
		t.Fatalf("tick prop = %v, want 1147897", props["tick"])
	}
	m.Tick = 0
	props = graphSnapshotProps(m, "/tmp/snap-1")
	if _, ok := props["tick"]; ok {
		t.Fatalf("tick prop present for a pre-field manifest: %v", props["tick"])
	}
}

// A logistic container's engine mode picks exactly one role label next to the
// shared :LogisticChest, a container with no mode gets none, and a roboport's
// coverage link keeps whether the chest is also inside its construction area.
func TestGraphBuilderLogisticChestRolesAndCoverage(t *testing.T) {
	yes, no := true, false
	b := newGraphBuilder("snap", "player")
	if err := b.add(&graphLeafPayload{
		Nodes: []graphRawNode{
			{ID: "nauvis:1", Type: "roboport", Surface: "nauvis", Force: "player"},
			{ID: "nauvis:2", Type: "logistic-container", Surface: "nauvis", Force: "player", LogisticMode: "active-provider"},
			{ID: "nauvis:3", Type: "logistic-container", Surface: "nauvis", Force: "player", LogisticMode: "buffer"},
			{ID: "nauvis:4", Type: "infinity-container", Surface: "nauvis", Force: "player"},
		},
		Edges: []graphRawEdge{
			{Kind: "logistic_cover", From: "nauvis:1", To: "nauvis:2", Construction: &yes},
			{Kind: "logistic_cover", From: "nauvis:1", To: "nauvis:3", Construction: &no},
		},
	}, false); err != nil {
		t.Fatal(err)
	}
	edges := graphTestEdges(t, b)
	want := map[string]string{"nauvis:2": "ActiveProviderChest", "nauvis:3": "BufferChest"}
	for id, role := range want {
		labels := strings.Join(b.owned[id].Labels, ",")
		if labels != "Entity,Chest,LogisticChest,"+role {
			t.Errorf("%s labels=%s, want Entity,Chest,LogisticChest,%s", id, labels, role)
		}
	}
	if got := strings.Join(b.owned["nauvis:4"].Labels, ","); got != "Entity,Chest" {
		t.Errorf("container without a logistic mode labels=%s, want Entity,Chest", got)
	}
	if e := edges["LOGISTIC_COVERS|nauvis:1|nauvis:2|"]; e == nil || e.Props["construction"] != true {
		t.Errorf("coverage inside construction area: %v", e)
	}
	if e := edges["LOGISTIC_COVERS|nauvis:1|nauvis:3|"]; e == nil || e.Props["construction"] != false {
		t.Errorf("coverage outside construction area: %v", e)
	}
	// A chest that changes mode in place must drop its old role label on reload.
	managed := strings.Join(graphManagedLabels(), ",")
	for _, l := range []string{"LogisticChest", "ActiveProviderChest", "PassiveProviderChest", "StorageChest", "RequesterChest", "BufferChest"} {
		if !strings.Contains(managed, l) {
			t.Errorf("%s is not a managed label, a mode change would leave it behind", l)
		}
	}
}
