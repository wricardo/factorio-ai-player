package main

// Factorioform prototype-aware validation and normalization. A manifest that
// parsed cleanly (factorioform_manifest.go) is turned into normalized
// ffResource rows using engine prototype data read in ONE RCON call, so the
// diff compares like with like (rotated tile footprints, integer directions,
// per-tile belt rows, live-shaped config values).
//
// Quality semantics: ffResource.Quality is "" when the manifest omits quality
// (unmanaged, like every other omitted attribute); the diff must not treat that
// as a change, and the plan builder places with "normal".

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// ffResource is one normalized desired entity; belt paths expand to one row
// per tile with address "<kind>.<name>[key].path[i]".
type ffResource struct {
	Address   string
	Entity    string
	Type      string
	Position  ffPoint // entity center
	Direction *int    // engine direction 0..15; nil = unmanaged (manifest omitted it)
	Quality   string  // "" = unmanaged
	Footprint ffBox   // tile box after rotation for directions 4/12
	PlaceItem string  // first item_to_place_this
	// Rotatable reports whether setDirection can turn this prototype in place.
	// nil leaves the decision to ffDefaultRotatable (records built without
	// prototype data, as in tests).
	Rotatable *bool
	// Config is keyed by field path ("recipe", "modules", "inserter.filters",
	// "underground_type", ...) with the same value shapes as ffLive.Config:
	// modules are sorted "name:quality:count" strings, filters ordered names
	// ("name@quality" when not normal), nested blocks use GraphQL camelCase maps.
	Config map[string]any
	Range  hcl.Range
}

// ffDesiredWire is one normalized manifest wire. Address is
// "wire:<from>-><to>:<color>" per the epic's diff contract.
type ffDesiredWire struct {
	Address       string
	Color         string
	From          string
	FromConnector string
	To            string
	ToConnector   string
}

// ffInstance is one expanded resource instance: count/for_each already
// evaluated by the dynamic-evaluation layer, Spec decoded with ffDecodeResource.
type ffInstance struct {
	Address string // e.g. assembler.green[0], belt.bus
	Spec    *ffResourceSpec
	Range   hcl.Range
}

// ffNormalized is the output of validation, ordered by address.
type ffNormalized struct {
	Resources []ffResource
	Wires     []ffDesiredWire
}

// ffProtoEntity is the engine prototype data the validator needs.
type ffProtoEntity struct {
	Type                string          `json:"type"`
	TileWidth           float64         `json:"tile_width"`
	TileHeight          float64         `json:"tile_height"`
	CollisionBox        []float64       `json:"collision_box"`
	SupportsDirection   bool            `json:"supports_direction"`
	Rotatable           bool            `json:"rotatable"`
	PlaceItem           string          `json:"place_item"`
	ModuleInventorySize int             `json:"module_inventory_size"`
	CraftingCategories  map[string]bool `json:"crafting_categories"`
	FilterCount         int             `json:"filter_count"`
	// AllowCustomVectors is the inserter prototype flag that lets scripts
	// write pickup/drop positions; without it the engine ignores the write.
	AllowCustomVectors bool `json:"allow_custom_vectors"`
}

// ffPrototypes is the answer of the single batch read: only names that exist.
type ffPrototypes struct {
	Entities  map[string]ffProtoEntity `json:"entities"`
	Recipes   map[string]bool          `json:"recipes"`
	Items     map[string]bool          `json:"items"`
	Qualities map[string]bool          `json:"qualities"`
}

// ffProtoNames are the distinct names to look up.
type ffProtoNames struct {
	Entities, Recipes, Items, Qualities []string
}

// ffStaticInstances turns a parsed manifest without count/for_each into
// instances; resources with count/for_each need the dynamic layer.
func ffStaticInstances(m *ffManifest) ([]ffInstance, hcl.Diagnostics) {
	var out []ffInstance
	var diags hcl.Diagnostics
	for _, d := range m.Resources {
		if d.Spec == nil {
			diags = append(diags, ffErr(d.Range, ffDynamicUnavailable, fmt.Sprintf("resource %s.%s uses count/for_each and must be expanded first", d.Kind, d.Name)))
			continue
		}
		out = append(out, ffInstance{Address: d.Kind + "." + d.Name, Spec: d.Spec, Range: d.Range})
	}
	return out, diags
}

