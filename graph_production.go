package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// The production graph answers "who makes or uses item X" for every kind of
// producer, not just a crafting machine whose recipe happens to share the item
// name. It adds two derived node labels and the edges between them:
//
//   - :Item, one per surface and item/fluid name;
//   - :Recipe, one per surface and recipe name.
//
// Entities keep their identity (:Entity) and reach items through RUNS,
// PRODUCES, CONSUMES, BURNS, CONTAINS and HOLDS. Item and recipe nodes carry
// scope and snapshot like every other derived node, so the loader's per-scope
// prune removes whatever the next snapshot stops referencing.
//
// Rates are nameplates: they ignore modules, beacons and productivity modules,
// so they show what a machine could do, not what it did.

const (
	itemLabel       = "Item"
	recipeLabel     = "Recipe"
	technologyLabel = "Technology"
)

// luaList decodes a Lua array, treating the empty table encoding "{}" as an
// empty list. helpers.table_to_json cannot tell an empty array from an empty
// map, so every list that may be empty needs this.
type luaList[T any] []T

func (l *luaList[T]) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		*l = nil
		return nil
	}
	var out []T
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return err
	}
	*l = out
	return nil
}

type graphCatalog struct {
	Recipes            map[string]graphCatalogRecipe   `json:"recipes"`
	Resources          map[string]graphCatalogResource `json:"resources"`
	Machines           map[string]graphCatalogMachine  `json:"machines"`
	Technologies       luaList[graphCatalogTechnology] `json:"technologies"`
	MiningProductivity float64                         `json:"miningProductivity"`
	Infinity           luaList[graphCatalogInfinity]   `json:"infinity"`
}

type graphCatalogRecipe struct {
	Energy      float64                      `json:"energy"`
	Category    string                       `json:"category"`
	Ingredients luaList[graphCatalogProduct] `json:"ingredients"`
	Products    luaList[graphCatalogProduct] `json:"products"`
}

// graphCatalogProduct is one recipe ingredient or product, an entity's
// mineable products, or a drilling product. Amount is set for a fixed product;
// AmountMin/AmountMax when the engine rolls a range.
type graphCatalogProduct struct {
	Type               string   `json:"type"`
	Name               string   `json:"name"`
	Amount             *float64 `json:"amount"`
	AmountMin          *float64 `json:"amountMin"`
	AmountMax          *float64 `json:"amountMax"`
	Probability        *float64 `json:"probability"`
	ExtraCountFraction *float64 `json:"extraCountFraction"`
}

type graphCatalogResource struct {
	MiningTime float64                      `json:"miningTime"`
	Products   luaList[graphCatalogProduct] `json:"products"`
}

type graphCatalogMachine struct {
	CraftingSpeed *float64 `json:"craftingSpeed"`
	MiningSpeed   *float64 `json:"miningSpeed"`
	// LabInputs is the lab prototype's lab_inputs: the packs it accepts.
	LabInputs luaList[string] `json:"labInputs"`
}

// graphCatalogTechnology is one technology prototype: its per-unit pack
// requirements, prerequisite technologies and the recipes it unlocks.
type graphCatalogTechnology struct {
	Name          string                       `json:"name"`
	Count         *float64                     `json:"count"`
	Energy        *float64                     `json:"energy"`
	Ingredients   luaList[graphCatalogProduct] `json:"ingredients"`
	Prerequisites luaList[string]              `json:"prerequisites"`
	Unlocks       luaList[string]              `json:"unlocks"`
}

type graphCatalogInfinity struct {
	Unit    *int64                      `json:"unit"`
	Surface string                      `json:"surface"`
	Filters luaList[graphCatalogFilter] `json:"filters"`
	// RemoveUnfiltered is the container's remove_unfiltered_items flag; nil
	// when the engine did not report it.
	RemoveUnfiltered *bool                        `json:"removeUnfiltered"`
	Contents         luaList[graphCatalogContent] `json:"contents"`
}

type graphCatalogFilter struct {
	Name    string `json:"name"`
	Quality string `json:"quality"`
	Count   *int64 `json:"count"`
	Mode    string `json:"mode"`
}

type graphCatalogContent struct {
	Name    string `json:"name"`
	Quality string `json:"quality"`
	Count   int64  `json:"count"`
}

// parseGraphItem splits one Lua content string back into its parts. The mod
// writes "name:count", or "name@quality:count" for a quality other than
// normal (graph_items in topology.lua).
func parseGraphItem(s string) (name, quality string, count int64, ok bool) {
	sep := strings.LastIndex(s, ":")
	if sep <= 0 || sep == len(s)-1 {
		return "", "", 0, false
	}
	n, err := strconv.ParseInt(s[sep+1:], 10, 64)
	if err != nil || n < 0 {
		return "", "", 0, false
	}
	name, quality = s[:sep], "normal"
	if at := strings.LastIndex(name, "@"); at > 0 {
		name, quality = name[:at], name[at+1:]
	}
	if name == "" || quality == "" {
		return "", "", 0, false
	}
	return name, quality, n, true
}

