package main

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
)

// ccSpec is one machine or chest of a canned chain.
type ccSpec struct {
	unit     int64
	kind     string // Machine or Chest
	name     string
	recipe   string
	rates    map[string]float64 // PRODUCES perMinute per item
	infinity []string           // PRODUCES via infinity
	contains []string           // CONTAINS
	makes    map[string]float64 // recipe PRODUCES amount
	needs    map[string]float64 // recipe CONSUMES amount
}

func ccMachine(unit int64, recipe string, rate float64, makes, needs map[string]float64) ccSpec {
	rates := map[string]float64{}
	for item, amount := range makes {
		rates[item] = rate * amount
	}
	return ccSpec{unit: unit, kind: "Machine", name: "assembling-machine-1", recipe: recipe, rates: rates, makes: makes, needs: needs}
}

func ccInfinity(unit int64, items ...string) ccSpec {
	return ccSpec{unit: unit, kind: "Chest", name: "infinity-chest", infinity: items, contains: items}
}

func (s ccSpec) node(extra map[string]any) map[string]any {
	props := map[string]any{"name": s.name}
	if s.recipe != "" {
		props["recipe"] = s.recipe
	}
	return testBoltNode("n:"+strconv.FormatInt(s.unit, 10), s.kind, s.unit, props)
}

// ccReader answers the root lookup, the two chain-capacity statements and the
// supply chain statement from canned data. paths are unit lists, root first.
func ccReader(specs []ccSpec, paths [][]int64, truncated bool, seen *[]MemgraphQueryInput) memgraphReader {
	byUnit := map[int64]ccSpec{}
	for _, s := range specs {
		byUnit[s.unit] = s
	}
	var itemRows, recipeRows [][]any
	for _, s := range specs {
		n := s.node(nil)
		emitted := false
		for _, item := range sortedKeysFloat(s.rates) {
			itemRows = append(itemRows, []any{n, "PRODUCES", item, s.rates[item], "recipe"})
			emitted = true
		}
		for _, item := range s.infinity {
			itemRows = append(itemRows, []any{n, "PRODUCES", item, nil, "infinity"})
			emitted = true
		}
		for _, item := range s.contains {
			itemRows = append(itemRows, []any{n, "CONTAINS", item, nil, nil})
			emitted = true
		}
		if !emitted {
			itemRows = append(itemRows, []any{n, nil, nil, nil, nil})
		}
		for _, item := range sortedKeysFloat(s.makes) {
			recipeRows = append(recipeRows, []any{s.unit, "PRODUCES", item, s.makes[item]})
		}
		for _, item := range sortedKeysFloat(s.needs) {
			recipeRows = append(recipeRows, []any{s.unit, "CONSUMES", item, s.needs[item]})
		}
	}
	var pathRows [][]any
	for _, p := range paths {
		var steps []any
		for i, u := range p {
			if i > 0 {
				steps = append(steps, testBoltNode("n:arm"+strconv.Itoa(i)+"-"+strconv.FormatInt(u, 10), "Inserter", 9000+u, map[string]any{"name": "inserter"}))
			}
			steps = append(steps, byUnit[u].node(nil))
		}
		pathRows = append(pathRows, []any{int64(1), steps})
	}
	root := byUnit[paths[0][0]]
	return func(_ context.Context, in MemgraphQueryInput) (*MemgraphQueryResult, error) {
		if seen != nil {
			*seen = append(*seen, in)
		}
		switch {
		case strings.Contains(in.Cypher, "RETURN r AS node"):
			return &MemgraphQueryResult{Rows: [][]any{{root.node(nil)}}}, nil
		case in.Cypher == chainCapacityItemsCypher:
			return &MemgraphQueryResult{Rows: itemRows}, nil
		case in.Cypher == chainCapacityRecipeCypher:
			return &MemgraphQueryResult{Rows: recipeRows}, nil
		}
		return &MemgraphQueryResult{Rows: pathRows, Truncated: truncated}, nil
	}
}

func sortedKeysFloat(m map[string]float64) []string { return chainCapacitySortedKeys(m) }