// ffSplitQuality splits "name@quality"; the quality is "normal" when absent.
func ffSplitQuality(s string) (string, string) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, "normal"
}

func ffSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ffCollectNames returns every distinct prototype name the instances mention.
func ffCollectNames(insts []ffInstance) ffProtoNames {
	ent, rec, item, qual := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	addItem := func(s string) {
		name, q := ffSplitQuality(s)
		item[name] = true
		qual[q] = true
	}
	for _, in := range insts {
		s := in.Spec
		if s == nil {
			continue
		}
		if s.Belt != nil {
			ent[s.Belt.Entity] = true
		} else {
			ent[s.Entity] = true
		}
		if s.Recipe != nil {
			rec[*s.Recipe] = true
		}
		if s.Quality != nil {
			qual[*s.Quality] = true
		}
		for _, m := range s.Modules {
			addItem(m)
		}
		if c := s.Configs["inserter"]; c != nil {
			if v, ok := c.Attrs["filters"]; ok && !v.IsNull() && v.IsKnown() {
				if list, d := ffStringList(v, c.AttrRanges["filters"], "filters"); !d.HasErrors() {
					for _, f := range list {
						addItem(f)
					}
				}
			}
		}
		if c := s.Configs["splitter"]; c != nil {
			if v, ok := c.Attrs["filter"]; ok && !v.IsNull() && v.Type() == cty.String && v.AsString() != "" {
				addItem(v.AsString())
			}
		}
	}
	return ffProtoNames{ffSortedKeys(ent), ffSortedKeys(rec), ffSortedKeys(item), ffSortedKeys(qual)}
}

func ffLuaNameSet(names []string) string { return luaStringListOrEmpty(names) }

// ffPrototypeLua renders the one batch prototype read.
func ffPrototypeLua(n ffProtoNames) string {
	return fmt.Sprintf(`local entity_names,recipe_names,item_names,quality_names=%s,%s,%s,%s
local function try(f) local ok,v=pcall(f); if ok then return v end end
local out={entities={},recipes={},items={},qualities={}}
for _,name in ipairs(entity_names) do
  local p=prototypes.entity[name]
  if p then
    local item
    local list=try(function() return p.items_to_place_this end)
    if list and list[1] then item=list[1].name or list[1][1] end
    local cats={}
    local cc=try(function() return p.crafting_categories end)
    if cc then for k in pairs(cc) do cats[k]=true end end
    local box=try(function() return p.collision_box end)
    local cb
    if box then cb={box.left_top.x,box.left_top.y,box.right_bottom.x,box.right_bottom.y} end
    out.entities[name]={type=p.type,tile_width=try(function() return p.tile_width end) or 1,tile_height=try(function() return p.tile_height end) or 1,
      collision_box=cb,supports_direction=try(function() return p.supports_direction end) or false,rotatable=try(function() return p.rotatable end) or false,
      place_item=item,module_inventory_size=try(function() return p.module_inventory_size end) or 0,crafting_categories=cats,
      filter_count=try(function() return p.filter_count end) or 0,
      allow_custom_vectors=try(function() return p.allow_custom_vectors end) or false}
  end
end
for _,name in ipairs(recipe_names) do if prototypes.recipe[name] then out.recipes[name]=true end end
for _,name in ipairs(item_names) do if prototypes.item[name] then out.items[name]=true end end
for _,name in ipairs(quality_names) do if prototypes.quality[name] then out.qualities[name]=true end end
rcon.print(helpers.table_to_json(out))`, ffLuaNameSet(n.Entities), ffLuaNameSet(n.Recipes), ffLuaNameSet(n.Items), ffLuaNameSet(n.Qualities))
}

// ffReadPrototypes performs the single RCON call for all names.
func ffReadPrototypes(ctx context.Context, r *Resolver, n ffProtoNames) (*ffPrototypes, error) {
	response, _, err := executeLuaContext(ctx, r.addr, r.password, ffPrototypeLua(n))
	if err != nil {
		return nil, err
	}
	var p ffPrototypes
	if err := json.Unmarshal([]byte(response), &p); err != nil {
		return nil, fmt.Errorf("decode prototype read: %w", err)
	}
	return &p, nil
}

