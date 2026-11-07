package main

import (
	"encoding/json"
	"sort"
	"testing"
)

// prodFixture builds a builder out of hand-written entity nodes and the
// catalog the export would have read, so production rules are tested without
// an engine.
type prodFixture struct {
	b *graphBuilder
}

func newProdFixture() *prodFixture {
	return &prodFixture{b: newGraphBuilder("snap-test", "player")}
}

// node adds one entity node like nodeOut would, with the given category labels.
func (f *prodFixture) node(unit int64, name string, labels []string, props map[string]any) string {
	id := "nauvis:" + itoa(unit)
	p := map[string]any{"id": id, "name": name, "type": name, "surface": "nauvis", "force": "player",
		"unit": unit, "x": 0.0, "y": 0.0, "snapshot": "snap-test", "scope": "nauvis|player"}
	for k, v := range props {
		p[k] = v
	}
	f.b.owned[id] = &graphNodeOut{ID: id, Labels: append([]string{"Entity"}, labels...), Props: p}
	f.b.types[id] = name
	return id
}

func (f *prodFixture) catalog() *graphCatalog {
	return &graphCatalog{
		Recipes:   map[string]graphCatalogRecipe{},
		Resources: map[string]graphCatalogResource{},
		Machines:  map[string]graphCatalogMachine{},
	}
}

func (f *prodFixture) build(t *testing.T, c *graphCatalog) {
	t.Helper()
	if err := f.b.buildProduction(c); err != nil {
		t.Fatalf("buildProduction: %v", err)
	}
}

