package main

import "testing"

func allDefectKinds() map[DefectKind]bool {
	want := map[DefectKind]bool{}
	for _, kind := range defectKindOrder {
		want[kind] = true
	}
	return want
}

func node(name, entityType, unit string, x, y float64) *LogisticsEndpoint {
	unitCopy := unit
	return &LogisticsEndpoint{Name: name, Type: entityType, UnitNumber: &unitCopy, Position: &Position{X: x, Y: y}}
}

func arm(inserter, pickup, drop string) *CompactInserter {
	entry := &CompactInserter{Inserter: "nauvis:" + inserter}
	if pickup != "" {
		value := "nauvis:" + pickup
		entry.Pickup = &value
	}
	if drop != "" {
		value := "nauvis:" + drop
		entry.Drop = &value
	}
	return entry
}

// The joins must flag missing inputs per entity type: a boiler and a furnace
// need an arm, while a steam engine, a drill and a chest never do.
func TestGraphDefectsPerTypeRules(t *testing.T) {
	graph := &FactoryGraph{
		Nodes: []*LogisticsEndpoint{
			node("boiler", "boiler", "10", 0, 0),
			node("steam-engine", "generator", "11", 4, 0),
			node("stone-furnace", "furnace", "12", 8, 0),
			node("electric-mining-drill", "mining-drill", "13", 12, 0),
			node("iron-chest", "container", "14", 16, 0),
			node("lab", "lab", "15", 20, 0),
			node("inserter", "inserter", "20", 1, 1),
		},
		CompactInserters: []*CompactInserter{
			arm("20", "14", "15"), // chest -> lab: lab fed, chest drained
		},
		CompactEdges: []*CompactTopologyEdge{
			{From: "nauvis:14", To: "nauvis:20", Kind: "pickup"},
			{From: "nauvis:20", To: "nauvis:15", Kind: "drop"},
		},
	}
	defects := graphDefects(FactoryDefectsInput{Surface: "nauvis"}, allDefectKinds(), graph)

	kinds := map[DefectKind][]string{}
	for _, defect := range defects {
		kinds[defect.Kind] = append(kinds[defect.Kind], defect.Entity)
	}
	if got := kinds[DefectKindBoilerNoFuelInserter]; len(got) != 1 || got[0] != "boiler" {
		t.Fatalf("boiler rule: %v", got)
	}
	if got := kinds[DefectKindMachineNoInput]; len(got) != 1 || got[0] != "stone-furnace" {
		t.Fatalf("furnace without input must be the only machine row, got %v", got)
	}
	for _, exempt := range []string{"steam-engine", "electric-mining-drill", "iron-chest"} {
		if containsString(kinds[DefectKindMachineNoInput], exempt) {
			t.Fatalf("%s must not be flagged as missing an input", exempt)
		}
	}
	// The chest has an edge and an inserter, so it is not an orphan; the boiler has neither.
	orphans := kinds[DefectKindOrphanEntity]
	if !containsString(orphans, "boiler") || containsString(orphans, "iron-chest") {
		t.Fatalf("orphan rule: %v", orphans)
	}
}

func TestGraphDefectsDanglingAndSharedSource(t *testing.T) {
	graph := &FactoryGraph{
		Nodes: []*LogisticsEndpoint{
			node("inserter", "inserter", "20", 0, 0),
			node("inserter", "inserter", "21", 1, 0),
			node("wooden-chest", "container", "30", 2, 0),
		},
		CompactInserters: []*CompactInserter{
			arm("20", "30", ""),
			arm("21", "30", ""),
		},
	}
	defects := graphDefects(FactoryDefectsInput{Surface: "nauvis"}, allDefectKinds(), graph)
	kinds := map[DefectKind]int{}
	for _, defect := range defects {
		kinds[defect.Kind]++
	}
	if kinds[DefectKindInserterDanglingDrop] != 2 || kinds[DefectKindInserterDanglingPickup] != 0 {
		t.Fatalf("dangling arms: %+v", kinds)
	}
	if kinds[DefectKindInserterSharedSource] != 1 {
		t.Fatalf("two arms from one chest must be one shared-source row, got %d", kinds[DefectKindInserterSharedSource])
	}
	if kinds[DefectKindMachineNoInput] != 0 {
		t.Fatal("a chest must not be reported as a machine missing input")
	}
}