// ffValidate reads prototypes with exactly one RCON call (none when the
// manifest names nothing) and normalizes. The error is an engine/transport
// failure; manifest problems come back as diagnostics.
func ffValidate(ctx context.Context, r *Resolver, insts []ffInstance, wires []*ffManifestWire) (*ffNormalized, hcl.Diagnostics, error) {
	names := ffCollectNames(insts)
	protos := &ffPrototypes{}
	if len(names.Entities)+len(names.Recipes)+len(names.Items)+len(names.Qualities) > 0 {
		p, err := ffReadPrototypes(ctx, r, names)
		if err != nil {
			return nil, nil, err
		}
		protos = p
	}
	out, diags := ffNormalize(insts, wires, protos)
	return out, diags, nil
}

func ffAddrRange(r hcl.Range) *hcl.Range {
	if r.Filename == "" && r.Start.Byte == 0 && r.End.Byte == 0 {
		return nil
	}
	return &r
}

func ffAddrErr(r hcl.Range, summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{Severity: hcl.DiagError, Summary: summary, Detail: detail, Subject: ffAddrRange(r)}
}

func ffCtyAny(v cty.Value) any {
	if v.IsNull() || !v.IsKnown() {
		return nil
	}
	t := v.Type()
	switch {
	case t == cty.String:
		return v.AsString()
	case t == cty.Bool:
		return v.True()
	case t == cty.Number:
		f, _ := v.AsBigFloat().Float64()
		if f == math.Trunc(f) && math.Abs(f) < 1e15 {
			return int64(f)
		}
		return f
	case t.IsListType() || t.IsTupleType() || t.IsSetType():
		out := []any{}
		for it := v.ElementIterator(); it.Next(); {
			_, e := it.Element()
			out = append(out, ffCtyAny(e))
		}
		return out
	case t.IsObjectType() || t.IsMapType():
		out := map[string]any{}
		for it := v.ElementIterator(); it.Next(); {
			k, e := it.Element()
			out[k.AsString()] = ffCtyAny(e)
		}
		return out
	}
	return nil
}

// ffBlockValue converts a nested config block to its GraphQL camelCase shape.
func ffBlockValue(b *ffConfigBlock, spec *ffBlockSpec) map[string]any {
	out := map[string]any{}
	for _, a := range spec.Attrs {
		if v, ok := b.Attrs[a.Name]; ok {
			out[a.GraphQL] = ffCtyAny(v)
		}
	}
	ffNestedValues(b, spec, out)
	return out
}

func ffNestedValues(b *ffConfigBlock, spec *ffBlockSpec, out map[string]any) {
	for _, ns := range spec.Blocks {
		var list []any
		for _, c := range b.Blocks {
			if c.Type == ns.Name {
				list = append(list, ffBlockValue(c, ns))
			}
		}
		if len(list) == 0 {
			continue
		}
		if ns.Repeat {
			out[ns.GraphQL] = list
		} else {
			out[ns.GraphQL] = list[0]
		}
	}
}

// ffBlockTypes lists the prototype types each config block may configure; a
// block absent from the table (circuit) is accepted on any entity.
var ffBlockTypes = map[string][]string{
	"inserter":            {"inserter"},
	"splitter":            {"splitter", "lane-splitter"},
	"container":           {"container", "logistic-container", "infinity-container"},
	"logistic":            {"logistic-container"},
	"constant_combinator": {"constant-combinator"},
	"combinator":          {"arithmetic-combinator", "decider-combinator"},
}

// ffRecipeTypes are the crafter types whose recipe is settable.
var ffRecipeTypes = map[string]bool{"assembling-machine": true, "rocket-silo": true}

func ffContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func ffTileSize(p ffProtoEntity) (float64, float64) {
	w, h := p.TileWidth, p.TileHeight
	if w < 1 || h < 1 {
		if len(p.CollisionBox) == 4 {
			w = math.Max(1, math.Ceil(p.CollisionBox[2]-p.CollisionBox[0]-1e-4))
			h = math.Max(1, math.Ceil(p.CollisionBox[3]-p.CollisionBox[1]-1e-4))
		} else {
			w, h = math.Max(w, 1), math.Max(h, 1)
		}
	}
	return w, h
}

// ffGridSnap returns the nearest legal center coordinate for a footprint side.
func ffGridSnap(v, size float64) float64 {
	if int(size)%2 == 1 {
		return math.Floor(v) + 0.5
	}
	return math.Round(v)
}