func (f *prodFixture) edgesOf(typ string) []*graphEdgeOut {
	var out []*graphEdgeOut
	for _, e := range f.b.edges {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// edge returns the single edge of one type between two nodes.
func (f *prodFixture) edge(t *testing.T, typ, from, to string) *graphEdgeOut {
	t.Helper()
	for _, e := range f.b.edges {
		if e.Type == typ && e.From == from && e.To == to {
			return e
		}
	}
	t.Fatalf("no %s edge %s -> %s", typ, from, to)
	return nil
}

func product(typ, name string, amount float64) graphCatalogProduct {
	return graphCatalogProduct{Type: typ, Name: name, Amount: new(amount), Probability: new(1.0)}
}

// A stone furnace running iron-plate: RUNS to the recipe, per-minute rates on
// both sides, and per-craft amounts on the recipe.
func TestProductionRecipeRates(t *testing.T) {
	f := newProdFixture()
	id := f.node(4, "stone-furnace", []string{"Machine"}, map[string]any{"recipe": "iron-plate"})
	c := f.catalog()
	c.Recipes["iron-plate"] = graphCatalogRecipe{
		Energy: 3.2, Category: "smelting",
		Ingredients: luaList[graphCatalogProduct]{product("item", "iron-ore", 1)},
		Products:    luaList[graphCatalogProduct]{product("item", "iron-plate", 1)},
	}
	c.Machines["stone-furnace"] = graphCatalogMachine{CraftingSpeed: new(1.0)}
	f.build(t, c)

	recipeID := "nauvis:recipe:iron-plate"
	itemID := "nauvis:item:iron-plate"
	if r := f.b.derived[recipeID]; r == nil || r.Props["energy"] != 3.2 {
		t.Fatalf("recipe node = %+v, want energy 3.2", r)
	}
	if e := f.edge(t, "RUNS", id, recipeID); e.FromLabel != "Entity" || e.ToLabel != "Recipe" {
		t.Fatalf("RUNS labels = %s -> %s", e.FromLabel, e.ToLabel)
	}
	if e := f.edge(t, "PRODUCES", id, itemID); e.Props["perMinute"] != 18.75 || e.Props["via"] != "recipe" {
		t.Fatalf("entity PRODUCES props = %v, want perMinute 18.75 via recipe", e.Props)
	}
	if e := f.edge(t, "CONSUMES", id, "nauvis:item:iron-ore"); e.Props["perMinute"] != 18.75 {
		t.Fatalf("entity CONSUMES props = %v, want perMinute 18.75", e.Props)
	}
	if e := f.edge(t, "PRODUCES", recipeID, itemID); e.Props["amount"] != 1.0 || e.ToLabel != "Item" {
		t.Fatalf("recipe PRODUCES props = %v", e.Props)
	}
}

// A fluid recipe with three products keeps every product and its fluid kind.
func TestProductionFluidProducts(t *testing.T) {
	f := newProdFixture()
	id := f.node(7, "oil-refinery", []string{"Machine"}, map[string]any{"recipe": "advanced-oil-processing"})
	c := f.catalog()
	c.Recipes["advanced-oil-processing"] = graphCatalogRecipe{
		Energy: 5,
		Ingredients: luaList[graphCatalogProduct]{
			product("fluid", "water", 50), product("fluid", "crude-oil", 100),
		},
		Products: luaList[graphCatalogProduct]{
			product("fluid", "heavy-oil", 25), product("fluid", "light-oil", 45), product("fluid", "petroleum-gas", 55),
		},
	}
	c.Machines["oil-refinery"] = graphCatalogMachine{CraftingSpeed: new(1.0)}
	f.build(t, c)

	if got := f.edge(t, "PRODUCES", id, "nauvis:item:petroleum-gas").Props["perMinute"]; got != 660.0 {
		t.Fatalf("petroleum-gas perMinute = %v, want 660", got)
	}
	for _, e := range f.edgesOf("PRODUCES") {
		if e.To == "nauvis:item:crude-oil" {
			t.Fatalf("crude-oil must be an ingredient, not a product")
		}
	}
	item := f.b.derived["nauvis:item:heavy-oil"]
	if item == nil || item.Props["kind"] != "fluid" {
		t.Fatalf("heavy-oil item = %+v, want kind fluid", item)
	}
	if e := f.edge(t, "CONSUMES", id, "nauvis:item:water"); e.Props["perMinute"] != 600.0 {
		t.Fatalf("water perMinute = %v, want 600", e.Props["perMinute"])
	}
}

// A drill produces what its mining target yields, through the productivity bonus.
func TestProductionMining(t *testing.T) {
	f := newProdFixture()
	drill := f.node(9, "electric-mining-drill", []string{"MiningDrill"}, nil)
	ore := "nauvis:ore:iron-ore:-5:3"
	f.b.refs[ore] = graphRef{ID: ore, Name: "iron-ore", Type: "resource", Surface: "nauvis", Force: "player"}
	f.b.edge("MINES", drill, "Entity", ore, "Entity", "", nil, "nauvis")

	c := f.catalog()
	c.MiningProductivity = 0.1
	c.Machines["electric-mining-drill"] = graphCatalogMachine{MiningSpeed: new(0.5)}
	c.Resources["iron-ore"] = graphCatalogResource{
		MiningTime: 1, Products: luaList[graphCatalogProduct]{product("item", "iron-ore", 1)},
	}
	f.build(t, c)

	e := f.edge(t, "PRODUCES", drill, "nauvis:item:iron-ore")
	if e.Props["perMinute"] != 33.0 || e.Props["via"] != "mining" {
		t.Fatalf("mining PRODUCES props = %v, want perMinute 33 via mining", e.Props)
	}
}

// Infinity chests produce their filters (no rate) and contain their stock.
func TestProductionInfinity(t *testing.T) {
	f := newProdFixture()
	unit := int64(2)
	chest := f.node(unit, "infinity-chest", []string{"Chest"}, nil)
	c := f.catalog()
	c.Infinity = luaList[graphCatalogInfinity]{{Unit: &unit, Surface: "nauvis",
		Filters:  luaList[graphCatalogFilter]{{Name: "iron-ore", Quality: "normal", Count: new(int64(50)), Mode: "at-least"}},
		Contents: luaList[graphCatalogContent]{{Name: "iron-ore", Quality: "normal", Count: 50}},
	}}
	f.build(t, c)

	e := f.edge(t, "PRODUCES", chest, "nauvis:item:iron-ore")
	if e.Props["via"] != "infinity" || e.Props["count"] != int64(50) || e.Props["mode"] != "at-least" {
		t.Fatalf("infinity PRODUCES props = %v", e.Props)
	}
	if _, ok := e.Props["perMinute"]; ok {
		t.Fatalf("infinity PRODUCES must not carry perMinute: %v", e.Props)
	}
	if got := f.edge(t, "CONTAINS", chest, "nauvis:item:iron-ore").Props["count"]; got != int64(50) {
		t.Fatalf("infinity CONTAINS count = %v, want 50", got)
	}
}

// removeUnfilteredItems is written on the chest node only when the engine
// reported it, so an absent property means unknown rather than false.
func TestProductionInfinityRemoveUnfiltered(t *testing.T) {
	f := newProdFixture()
	voiding, keeping, unknown := int64(2), int64(3), int64(4)
	voidID := f.node(voiding, "infinity-chest", []string{"Chest"}, nil)
	keepID := f.node(keeping, "infinity-chest", []string{"Chest"}, nil)
	unknownID := f.node(unknown, "infinity-chest", []string{"Chest"}, nil)
	c := f.catalog()
	c.Infinity = luaList[graphCatalogInfinity]{
		{Unit: &voiding, Surface: "nauvis", RemoveUnfiltered: new(true)},
		{Unit: &keeping, Surface: "nauvis", RemoveUnfiltered: new(false)},
		{Unit: &unknown, Surface: "nauvis"},
	}
	f.build(t, c)

	if got := f.b.owned[voidID].Props["removeUnfilteredItems"]; got != true {
		t.Fatalf("voiding chest removeUnfilteredItems = %v, want true", got)
	}
	if got := f.b.owned[keepID].Props["removeUnfilteredItems"]; got != false {
		t.Fatalf("keeping chest removeUnfilteredItems = %v, want false", got)
	}
	if _, ok := f.b.owned[unknownID].Props["removeUnfilteredItems"]; ok {
		t.Fatal("an unreported flag must not be written")
	}
}

// A regular chest keeps one CONTAINS edge per quality, with distinct keys.
func TestProductionChestContents(t *testing.T) {
	f := newProdFixture()
	chest := f.node(11, "wooden-chest", []string{"Chest"}, map[string]any{
		"items": []string{"iron-plate:40", "iron-plate@rare:2"},
	})
	f.build(t, f.catalog())

	edges := f.edgesOf("CONTAINS")
	if len(edges) != 2 {
		t.Fatalf("CONTAINS edges = %d, want 2", len(edges))
	}
	if edges[0].Key == edges[1].Key {
		t.Fatalf("CONTAINS keys collide: %s", edges[0].Key)
	}
	byQuality := map[string]int64{}
	for _, e := range edges {
		if e.From != chest {
			t.Fatalf("CONTAINS source = %s, want %s", e.From, chest)
		}
		q, _ := e.Props["quality"].(string)
		n, _ := e.Props["count"].(int64)
		byQuality[q] = n
		if e.To != "nauvis:item:iron-plate" {
			t.Fatalf("CONTAINS target = %s", e.To)
		}
	}
	if byQuality["normal"] != 40 || byQuality["rare"] != 2 {
		t.Fatalf("CONTAINS counts = %v, want normal 40 rare 2", byQuality)
	}
}

// Machine inventories become HOLDS edges keyed by slot, and fuel also burns.
func TestProductionHeld(t *testing.T) {
	f := newProdFixture()
	furnace := f.node(12, "stone-furnace", []string{"Machine"}, map[string]any{
		"input":  []string{"iron-ore:2"},
		"output": []string{"iron-plate@rare:3"},
		"fuel":   []string{"coal:5"},
	})
	f.build(t, f.catalog())

	if got := len(f.edgesOf("HOLDS")); got != 3 {
		t.Fatalf("HOLDS edges = %d, want 3", got)
	}
	if e := f.edge(t, "HOLDS", furnace, "nauvis:item:iron-ore"); e.Props["slot"] != "input" || e.Props["count"] != int64(2) {
		t.Fatalf("input HOLDS props = %v", e.Props)
	}
	if e := f.edge(t, "HOLDS", furnace, "nauvis:item:iron-plate"); e.Props["slot"] != "output" || e.Props["quality"] != "rare" {
		t.Fatalf("output HOLDS props = %v", e.Props)
	}
	if e := f.edge(t, "BURNS", furnace, "nauvis:item:coal"); e.Props["snapshot"] != "snap-test" {
		t.Fatalf("BURNS props = %v", e.Props)
	}
	if f.edge(t, "HOLDS", furnace, "nauvis:item:coal").Props["slot"] != "fuel" {
		t.Fatalf("coal must also be held in the fuel slot")
	}
}

// One machine can hold the same item in two slots without the edges collapsing.
func TestProductionHeldSameItemTwoSlots(t *testing.T) {
	f := newProdFixture()
	m := f.node(13, "assembling-machine-1", []string{"Machine"}, map[string]any{
		"input":  []string{"iron-plate:1"},
		"output": []string{"iron-plate:1"},
	})
	f.build(t, f.catalog())

	edges := f.edgesOf("HOLDS")
	if len(edges) != 2 || edges[0].Key == edges[1].Key {
		t.Fatalf("HOLDS edges = %d (%v), want 2 distinct keys", len(edges), edges)
	}
	if edges[0].From != m || edges[1].From != m {
		t.Fatalf("HOLDS sources = %s/%s, want %s", edges[0].From, edges[1].From, m)
	}
}

func TestParseGraphItem(t *testing.T) {
	cases := []struct {
		in      string
		name    string
		quality string
		count   int64
		ok      bool
	}{
		{"coal:5", "coal", "normal", 5, true},
		{"iron-plate@rare:12", "iron-plate", "rare", 12, true},
		{"bad", "", "", 0, false},
		{"x:notanumber", "", "", 0, false},
		{":5", "", "", 0, false},
		{"coal:", "", "", 0, false},
	}
	for _, c := range cases {
		name, quality, count, ok := parseGraphItem(c.in)
		if ok != c.ok || name != c.name || quality != c.quality || count != c.count {
			t.Fatalf("parseGraphItem(%q) = (%q,%q,%d,%v), want (%q,%q,%d,%v)",
				c.in, name, quality, count, ok, c.name, c.quality, c.count, c.ok)
		}
	}
}

// The loader accepts the two new labels as node starts and edge endpoints.
func TestProductionLabelsAccepted(t *testing.T) {
	for _, labels := range [][]string{{"Item"}, {"Recipe"}, {"Item", "Thing"}} {
		if _, err := graphNodeQuery(labels); err != nil {
			t.Fatalf("graphNodeQuery(%v): %v", labels, err)
		}
	}
	for _, pair := range [][2]string{{"Recipe", "Item"}, {"Entity", "Item"}, {"Entity", "Recipe"}} {
		if _, err := graphEdgeQuery("PRODUCES", pair[0], pair[1]); err != nil {
			t.Fatalf("graphEdgeQuery(PRODUCES,%s,%s): %v", pair[0], pair[1], err)
		}
	}
	if _, err := graphNodeQuery([]string{"Bogus"}); err == nil {
		t.Fatalf("graphNodeQuery accepted unknown label")
	}
	if _, err := graphEdgeQuery("PRODUCES", "Bogus", "Item"); err == nil {
		t.Fatalf("graphEdgeQuery accepted unknown endpoint label")
	}
	if _, err := graphEdgeQuery("NOT_A_TYPE", "Entity", "Item"); err == nil {
		t.Fatalf("graphEdgeQuery accepted unknown relationship type")
	}
}

// The catalog arrives as JSON from the game, and helpers.table_to_json encodes
// an empty Lua table as {}. A save with no infinity containers or a recipe with
// no ingredients must decode, not fail the export.
func TestGraphCatalogDecode(t *testing.T) {
	payload := `{
	  "recipes": {
	    "iron-plate": {"energy": 3.2, "category": "smelting",
	      "ingredients": {}, 
	      "products": [{"type": "item", "name": "iron-plate", "amount": 1, "probability": 1}]},
	    "uranium-processing": {"energy": 12,
	      "ingredients": [{"type": "item", "name": "uranium-ore", "amount": 10}],
	      "products": [
	        {"type": "item", "name": "uranium-235", "amount": 1, "probability": 0.007, "extraCountFraction": 0},
	        {"type": "item", "name": "uranium-238", "amount": 1, "probability": 0.993}]}
	  },
	  "resources": {"iron-ore": {"miningTime": 1, "products": [{"type": "item", "name": "iron-ore", "amount": 1}]}},
	  "machines": {"stone-furnace": {"craftingSpeed": 1}, "electric-mining-drill": {"miningSpeed": 0.5}},
	  "miningProductivity": 0.3,
	  "infinity": {}
	}`
	var c graphCatalog
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		t.Fatalf("unmarshal empty infinity: %v", err)
	}
	if len(c.Infinity) != 0 {
		t.Fatalf("infinity = %d rows, want 0", len(c.Infinity))
	}
	rec := c.Recipes["iron-plate"]
	if len(rec.Ingredients) != 0 || len(rec.Products) != 1 || rec.Energy != 3.2 {
		t.Fatalf("iron-plate recipe = %+v", rec)
	}
	uranium := c.Recipes["uranium-processing"]
	if len(uranium.Products) != 2 || uranium.Products[0].Probability == nil || *uranium.Products[0].Probability != 0.007 {
		t.Fatalf("uranium products = %+v", uranium.Products)
	}
	if c.Resources["iron-ore"].MiningTime != 1 || len(c.Resources["iron-ore"].Products) != 1 {
		t.Fatalf("iron-ore resource = %+v", c.Resources["iron-ore"])
	}
	if c.Machines["electric-mining-drill"].MiningSpeed == nil || *c.Machines["electric-mining-drill"].MiningSpeed != 0.5 {
		t.Fatalf("drill machine = %+v", c.Machines["electric-mining-drill"])
	}
	if c.MiningProductivity != 0.3 {
		t.Fatalf("miningProductivity = %v", c.MiningProductivity)
	}
}

// A catalog row with no filters and no contents decodes to empty lists.
func TestGraphCatalogDecodeEmptyInfinityRow(t *testing.T) {
	payload := `{"recipes":{},"resources":{},"machines":{},"miningProductivity":0,
	  "infinity":[{"unit":238,"surface":"nauvis","filters":{},"contents":{}},
	              {"unit":2,"surface":"nauvis",
	               "filters":[{"name":"iron-ore","quality":"normal","count":50,"mode":"at-least"}],
	               "contents":[{"name":"iron-ore","quality":"normal","count":50}]}]}`
	var c graphCatalog
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		t.Fatalf("unmarshal infinity rows: %v", err)
	}
	if len(c.Infinity) != 2 {
		t.Fatalf("infinity rows = %d, want 2", len(c.Infinity))
	}
	if len(c.Infinity[0].Filters) != 0 || len(c.Infinity[0].Contents) != 0 {
		t.Fatalf("empty row = %+v", c.Infinity[0])
	}
	row := c.Infinity[1]
	if len(row.Filters) != 1 || row.Filters[0].Count == nil || *row.Filters[0].Count != 50 {
		t.Fatalf("filter row = %+v", row.Filters)
	}
	if len(row.Contents) != 1 || row.Contents[0].Count != 50 || row.Contents[0].Quality != "normal" {
		t.Fatalf("contents row = %+v", row.Contents)
	}
}