// hasGraphLabel reports whether a node carries a category label.
func hasGraphLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// catalogRequest lists the prototypes the finished entity pass references, so
// the catalog call stays small. Results are sorted for deterministic snapshots.
func (b *graphBuilder) catalogRequest() (recipes, resources, machines []string) {
	recs, res, mach := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range b.owned {
		if r, _ := n.Props["recipe"].(string); r != "" {
			recs[r] = true
		}
		name, _ := n.Props["name"].(string)
		if hasGraphLabel(n.Labels, "Machine") || hasGraphLabel(n.Labels, "MiningDrill") {
			if name != "" {
				mach[name] = true
			}
		}
	}
	for _, e := range b.edges {
		if e.Type != "MINES" {
			continue
		}
		if n := b.owned[e.To]; n != nil {
			if name, _ := n.Props["name"].(string); name != "" {
				res[name] = true
			}
		} else if r, ok := b.refs[e.To]; ok && r.Name != "" {
			res[r.Name] = true
		}
	}
	return sortedKeys(recs), sortedKeys(res), sortedKeys(mach)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// itemOut returns the id of the :Item node for one surface and name, creating
// it on first reference. Quality stays on the edges: one node per name keeps
// "all iron-plate" a single match.
func (b *graphBuilder) itemOut(surface, name, kind string) string {
	id := fmt.Sprintf("%s:%s:%s", surface, strings.ToLower(itemLabel), name)
	if _, ok := b.derived[id]; ok {
		return id
	}
	if kind == "" {
		kind = "item"
	}
	b.derived[id] = &graphNodeOut{ID: id, Labels: []string{itemLabel}, Props: map[string]any{
		"id": id, "name": name, "kind": kind, "surface": surface, "force": b.force,
		"snapshot": b.snapshot, "scope": b.scope(surface),
	}}
	return id
}

func (b *graphBuilder) recipeOut(surface, name, category string, energy float64) string {
	id := fmt.Sprintf("%s:%s:%s", surface, strings.ToLower(recipeLabel), name)
	if _, ok := b.derived[id]; ok {
		return id
	}
	props := map[string]any{
		"id": id, "name": name, "surface": surface, "force": b.force,
		"snapshot": b.snapshot, "scope": b.scope(surface), "energy": energy,
	}
	if category != "" {
		props["category"] = category
	}
	b.derived[id] = &graphNodeOut{ID: id, Labels: []string{recipeLabel}, Props: props}
	return id
}

// productAmount is the expected amount one craft of a product yields.
func productAmount(p graphCatalogProduct) float64 {
	amount := 0.0
	switch {
	case p.Amount != nil:
		amount = *p.Amount
	case p.AmountMin != nil && p.AmountMax != nil:
		amount = (*p.AmountMin + *p.AmountMax) / 2
	case p.AmountMin != nil:
		amount = *p.AmountMin
	case p.AmountMax != nil:
		amount = *p.AmountMax
	}
	if p.Probability != nil {
		amount *= *p.Probability
	}
	if p.ExtraCountFraction != nil {
		amount += *p.ExtraCountFraction
	}
	return amount
}

func roundGraphRate(v float64) float64 {
	return math.Round(v*1000) / 1000
}

func graphItemKind(typ string) string {
	if strings.EqualFold(typ, "fluid") {
		return "fluid"
	}
	return "item"
}

// buildProduction derives the Item/Recipe nodes and every edge that ties an
// entity to the items it makes, uses, burns or stores.
func (b *graphBuilder) buildProduction(c *graphCatalog) error {
	if b.derived == nil {
		b.derived = map[string]*graphNodeOut{}
	}
	byUnit := map[string]string{}
	ids := make([]string, 0, len(b.owned))
	for id, n := range b.owned {
		ids = append(ids, id)
		if unit, ok := n.Props["unit"].(int64); ok {
			if surface, _ := n.Props["surface"].(string); surface != "" {
				byUnit[fmt.Sprintf("%s|%d", surface, unit)] = id
			}
		}
	}
	sort.Strings(ids)

	mines := map[string]string{}
	for _, e := range b.edges {
		if e.Type != "MINES" {
			continue
		}
		if _, ok := mines[e.From]; !ok {
			mines[e.From] = e.To
		}
	}

	for _, id := range ids {
		n := b.owned[id]
		surface, _ := n.Props["surface"].(string)
		name, _ := n.Props["name"].(string)
		if recipe, _ := n.Props["recipe"].(string); recipe != "" {
			if err := b.applyRecipe(n, surface, recipe, name, c); err != nil {
				return err
			}
		}
		if hasGraphLabel(n.Labels, "MiningDrill") {
			if target, ok := mines[id]; ok {
				b.applyMining(id, surface, name, target, c)
			}
		}
		if hasGraphLabel(n.Labels, "Chest") {
			b.applyContents(id, surface)
		}
		if hasGraphLabel(n.Labels, "Machine") || hasGraphLabel(n.Labels, "MiningDrill") {
			b.applyHeld(id, surface)
		}
	}
	for _, row := range c.Infinity {
		b.applyInfinity(row, byUnit)
	}
	b.applyResearch(ids, c)
	return nil
}

// applyResearch links every lab to the packs its prototype accepts and, for each
// surface holding a lab, writes the :Technology nodes the catalog returned with
// their pack requirements, prerequisites and recipe unlocks. Only structure is
// exported: what is being researched and how far along is live force state.
// UNLOCKS points only at :Recipe nodes the snapshot already has, because a
// recipe node carries energy and category that this catalog does not read.
func (b *graphBuilder) applyResearch(ids []string, c *graphCatalog) {
	labSurfaces := map[string]bool{}
	for _, id := range ids {
		n := b.owned[id]
		if typ, _ := n.Props["type"].(string); typ != "lab" {
			continue
		}
		surface, _ := n.Props["surface"].(string)
		name, _ := n.Props["name"].(string)
		labSurfaces[surface] = true
		for _, pack := range c.Machines[name].LabInputs {
			b.edge("ACCEPTS", id, "Entity", b.itemOut(surface, pack, "item"), itemLabel, "", nil, surface)
		}
	}
	for _, surface := range sortedKeys(labSurfaces) {
		for _, t := range c.Technologies {
			b.technologyOut(surface, t)
		}
		for _, t := range c.Technologies {
			techID := b.technologyID(surface, t.Name)
			for _, ing := range t.Ingredients {
				b.edge("REQUIRES_PACK", techID, technologyLabel, b.itemOut(surface, ing.Name, "item"), itemLabel, "",
					map[string]any{"amount": productAmount(ing)}, surface)
			}
			// A prerequisite outside the exported set (hidden, or needing a pack no
			// lab accepts) has no node to point at.
			for _, pre := range t.Prerequisites {
				if preID := b.technologyID(surface, pre); b.derived[preID] != nil {
					b.edge("PREREQUISITE", techID, technologyLabel, preID, technologyLabel, "", nil, surface)
				}
			}
			for _, recipe := range t.Unlocks {
				recipeID := fmt.Sprintf("%s:%s:%s", surface, strings.ToLower(recipeLabel), recipe)
				if _, ok := b.derived[recipeID]; ok {
					b.edge("UNLOCKS", techID, technologyLabel, recipeID, recipeLabel, "", nil, surface)
				}
			}
		}
	}
}

func (b *graphBuilder) technologyID(surface, name string) string {
	return fmt.Sprintf("%s:%s:%s", surface, strings.ToLower(technologyLabel), name)
}

func (b *graphBuilder) technologyOut(surface string, t graphCatalogTechnology) string {
	id := b.technologyID(surface, t.Name)
	props := map[string]any{
		"id": id, "name": t.Name, "surface": surface, "force": b.force,
		"snapshot": b.snapshot, "scope": b.scope(surface),
	}
	if t.Count != nil {
		props["count"] = *t.Count
	}
	if t.Energy != nil {
		props["energy"] = *t.Energy
	}
	b.derived[id] = &graphNodeOut{ID: id, Labels: []string{technologyLabel}, Props: props}
	return id
}

// applyRecipe writes RUNS plus the per-craft graph (recipe → items) and the
// per-minute graph (entity → items) for one crafting machine.
func (b *graphBuilder) applyRecipe(n *graphNodeOut, surface, recipe, machine string, c *graphCatalog) error {
	rec, ok := c.Recipes[recipe]
	if !ok {
		return nil
	}
	recipeID := b.recipeOut(surface, recipe, rec.Category, rec.Energy)
	runProps := map[string]any{}
	if src, _ := n.Props["recipeSource"].(string); src != "" {
		runProps["recipeSource"] = src
	}
	b.edge("RUNS", n.ID, "Entity", recipeID, recipeLabel, "", runProps, surface)

	speed := 0.0
	if m := c.Machines[machine]; m.CraftingSpeed != nil {
		speed = *m.CraftingSpeed
	}
	craftsPerMin := 0.0
	if rec.Energy > 0 {
		craftsPerMin = 60 * speed / rec.Energy
	}

	for _, p := range rec.Products {
		kind := graphItemKind(p.Type)
		itemID := b.itemOut(surface, p.Name, kind)
		props := map[string]any{"amount": productAmount(p), "kind": kind}
		if p.Probability != nil {
			props["probability"] = *p.Probability
		}
		b.edge("PRODUCES", recipeID, recipeLabel, itemID, itemLabel, "", props, surface)
		b.edge("PRODUCES", n.ID, "Entity", itemID, itemLabel, "", map[string]any{
			"via": "recipe", "perMinute": roundGraphRate(craftsPerMin * productAmount(p)), "kind": kind,
		}, surface)
	}
	for _, p := range rec.Ingredients {
		kind := graphItemKind(p.Type)
		itemID := b.itemOut(surface, p.Name, kind)
		b.edge("CONSUMES", recipeID, recipeLabel, itemID, itemLabel, "", map[string]any{
			"amount": productAmount(p), "kind": kind,
		}, surface)
		b.edge("CONSUMES", n.ID, "Entity", itemID, itemLabel, "", map[string]any{
			"via": "recipe", "perMinute": roundGraphRate(craftsPerMin * productAmount(p)), "kind": kind,
		}, surface)
	}
	return nil
}

// applyMining links a drill to what its mining target yields.
func (b *graphBuilder) applyMining(drill, surface, machine, target string, c *graphCatalog) {
	name := ""
	if n := b.owned[target]; n != nil {
		name, _ = n.Props["name"].(string)
	} else if r, ok := b.refs[target]; ok {
		name = r.Name
	}
	res, ok := c.Resources[name]
	if !ok || res.MiningTime <= 0 {
		return
	}
	speed := 0.0
	if m := c.Machines[machine]; m.MiningSpeed != nil {
		speed = *m.MiningSpeed
	}
	productivity := 1 + c.MiningProductivity
	for _, p := range res.Products {
		kind := graphItemKind(p.Type)
		itemID := b.itemOut(surface, p.Name, kind)
		rate := 60 * speed / res.MiningTime * productAmount(p) * productivity
		b.edge("PRODUCES", drill, "Entity", itemID, itemLabel, "", map[string]any{
			"via": "mining", "perMinute": roundGraphRate(rate), "kind": kind,
		}, surface)
	}
}

// applyContents turns the frozen chest contents of one :Chest node into
// CONTAINS edges. Infinity containers are handled from the catalog because the
// leaf payload does not sample their inventory.
func (b *graphBuilder) applyContents(id, surface string) {
	n := b.owned[id]
	items, _ := n.Props["items"].([]string)
	for _, entry := range items {
		name, quality, count, ok := parseGraphItem(entry)
		if !ok {
			continue
		}
		b.edge("CONTAINS", id, "Entity", b.itemOut(surface, name, "item"), itemLabel,
			quality, map[string]any{"count": count, "quality": quality}, surface)
	}
}

// applyHeld links a machine or drill to the items in its input, output and fuel
// inventories, and to what it burns.
func (b *graphBuilder) applyHeld(id, surface string) {
	n := b.owned[id]
	for _, slot := range []string{"input", "output", "fuel"} {
		items, _ := n.Props[slot].([]string)
		for _, entry := range items {
			name, quality, count, ok := parseGraphItem(entry)
			if !ok {
				continue
			}
			itemID := b.itemOut(surface, name, "item")
			b.edge("HOLDS", id, "Entity", itemID, itemLabel, slot+"|"+quality,
				map[string]any{"slot": slot, "count": count, "quality": quality}, surface)
			if slot == "fuel" {
				b.edge("BURNS", id, "Entity", itemID, itemLabel, "", nil, surface)
			}
		}
	}
}

// applyInfinity links an infinity container to the items its filters emit and
// to the items it currently holds.
func (b *graphBuilder) applyInfinity(row graphCatalogInfinity, byUnit map[string]string) {
	if row.Unit == nil {
		return
	}
	id, ok := byUnit[fmt.Sprintf("%s|%d", row.Surface, *row.Unit)]
	if !ok {
		return
	}
	surface := row.Surface
	if row.RemoveUnfiltered != nil {
		if n, ok := b.owned[id]; ok {
			n.Props["removeUnfilteredItems"] = *row.RemoveUnfiltered
		}
	}
	for _, f := range row.Filters {
		if f.Name == "" {
			continue
		}
		props := map[string]any{"via": "infinity", "kind": "item"}
		if f.Count != nil {
			props["count"] = *f.Count
		}
		if f.Mode != "" {
			props["mode"] = f.Mode
		}
		b.edge("PRODUCES", id, "Entity", b.itemOut(surface, f.Name, "item"), itemLabel, "", props, surface)
	}
	for _, c := range row.Contents {
		if c.Name == "" {
			continue
		}
		quality := c.Quality
		if quality == "" {
			quality = "normal"
		}
		b.edge("CONTAINS", id, "Entity", b.itemOut(surface, c.Name, "item"), itemLabel,
			quality, map[string]any{"count": c.Count, "quality": quality}, surface)
	}
}