func ffOnGrid(v, size float64) bool {
	return math.Abs(v-ffGridSnap(v, size)) < 1e-9
}

// ffFootprintFor returns the half-open tile box of a prototype centered at
// pos; directions 4 and 12 swap width and height (placeNextTo, luaTileOccupants).
func ffFootprintFor(p ffProtoEntity, pos ffPoint, direction int) (ffBox, float64, float64) {
	w, h := ffTileSize(p)
	if direction == 4 || direction == 12 {
		w, h = h, w
	}
	return ffBox{pos.X - w/2, pos.Y - h/2, pos.X + w/2, pos.Y + h/2}, w, h
}

type ffNormalizer struct {
	protos *ffPrototypes
	diags  hcl.Diagnostics
}

func (n *ffNormalizer) errf(r hcl.Range, summary, format string, a ...any) {
	n.diags = append(n.diags, ffAddrErr(r, summary, fmt.Sprintf(format, a...)))
}

// checkQuality validates a quality name.
func (n *ffNormalizer) checkQuality(r hcl.Range, addr, q string) {
	if !n.protos.Qualities[q] {
		n.errf(r, "Unknown quality", "%s: quality %q does not exist", addr, q)
	}
}

// checkItem validates "name" or "name@quality" and returns the parts.
func (n *ffNormalizer) checkItem(r hcl.Range, addr, what, s string) (string, string) {
	name, q := ffSplitQuality(s)
	if !n.protos.Items[name] {
		n.errf(r, "Unknown item", "%s: %s %q does not exist", addr, what, name)
	}
	n.checkQuality(r, addr, q)
	return name, q
}

// ffNormalize is the pure half of validation: spec + prototypes -> rows.
func ffNormalize(insts []ffInstance, wires []*ffManifestWire, protos *ffPrototypes) (*ffNormalized, hcl.Diagnostics) {
	n := &ffNormalizer{protos: protos}
	var rows []ffResource
	for _, in := range insts {
		if in.Spec == nil {
			n.errf(in.Range, "Missing resource", "%s: resource was not decoded", in.Address)
			continue
		}
		if in.Spec.Belt != nil {
			rows = append(rows, n.belt(in)...)
		} else if row, ok := n.entity(in); ok {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Address < rows[j].Address })

	// Manifest-internal footprint overlaps on the tile grid.
	owner := map[[2]int]string{}
	reported := map[string]bool{}
	for _, row := range rows {
		for x := int(math.Floor(row.Footprint.X1 + 1e-6)); float64(x) < row.Footprint.X2-1e-6; x++ {
			for y := int(math.Floor(row.Footprint.Y1 + 1e-6)); float64(y) < row.Footprint.Y2-1e-6; y++ {
				key := [2]int{x, y}
				other, taken := owner[key]
				if !taken {
					owner[key] = row.Address
					continue
				}
				pair := other + "|" + row.Address
				if !reported[pair] {
					reported[pair] = true
					n.errf(row.Range, "Overlapping footprints", "%s and %s both occupy tile (%d,%d)", other, row.Address, x, y)
				}
			}
		}
	}

	known := map[string]bool{}
	for _, row := range rows {
		known[row.Address] = true
	}
	var dw []ffDesiredWire
	seen := map[string]bool{}
	for _, w := range wires {
		bad := false
		for _, a := range []string{w.From, w.To} {
			if !known[a] {
				n.errf(w.Range, "Unknown wire endpoint", "wire endpoint %s does not resolve to a resource row", a)
				bad = true
			}
		}
		if bad {
			continue
		}
		addr := fmt.Sprintf("wire:%s->%s:%s", w.From, w.To, w.Color)
		if seen[addr] {
			n.errf(w.Range, "Duplicate wire", "wire %s is declared twice", addr)
			continue
		}
		seen[addr] = true
		dw = append(dw, ffDesiredWire{Address: addr, Color: w.Color, From: w.From, FromConnector: w.FromConnector, To: w.To, ToConnector: w.ToConnector})
	}
	sort.Slice(dw, func(i, j int) bool { return dw[i].Address < dw[j].Address })

	if n.diags.HasErrors() {
		return nil, n.diags
	}
	return &ffNormalized{Resources: rows, Wires: dw}, n.diags
}