// A lab accepts the packs its prototype lists; technologies hang off the packs
// they need, point at exported prerequisites only, and unlock only recipes the
// snapshot already carries. Nothing is written without a lab on the surface.
func TestProductionResearchStructure(t *testing.T) {
	c := func(f *prodFixture) *graphCatalog {
		cat := f.catalog()
		cat.Machines["lab"] = graphCatalogMachine{LabInputs: luaList[string]{"automation-science-pack"}}
		cat.Recipes["iron-plate"] = graphCatalogRecipe{Energy: 3.2, Products: luaList[graphCatalogProduct]{product("item", "iron-plate", 1)}}
		cat.Technologies = luaList[graphCatalogTechnology]{
			{Name: "automation", Count: new(10.0), Energy: new(10.0),
				Ingredients: luaList[graphCatalogProduct]{product("item", "automation-science-pack", 1)},
				Unlocks:     luaList[string]{"iron-plate", "assembling-machine-1"}},
			{Name: "logistics", Prerequisites: luaList[string]{"automation", "hidden-tech"}},
		}
		return cat
	}

	f := newProdFixture()
	lab := f.node(9, "lab", []string{"Machine"}, nil)
	f.node(4, "stone-furnace", []string{"Machine"}, map[string]any{"recipe": "iron-plate"})
	f.build(t, c(f))

	f.edge(t, "ACCEPTS", lab, "nauvis:item:automation-science-pack")
	if e := f.edge(t, "REQUIRES_PACK", "nauvis:technology:automation", "nauvis:item:automation-science-pack"); e.Props["amount"] != 1.0 {
		t.Fatalf("REQUIRES_PACK props = %v", e.Props)
	}
	f.edge(t, "PREREQUISITE", "nauvis:technology:logistics", "nauvis:technology:automation")
	if n := len(f.edgesOf("PREREQUISITE")); n != 1 {
		t.Fatalf("PREREQUISITE edges = %d, want 1 (hidden-tech has no node)", n)
	}
	f.edge(t, "UNLOCKS", "nauvis:technology:automation", "nauvis:recipe:iron-plate")
	if n := len(f.edgesOf("UNLOCKS")); n != 1 {
		t.Fatalf("UNLOCKS edges = %d, want 1 (assembling-machine-1 has no recipe node)", n)
	}
	if n := f.b.derived["nauvis:technology:automation"]; n.Props["count"] != 10.0 || n.Props["energy"] != 10.0 {
		t.Fatalf("technology props = %v", n.Props)
	}

	bare := newProdFixture()
	bare.node(4, "stone-furnace", []string{"Machine"}, nil)
	bare.build(t, c(bare))
	if len(bare.edgesOf("REQUIRES_PACK"))+len(bare.edgesOf("ACCEPTS")) != 0 || bare.b.derived["nauvis:technology:automation"] != nil {
		t.Fatal("technologies exported on a surface without labs")
	}
}