func ccRun(t *testing.T, specs []ccSpec, paths [][]int64) *MemgraphChainCapacity {
	t.Helper()
	got, err := chainCapacity(context.Background(), ccReader(specs, paths, false, nil),
		MemgraphChainCapacityInput{UnitNumber: strconv.FormatInt(paths[0][0], 10), Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func ccStage(t *testing.T, stages []*MemgraphStageRate, unit int, item string) *MemgraphStageRate {
	t.Helper()
	for _, s := range stages {
		if s.Node.UnitNumber != nil && *s.Node.UnitNumber == strconv.Itoa(unit) && s.Item == item {
			return s
		}
	}
	t.Fatalf("no stage for unit %d item %s in %v", unit, item, stages)
	return nil
}

func ccNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func ccHasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// ccInserterChain is the live shape of machine 370: inserter <- plate (chest),
// gear (346, plates from the chest) and circuit (143: plate from the chest,
// cable from 144 fed by copper furnaces 439.. which burn ore from chest 207).
func ccInserterChain(furnaces int) ([]ccSpec, [][]int64) {
	specs := []ccSpec{
		ccMachine(370, "inserter", 60, map[string]float64{"inserter": 1}, map[string]float64{"iron-plate": 1, "iron-gear-wheel": 1, "electronic-circuit": 1}),
		ccMachine(346, "iron-gear-wheel", 60, map[string]float64{"iron-gear-wheel": 1}, map[string]float64{"iron-plate": 2}),
		ccMachine(143, "electronic-circuit", 60, map[string]float64{"electronic-circuit": 1}, map[string]float64{"iron-plate": 1, "copper-cable": 3}),
		ccMachine(144, "copper-cable", 60, map[string]float64{"copper-cable": 2}, map[string]float64{"copper-plate": 1}),
		ccInfinity(5, "iron-plate"),
		ccInfinity(207, "copper-ore"),
	}
	paths := [][]int64{{370, 5}, {370, 346, 5}, {370, 143, 5}, {370, 143, 144}}
	for i := range furnaces {
		u := int64(439 + i)
		specs = append(specs, ccMachine(u, "copper-plate", 18.75, map[string]float64{"copper-plate": 1}, map[string]float64{"copper-ore": 1}))
		paths = append(paths, []int64{370, 143, 144, u, 207})
	}
	return specs, paths
}

// TestChainCapacityOneCopperFurnace pins the acceptance arithmetic: one stone
// furnace gives 18.75 copper plate/min, cable doubles it to 37.5, and an
// electronic circuit takes 3 cable, so the inserter chain makes 12.5/min.
func TestChainCapacityOneCopperFurnace(t *testing.T) {
	specs, paths := ccInserterChain(1)
	got := ccRun(t, specs, paths)
	if got.OutputItem == nil || *got.OutputItem != "inserter" {
		t.Fatalf("output item = %v", got.OutputItem)
	}
	ccNear(t, "outputPerMinute", got.OutputPerMinute, 12.5)
	if got.Truncated {
		t.Fatal("not truncated")
	}
	if *got.Stages[0].Node.UnitNumber != "370" || got.Stages[0].Item != "inserter" {
		t.Fatalf("root must be the first stage, got %+v", got.Stages[0])
	}
	ccNear(t, "root supply", got.Stages[0].SupplyPerMinute, 60)
	ccNear(t, "root demand", got.Stages[0].DemandPerMinute, 60)

	// Demand when the root runs flat out: 60 circuits -> 180 cable -> 90 copper plate.
	furnace := ccStage(t, got.Stages, 439, "copper-plate")
	ccNear(t, "furnace supply", furnace.SupplyPerMinute, 18.75)
	ccNear(t, "furnace demand", furnace.DemandPerMinute, 90)
	cable := ccStage(t, got.Stages, 144, "copper-cable")
	ccNear(t, "cable supply", cable.SupplyPerMinute, 120)
	ccNear(t, "cable demand", cable.DemandPerMinute, 180)
	gear := ccStage(t, got.Stages, 346, "iron-gear-wheel")
	ccNear(t, "gear supply", gear.SupplyPerMinute, 60)
	ccNear(t, "gear demand", gear.DemandPerMinute, 60)

	// Infinity chest: supply equals the demand placed on it
	// (60 root + 120 gear + 60 circuit plates); copper ore 90 for the furnace.
	plates := ccStage(t, got.Stages, 5, "iron-plate")
	ccNear(t, "chest supply", plates.SupplyPerMinute, 240)
	ccNear(t, "chest demand", plates.DemandPerMinute, 240)
	ccNear(t, "ore demand", ccStage(t, got.Stages, 207, "copper-ore").DemandPerMinute, 90)

	// Limiting: furnace (18.75/90) before cable (120/180).
	if len(got.Limiting) != 2 || *got.Limiting[0].Node.UnitNumber != "439" || *got.Limiting[1].Node.UnitNumber != "144" {
		t.Fatalf("limiting = %+v, want furnace 439 then cable 144", got.Limiting)
	}
	for _, want := range []string{"Inserter and belt throughput are not modelled", "Infinity chests are treated as unbounded"} {
		if !ccHasNote(got.Notes, want) {
			t.Fatalf("missing note %q in %v", want, got.Notes)
		}
	}
}

// More furnaces raise the output until the cable assembler (120 cable/min =
// 40 circuits/min) caps it, and then it is the tightest stage.
func TestChainCapacityScalesUntilCableCaps(t *testing.T) {
	for _, tc := range []struct {
		furnaces int
		want     float64
	}{{1, 12.5}, {2, 25}, {3, 37.5}, {4, 40}, {7, 40}} {
		specs, paths := ccInserterChain(tc.furnaces)
		got := ccRun(t, specs, paths)
		ccNear(t, "outputPerMinute with "+strconv.Itoa(tc.furnaces)+" furnaces", got.OutputPerMinute, tc.want)
	}
	specs, paths := ccInserterChain(7)
	got := ccRun(t, specs, paths)
	if len(got.Limiting) != 1 || *got.Limiting[0].Node.UnitNumber != "144" {
		t.Fatalf("cable assembler must be the only limiting stage with 7 furnaces, got %+v", got.Limiting)
	}
	// Demand 90 plate/min is split evenly over 7 identical furnaces.
	ccNear(t, "furnace demand", ccStage(t, got.Stages, 439, "copper-plate").DemandPerMinute, 90.0/7)
}

// A producer shared by the root and a second consumer is split in proportion
// to the demand each places on it: A makes 30 a/min, root wants 60 and B wants
// 60, so each side gets 15 and the root (needs a and b 1:1) makes 15.
func TestChainCapacitySharedProducerSplitsByDemand(t *testing.T) {
	specs := []ccSpec{
		ccMachine(1, "x", 60, map[string]float64{"x": 1}, map[string]float64{"a": 1, "b": 1}),
		ccMachine(2, "b", 60, map[string]float64{"b": 1}, map[string]float64{"a": 1}),
		ccMachine(3, "a", 30, map[string]float64{"a": 1}, nil),
	}
	got := ccRun(t, specs, [][]int64{{1, 3}, {1, 2, 3}})
	ccNear(t, "outputPerMinute", got.OutputPerMinute, 15)
	ccNear(t, "A demand", ccStage(t, got.Stages, 3, "a").DemandPerMinute, 120)
	if len(got.Limiting) != 1 || *got.Limiting[0].Node.UnitNumber != "3" {
		t.Fatalf("limiting = %+v, want only producer 3", got.Limiting)
	}
	if !ccHasNote(got.Notes, "split in proportion") {
		t.Fatalf("missing sharing note in %v", got.Notes)
	}
}

// An ingredient nothing on the chain produces makes the output 0 and the note
// names it.
func TestChainCapacityMissingProducer(t *testing.T) {
	specs := []ccSpec{
		ccMachine(1, "x", 60, map[string]float64{"x": 1}, map[string]float64{"a": 1, "b": 1}),
		ccMachine(3, "a", 30, map[string]float64{"a": 1}, nil),
	}
	got := ccRun(t, specs, [][]int64{{1, 3}})
	ccNear(t, "outputPerMinute", got.OutputPerMinute, 0)
	if !ccHasNote(got.Notes, "needs b but nothing on the chain produces it") {
		t.Fatalf("missing note in %v", got.Notes)
	}
}

// A stocked, non-infinity chest with no producer behind it is counted as an
// unbounded stock and said so; a chest filled by a producer defers to it.
func TestChainCapacityFiniteChest(t *testing.T) {
	chest := ccSpec{unit: 9, kind: "Chest", name: "wooden-chest", contains: []string{"a"}}
	specs := []ccSpec{
		ccMachine(1, "x", 60, map[string]float64{"x": 1}, map[string]float64{"a": 1}),
		chest,
	}
	got := ccRun(t, specs, [][]int64{{1, 9}})
	ccNear(t, "outputPerMinute (stock)", got.OutputPerMinute, 60)
	if !ccHasNote(got.Notes, "unbounded stock") {
		t.Fatalf("missing stock note in %v", got.Notes)
	}

	specs = []ccSpec{
		ccMachine(1, "x", 60, map[string]float64{"x": 1}, map[string]float64{"a": 1}),
		chest,
		ccMachine(3, "a", 20, map[string]float64{"a": 1}, nil),
	}
	got = ccRun(t, specs, [][]int64{{1, 9, 3}})
	ccNear(t, "outputPerMinute (through chest)", got.OutputPerMinute, 20)
	if ccHasNote(got.Notes, "unbounded stock") {
		t.Fatalf("producer behind the chest must be used: %v", got.Notes)
	}
}

// Two machines that feed each other terminate; the back edge is reported.
func TestChainCapacityCycle(t *testing.T) {
	specs := []ccSpec{
		ccMachine(1, "x", 60, map[string]float64{"x": 1}, map[string]float64{"a": 1}),
		ccMachine(2, "a", 60, map[string]float64{"a": 1}, map[string]float64{"x": 1}),
	}
	got := ccRun(t, specs, [][]int64{{1, 2, 1}})
	// The dropped back edge leaves unit 2 without x, so nothing bootstraps the loop.
	ccNear(t, "outputPerMinute", got.OutputPerMinute, 0)
	if !ccHasNote(got.Notes, "Feed cycle ignored") {
		t.Fatalf("missing cycle note in %v", got.Notes)
	}
}

func TestChainCapacityTruncatedAndValidation(t *testing.T) {
	specs, paths := ccInserterChain(1)
	got, err := chainCapacity(context.Background(), ccReader(specs, paths, true, nil),
		MemgraphChainCapacityInput{UnitNumber: "370", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || !ccHasNote(got.Notes, "cut by the path limit") {
		t.Fatalf("truncation must be reported: %+v", got.Notes)
	}

	read := ccReader(specs, paths, false, nil)
	for name, in := range map[string]MemgraphChainCapacityInput{
		"bad unit":      {UnitNumber: "abc", Surface: "nauvis", MaxHops: 10},
		"bad surface":   {UnitNumber: "370", Surface: "na uvis", MaxHops: 10},
		"hops too low":  {UnitNumber: "370", Surface: "nauvis", MaxHops: 0},
		"hops too high": {UnitNumber: "370", Surface: "nauvis", MaxHops: 17},
	} {
		if _, err := chainCapacity(context.Background(), read, in); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestChainCapacityRootKinds(t *testing.T) {
	// A chest root is rejected.
	chestSpecs := []ccSpec{ccInfinity(5, "iron-plate")}
	_, err := chainCapacity(context.Background(), ccReader(chestSpecs, [][]int64{{5}}, false, nil),
		MemgraphChainCapacityInput{UnitNumber: "5", Surface: "nauvis", MaxHops: 10})
	if err == nil || !strings.Contains(err.Error(), "is a Chest") {
		t.Fatalf("want a kind error naming Chest, got %v", err)
	}

	// A machine without a recipe has a null output item and never queries the chain.
	idle := ccSpec{unit: 7, kind: "Machine", name: "assembling-machine-1"}
	var seen []MemgraphQueryInput
	got, err := chainCapacity(context.Background(), ccReader([]ccSpec{idle}, [][]int64{{7}}, false, &seen),
		MemgraphChainCapacityInput{UnitNumber: "7", Surface: "nauvis", MaxHops: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.OutputItem != nil || got.OutputPerMinute != 0 || len(got.Stages) != 0 || len(got.Limiting) != 0 || len(got.Notes) == 0 {
		t.Fatalf("unexpected result for a recipe-less machine: %+v", got)
	}
	if len(seen) != 1 {
		t.Fatalf("only the root lookup may run, got %d statements", len(seen))
	}
}

// The item and recipe statements are parameterized with the chain's units, so
// values never reach the statement text.
func TestChainCapacityStatementsAreParameterized(t *testing.T) {
	specs, paths := ccInserterChain(1)
	var seen []MemgraphQueryInput
	if _, err := chainCapacity(context.Background(), ccReader(specs, paths, false, &seen),
		MemgraphChainCapacityInput{UnitNumber: "370", Surface: "nauvis", MaxHops: 10}); err != nil {
		t.Fatal(err)
	}
	for _, in := range seen {
		if in.Cypher != chainCapacityItemsCypher && in.Cypher != chainCapacityRecipeCypher {
			continue
		}
		p := in.Params.(map[string]any)
		units := p["units"].([]any)
		if p["surface"] != "nauvis" || len(units) != 7 || units[0] != int64(5) {
			t.Fatalf("params = %v", p)
		}
		if strings.Contains(in.Cypher, "370") || strings.Contains(in.Cypher, "nauvis") {
			t.Fatalf("values must not be inlined: %s", in.Cypher)
		}
	}
}