// place validates entity existence, direction grid and builds the base row.
// direction is the manifest's direction, nil when it omitted the attribute.
func (n *ffNormalizer) place(addr string, r hcl.Range, entity string, pos ffPoint, direction *int) (ffResource, ffProtoEntity, bool) {
	p, ok := n.protos.Entities[entity]
	if !ok {
		n.errf(r, "Unknown entity", "%s: entity prototype %q does not exist", addr, entity)
		return ffResource{}, p, false
	}
	if p.PlaceItem == "" {
		n.errf(r, "Unplaceable entity", "%s: entity %q has no item that places it", addr, entity)
		return ffResource{}, p, false
	}
	dir := 0
	if direction != nil {
		dir = *direction
		if !p.SupportsDirection {
			n.errf(r, "Direction unsupported", "%s: entity %q does not support direction; remove the direction attribute", addr, entity)
			return ffResource{}, p, false
		}
		if dir%4 != 0 && !ffRotatableTypes[p.Type] {
			n.errf(r, "Invalid direction", "%s: %q only supports the four cardinal directions (0, 4, 8, 12); got %d", addr, entity, dir)
			return ffResource{}, p, false
		}
	}
	fp, w, h := ffFootprintFor(p, pos, dir)
	if !ffOnGrid(pos.X, w) || !ffOnGrid(pos.Y, h) {
		n.errf(r, "Off-grid position", "%s: position (%v,%v) is off the grid for the %vx%v footprint of %q at direction %d; use (%v,%v)",
			addr, pos.X, pos.Y, w, h, entity, dir, ffGridSnap(pos.X, w), ffGridSnap(pos.Y, h))
		return ffResource{}, p, false
	}
	// Rotatable records the prototype flag; nil when the prototype did not say,
	// so the diff keeps its type-table fallback.
	var rotatable *bool
	if p.Rotatable {
		rotatable = new(true)
	}
	return ffResource{Address: addr, Entity: entity, Type: p.Type, Position: pos, Direction: direction, Footprint: fp, PlaceItem: p.PlaceItem, Rotatable: rotatable, Config: map[string]any{}, Range: r}, p, true
}

func (n *ffNormalizer) belt(in ffInstance) []ffResource {
	b := in.Spec.Belt
	var rows []ffResource
	for i, t := range b.Tiles {
		addr := fmt.Sprintf("%s.path[%d]", in.Address, i)
		row, p, ok := n.place(addr, in.Range, b.Entity, t.Position, &t.Direction)
		if !ok {
			return nil
		}
		if p.Type != "transport-belt" {
			n.errf(in.Range, "Invalid belt entity", "%s: belt blocks require a transport-belt prototype, %q is %s", in.Address, b.Entity, p.Type)
			return nil
		}
		rows = append(rows, row)
	}
	return rows
}

func (n *ffNormalizer) entity(in ffInstance) (ffResource, bool) {
	s, addr, r := in.Spec, in.Address, in.Range
	errs := len(n.diags)
	row, p, ok := n.place(addr, r, s.Entity, s.Position, s.Direction)
	if !ok {
		return row, false
	}
	if s.Quality != nil {
		n.checkQuality(r, addr, *s.Quality)
		row.Quality = *s.Quality
	}
	if s.Recipe != nil {
		switch {
		case !ffRecipeTypes[p.Type] || len(p.CraftingCategories) == 0:
			n.errf(r, "Recipe on non-crafter", "%s: %q (%s) cannot have a recipe", addr, s.Entity, p.Type)
		case !n.protos.Recipes[*s.Recipe]:
			n.errf(r, "Unknown recipe", "%s: recipe %q does not exist", addr, *s.Recipe)
		default:
			row.Config["recipe"] = *s.Recipe
		}
	}
	if s.Modules != nil {
		if len(s.Modules) > p.ModuleInventorySize {
			n.errf(r, "Too many modules", "%s: %d modules requested but %q has %d module slots", addr, len(s.Modules), s.Entity, p.ModuleInventorySize)
		}
		counts := map[string]int{}
		for _, m := range s.Modules {
			name, q := n.checkItem(r, addr, "module", m)
			counts[name+":"+q]++
		}
		mods := make([]string, 0, len(counts))
		for k, c := range counts {
			mods = append(mods, k+":"+strconv.Itoa(c))
		}
		sort.Strings(mods)
		row.Config["modules"] = mods
	}
	if s.UndergroundType != nil {
		if p.Type != "underground-belt" {
			n.errf(r, "Invalid underground_type", "%s: underground_type applies to underground-belt only, %q is %s", addr, s.Entity, p.Type)
		} else {
			row.Config["underground_type"] = *s.UndergroundType
		}
	}
	for _, spec := range ffConfigSpecs {
		cfg := s.Configs[spec.Name]
		if cfg == nil {
			continue
		}
		if allowed, restricted := ffBlockTypes[spec.Name]; restricted && !ffContains(allowed, p.Type) {
			n.errf(cfg.Range, "Invalid block for entity", "%s: block %q requires a prototype of type %s, %q is %s", addr, spec.Name, strings.Join(allowed, " or "), s.Entity, p.Type)
			continue
		}
		n.config(row.Config, addr, spec, cfg, p)
	}
	return row, len(n.diags) == errs
}