func TestGraphDefectsKindFilter(t *testing.T) {
	graph := &FactoryGraph{
		Nodes: []*LogisticsEndpoint{node("boiler", "boiler", "10", 0, 0)},
	}
	want := map[DefectKind]bool{DefectKindInserterDanglingDrop: true}
	if defects := graphDefects(FactoryDefectsInput{Surface: "nauvis"}, want, graph); len(defects) != 0 {
		t.Fatalf("filtered kinds must not produce rows: %+v", defects)
	}
}

func TestBeltDefectsComponentRows(t *testing.T) {
	page := &BeltComponentPage{Components: []*BeltComponent{{
		ID:    "3",
		Dead:  true,
		Belts: 12,
		Mixed: true,
		Nodes: []*LogisticsEndpoint{node("transport-belt", "transport-belt", "40", 5, 6)},
		StarveHeads: []*LogisticsEndpoint{
			node("transport-belt", "transport-belt", "41", 5, 7),
		},
		StallEnds: []*LogisticsEndpoint{
			node("transport-belt", "transport-belt", "42", 5, 8),
		},
	}}}
	defects := beltDefects(FactoryDefectsInput{Surface: "nauvis"}, allDefectKinds(), page)
	kinds := map[DefectKind]int{}
	for _, defect := range defects {
		kinds[defect.Kind]++
		if defect.Position == nil {
			t.Fatalf("row without a position: %+v", defect)
		}
	}
	for _, want := range []DefectKind{DefectKindBeltDead, DefectKindBeltMixedLane, DefectKindBeltStarveHead, DefectKindBeltStallEnd} {
		if kinds[want] != 1 {
			t.Fatalf("%s rows: %d", want, kinds[want])
		}
	}
}

func TestPowerDefectsProducersOnly(t *testing.T) {
	network := "13"
	page := &PowerEntityPage{PowerEntities: []*PowerEntityInsight{
		{Name: "steam-engine", Type: "generator", UnitNumber: strPtr("50"), Position: &Position{X: 1, Y: 1}},
		{Name: "steam-engine", Type: "generator", UnitNumber: strPtr("51"), NetworkID: &network, Position: &Position{X: 3, Y: 1}},
		{Name: "boiler", Type: "boiler", UnitNumber: strPtr("52"), Position: &Position{X: 5, Y: 1}},
		{Name: "assembling-machine-1", Type: "assembling-machine", UnitNumber: strPtr("53"), Position: &Position{X: 7, Y: 1}},
	}}
	defects := powerDefects(FactoryDefectsInput{Surface: "nauvis"}, allDefectKinds(), page)
	if len(defects) != 1 || *defects[0].UnitNumber != "50" {
		t.Fatalf("only the unplugged generator may be reported: %+v", defects)
	}
	if defects[0].Kind != DefectKindProducerUnplugged {
		t.Fatalf("kind: %s", defects[0].Kind)
	}
}

func TestGhostDefectAggregate(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	ghosts := &GhostsPage{PageInfo: &PageInfo{Total: 5}}
	none := &LogisticNetworkPage{PageInfo: &PageInfo{Total: 0}}
	if defects := ghostDefect(FactoryDefectsInput{Surface: "nauvis", Area: area}, allDefectKinds(), ghosts, none); len(defects) != 1 {
		t.Fatalf("expected one aggregate row, got %+v", defects)
	}
	covered := &LogisticNetworkPage{PageInfo: &PageInfo{Total: 1}}
	if defects := ghostDefect(FactoryDefectsInput{Surface: "nauvis", Area: area}, allDefectKinds(), ghosts, covered); len(defects) != 0 {
		t.Fatalf("a covered area needs no row, got %+v", defects)
	}
	if defects := ghostDefect(FactoryDefectsInput{Surface: "nauvis", Area: area}, allDefectKinds(), &GhostsPage{PageInfo: &PageInfo{Total: 0}}, none); len(defects) != 0 {
		t.Fatalf("no ghosts needs no row, got %+v", defects)
	}
}

func TestFactoryDefectsValidation(t *testing.T) {
	if _, err := factoryDefectsQuery(&Resolver{}, FactoryDefectsInput{Surface: "nauvis", Force: "player"}); err == nil {
		t.Fatal("a missing area must be rejected before any read")
	}
	if _, err := factoryDefectsQuery(&Resolver{}, FactoryDefectsInput{Surface: "nauvis", Force: "player", Area: &FactoryAreaInput{RightBottomX: 8, RightBottomY: 8}, Limit: maxPageSize + 1}); err == nil {
		t.Fatal("an oversized limit must be rejected")
	}
}

func containsString(list []string, want string) bool {
	for _, value := range list {
		if value == want {
			return true
		}
	}
	return false
}