// config flattens one config block into field-path keys.
func (n *ffNormalizer) config(out map[string]any, addr string, spec *ffBlockSpec, cfg *ffConfigBlock, p ffProtoEntity) {
	for _, a := range spec.Attrs {
		v, ok := cfg.Attrs[a.Name]
		if !ok {
			continue
		}
		path := spec.Name + "." + a.Name
		rng := cfg.AttrRanges[a.Name]
		switch path {
		case "inserter.filters":
			list, d := ffStringList(v, rng, "filters")
			if d.HasErrors() {
				n.diags = append(n.diags, d...)
				continue
			}
			if p.FilterCount < 1 {
				n.errf(rng, "Filters unsupported", "%s: %q has no filter slots", addr, p.Type)
				continue
			}
			if len(list) > p.FilterCount {
				n.errf(rng, "Too many filters", "%s: %d filters but this %s has %d filter slots", addr, len(list), p.Type, p.FilterCount)
			}
			norm := make([]string, len(list))
			for i, f := range list {
				if f != "" {
					name, q := n.checkItem(rng, addr, "filter", f)
					f = name
					if q != "normal" {
						f = name + "@" + q
					}
				}
				norm[i] = f
			}
			out[path] = norm
		case "inserter.pickup_offset", "inserter.drop_offset":
			pt, _, d := ffPointOf(v, rng, a.Name)
			if d.HasErrors() {
				n.diags = append(n.diags, d...)
				continue
			}
			if !p.AllowCustomVectors {
				n.errf(rng, "Custom vectors unsupported", "%s: this inserter prototype has allow_custom_vectors=false, so %s cannot be applied (the engine ignores the write); remove it or use a prototype that allows custom vectors", addr, a.Name)
				continue
			}
			out[path] = map[string]any{"x": pt.X, "y": pt.Y}
		case "splitter.filter":
			if v.IsNull() {
				out[path] = ""
				continue
			}
			if v.Type() != cty.String {
				n.errf(rng, "Invalid filter", "%s: splitter.filter must be an item name string, got %s", addr, v.Type().FriendlyName())
				continue
			}
			if v.AsString() != "" {
				name, q := n.checkItem(rng, addr, "filter", v.AsString())
				if q != "normal" {
					name += "@" + q
				}
				out[path] = name
			} else {
				out[path] = ""
			}
		case "container.inventory":
			// A qualifier of container.allowed_slots, never a field of its own: the
			// live read reports one bar per inventory, so the key carries the name.
			if _, ok := cfg.Attrs["allowed_slots"]; !ok {
				n.errf(rng, "Inert container setting", "%s: container.inventory only selects the inventory that allowed_slots limits; add allowed_slots or remove inventory", addr)
			}
		case "container.allowed_slots":
			inv := "chest"
			if iv, ok := cfg.Attrs["inventory"]; ok {
				irng := cfg.AttrRanges["inventory"]
				s, d := ffString(iv, irng, "inventory")
				if d.HasErrors() {
					n.diags = append(n.diags, d...)
					continue
				}
				if _, known := settingsInventoryNames[s]; !known {
					n.errf(irng, "Unknown inventory", "%s: inventory %q does not exist", addr, s)
					continue
				}
				inv = s
			}
			out["container."+inv+".bar"] = ffCtyAny(v)
		default:
			out[path] = ffCtyAny(v)
		}
	}
	nested := map[string]any{}
	ffNestedValues(cfg, spec, nested)
	for k, v := range nested {
		out[spec.Name+"."+k] = v
	}
}
