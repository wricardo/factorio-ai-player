package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Factorioform live snapshot: one coherent read of every entity the diff
// compares. The Lua composes the graph exporter's node builder (quality,
// direction, recipe, modules, filters, splitter priorities, inserter settings,
// red/green wire edges with connector names) with settings_sections (container
// bar, logistic sections, circuit behaviour, constant combinator) and
// entity_meta_fields (combinator parameters). Neither prelude is edited, so no
// topologyModProtocol bump is needed; the chunk always runs inline.

// ffPoint is a world position in tiles.
type ffPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ffBox is a tile-aligned half-open world box [X1,X2) x [Y1,Y2).
type ffBox struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// Overlaps reports whether two half-open boxes share area.
func (b ffBox) Overlaps(o ffBox) bool {
	return b.X1 < o.X2 && o.X1 < b.X2 && b.Y1 < o.Y2 && o.Y1 < b.Y2
}

// ffWire is one circuit wire seen from the owning entity.
type ffWire struct {
	Color         string `json:"color"`
	Connector     string `json:"connector"`
	PeerUnit      int64  `json:"peerUnit"`
	PeerConnector string `json:"peerConnector"`
}

// ffLive is one live entity (or ghost, tree, rock) of the snapshot. Config keys
// use the manifest field paths: recipe, recipe_quality, modules (sorted
// "name:quality:count"), inserter.filters (ordered slot names, "" for an empty
// slot, "name@quality" for a non-normal quality), inserter.filter_mode,
// inserter.stack_size_override, inserter.pickup_from_left_lane,
// inserter.pickup_from_right_lane, inserter.pickup_offset and
// inserter.drop_offset ({x,y} relative to the entity centre, map axes), splitter.input_priority,
// splitter.output_priority, splitter.filter, container.bar (usable slots),
// circuit.<snake_case CircuitBehaviorInput field>, logistic.sections,
// constant_combinator.sections, combinator.arithmetic, combinator.decider and
// underground_type. Complex values keep the GraphQL camelCase shape of the
// matching write input.
type ffLive struct {
	UnitNumber        int64          `json:"unitNumber"`
	Entity            string         `json:"entity"`
	Type              string         `json:"type"`
	Position          ffPoint        `json:"position"`
	Direction         int            `json:"direction"`
	Quality           string         `json:"quality"`
	GhostName         string         `json:"ghostName,omitempty"`
	GhostType         string         `json:"ghostType,omitempty"`
	ToBeDeconstructed bool           `json:"toBeDeconstructed"`
	ToBeUpgraded      bool           `json:"toBeUpgraded"`
	Footprint         ffBox          `json:"footprint"`
	Config            map[string]any `json:"config"`
	Wires             []ffWire       `json:"wires,omitempty"`
}

// ffLiveTile reports one command of the read: the area it covered (nil for a
// pure unit lookup) and the tick it observed. Reads are not one instant.
type ffLiveTile struct {
	Area  *[4]float64 `json:"area,omitempty"`
	Units int         `json:"units"`
	Tick  float64     `json:"tick"`
}

// ffLiveSnapshot is the result of ffReadLive. Tick is the latest tile tick.
type ffLiveSnapshot struct {
	Tick     float64
	Tiles    []ffLiveTile
	Entities []*ffLive
	// Absent lists requested state unit numbers that no longer resolve.
	Absent []int64
}

// ffLiveQuery selects what to read.
type ffLiveQuery struct {
	Surface string
	Force   string
	// Area is the half-open rectangle [X1,X2)x[Y1,Y2) to scan; the caller adds
	// the 2 tile margin. Empty (zero-size) skips the scan.
	Area ffBox
	// StateUnits are unit numbers recorded in state, resolved wherever they are.
	StateUnits []int64
	// Concurrency caps simultaneous commands; 0 means rconConcurrency.
	Concurrency int
}

const (
	// ffLiveTileSide bounds one scan command so a dense tile stays well inside
	// the fixed 10s RCON deadline; the engine rejects nothing below 1024.
	ffLiveTileSide = 64
	// ffLiveMaxSide is the largest area side ffReadLive accepts in total.
	ffLiveMaxSide = 1024 * 64
	// ffLiveUnitBatch bounds the state unit numbers resolved by one command.
	ffLiveUnitBatch = 400
)

// ffLiveCommand is one generated RCON command with the work it covers.
type ffLiveCommand struct {
	Area  *[4]float64
	Units []int64
	Lua   string
}

// ffLiveCommands splits the query into tile scans (ffLiveTileSide square,
// row-major) followed by unit-lookup batches.
func ffLiveCommands(q ffLiveQuery) ([]ffLiveCommand, error) {
	surface, force := q.Surface, q.Force
	if surface == "" {
		surface = defaultSurface
	}
	if force == "" {
		force = "player"
	}
	for _, v := range []float64{q.Area.X1, q.Area.Y1, q.Area.X2, q.Area.Y2} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("live area must be finite")
		}
	}
	var cmds []ffLiveCommand
	w, h := q.Area.X2-q.Area.X1, q.Area.Y2-q.Area.Y1
	if w > 0 && h > 0 {
		if w > ffLiveMaxSide || h > ffLiveMaxSide {
			return nil, fmt.Errorf("live area %gx%g exceeds %d tiles per side", w, h, ffLiveMaxSide)
		}
		for y := q.Area.Y1; y < q.Area.Y2; y += ffLiveTileSide {
			for x := q.Area.X1; x < q.Area.X2; x += ffLiveTileSide {
				a := [4]float64{x, y, math.Min(x+ffLiveTileSide, q.Area.X2), math.Min(y+ffLiveTileSide, q.Area.Y2)}
				cmds = append(cmds, ffLiveCommand{Area: &a, Lua: ffLiveLua(surface, force, &a, nil)})
			}
		}
	}
	for i := 0; i < len(q.StateUnits); i += ffLiveUnitBatch {
		end := min(i+ffLiveUnitBatch, len(q.StateUnits))
		units := q.StateUnits[i:end]
		cmds = append(cmds, ffLiveCommand{Units: units, Lua: ffLiveLua(surface, force, nil, units)})
	}
	return cmds, nil
}

// ffLiveBodyLua reads one area and/or a unit list. It expects surface_name,
// force_name, area ({lx,ly,rx,ry} or nil) and units as locals. Every entity
// whose bounding box touches the area is returned; the caller de-duplicates
// entities that touch two tiles.
const ffLiveBodyLua = `
graph_reset()
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local excluded={}
for _,t in ipairs(excluded_types) do excluded[t]=true end
local function ff_box(e)
  local ok,bb=pcall(function() return e.bounding_box end)
  if ok and bb then return {bb.left_top.x,bb.left_top.y,bb.right_bottom.x,bb.right_bottom.y} end
  return nil
end
local function ff_flag(e,name)
  local ok,v=pcall(function() return e[name]() end)
  return ok and v==true
end
local ff_out,ff_seen={},{}
local function ff_record(e,belt_index)
  local id=graph_id(e)
  if ff_seen[id] then return end
  ff_seen[id]=true
  if e.force.name~=force_name then
    ff_out[#ff_out+1]={neutral=true,name=e.name,type=e.type,x=e.position.x,y=e.position.y,box=ff_box(e)}
    return
  end
  local rec={node=graph_node(e,belt_index,false),box=ff_box(e),deconstruct=ff_flag(e,"to_be_deconstructed"),upgrade=ff_flag(e,"to_be_upgraded")}
  if e.type=="underground-belt" or (e.type=="entity-ghost" and e.ghost_type=="underground-belt") then
    local ok,t=pcall(function() return e.belt_to_ground_type end)
    if ok and type(t)=="string" then rec.undergroundType=t end
  end
  if e.type~="entity-ghost" and not is_belt(e) then
    local ok,s=pcall(settings_sections,e,"chest")
    if ok and type(s)=="table" then
      rec.settings={container=s.container,circuit=s.circuit,logistic=s.logistic,constantCombinator=s.constantCombinator}
    end
  end
  if combinator_types[e.type] then
    local ok,m=pcall(entity_meta_fields,e)
    if ok and type(m)=="table" then rec.combinator=m.combinator_parameters end
  end
  ff_out[#ff_out+1]=rec
end
local ff_scanned=0
if area then
  local box={{area[1],area[2]},{area[3],area[4]}}
  local found=surface.find_entities_filtered{area=box,force=force}
  local wild=surface.find_entities_filtered{area=box,force="neutral",type=graph_known({"tree","simple-entity","cliff"})}
  for _,e in ipairs(wild) do found[#found+1]=e end
  table.sort(found,less)
  ff_scanned=#found
  local belt_index=belt_tile_index(found)
  for _,e in ipairs(found) do
    if valid(e) and not excluded[e.type] then ff_record(e,belt_index) end
  end
end
local absent={}
for _,u in ipairs(units) do
  -- Only characters and vehicles resolve directly; everything else needs the
  -- surface scan (one type-filtered scan per command, cached).
  local e=game.get_entity_by_unit_number(u)
  if not valid(e) then e=find_entity_by_unit_number(surface,u,"") end
  if valid(e) and e.surface==surface then ff_record(e,nil) else absent[#absent+1]=u end
end
local wires={}
for _,edge in ipairs(graph_edges) do
  if edge.kind=="red" or edge.kind=="green" then wires[#wires+1]=edge end
end
local payload={tick=game.tick,scanned=ff_scanned,entities=ff_out,wires=wires,absent=absent}`

// ffLiveLua renders one command. Neither graphPreludeLua nor topology.lua is
// edited; the chunk is always sent inline.
func ffLiveLua(surface, force string, area *[4]float64, units []int64) string {
	areaLua := "nil"
	if area != nil {
		areaLua = fmt.Sprintf("{%s,%s,%s,%s}", graphNumber(area[0]), graphNumber(area[1]), graphNumber(area[2]), graphNumber(area[3]))
	}
	unitParts := make([]string, len(units))
	for i, u := range units {
		unitParts[i] = strconv.FormatInt(u, 10)
	}
	locals := fmt.Sprintf("local surface_name,force_name,area,units,excluded_types=%s,%s,%s,{%s},%s\n",
		luaString(surface), luaString(force), areaLua, strings.Join(unitParts, ","), luaStringListOrEmpty(graphExcludedTypes()))
	return luaFail + transportLineLua + topologyCommonLua + "\n" + graphPreludeLua + "\n" + settingsLua + "\n" + entityMetaFieldsLua + "\n" +
		luaUnitNumberEntityTypes + locals + ffLiveBodyLua + "\nrcon.print(helpers.table_to_json(payload))"
}

type ffLiveRawRecord struct {
	Neutral         bool            `json:"neutral"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	X               float64         `json:"x"`
	Y               float64         `json:"y"`
	Node            *graphRawNode   `json:"node"`
	Box             []float64       `json:"box"`
	Deconstruct     bool            `json:"deconstruct"`
	Upgrade         bool            `json:"upgrade"`
	UndergroundType string          `json:"undergroundType"`
	Settings        *ffLiveSettings `json:"settings"`
	Combinator      map[string]any  `json:"combinator"`
}

type ffLiveSettings struct {
	Container *struct {
		Bar          int  `json:"bar"`
		Capacity     int  `json:"capacity"`
		AllowedSlots *int `json:"allowedSlots"`
	} `json:"container"`
	Circuit            map[string]any `json:"circuit"`
	Logistic           map[string]any `json:"logistic"`
	ConstantCombinator map[string]any `json:"constantCombinator"`
}

type ffLiveRawPayload struct {
	Error    string            `json:"error"`
	Tick     float64           `json:"tick"`
	Scanned  int               `json:"scanned"`
	Entities []ffLiveRawRecord `json:"entities"`
	Wires    []graphRawEdge    `json:"wires"`
	Absent   []int64           `json:"absent"`
}

// Lua serialises an empty table as an object, so list fields tolerate {}.
func (p *ffLiveRawPayload) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if s := strings.TrimSpace(string(v)); s == "{}" {
			raw[k] = json.RawMessage("[]")
		}
	}
	type plain ffLiveRawPayload
	fixed, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(fixed, (*plain)(p))
}

var ffCamelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

func ffSnake(s string) string { return strings.ToLower(ffCamelRe.ReplaceAllString(s, "${1}_${2}")) }

// ffFootprint converts an engine bounding box (inset inside its tiles) to the
// tile box the entity occupies.
func ffFootprint(box []float64) ffBox {
	if len(box) != 4 {
		return ffBox{}
	}
	const eps = 1e-4
	return ffBox{math.Floor(box[0] + eps), math.Floor(box[1] + eps), math.Ceil(box[2] - eps), math.Ceil(box[3] - eps)}
}

func ffUnitFromID(id string) (int64, bool) {
	i := strings.LastIndex(id, ":")
	if i < 0 {
		return 0, false
	}
	u, err := strconv.ParseInt(id[i+1:], 10, 64)
	return u, err == nil
}

// ffDecodeRecord maps one raw record to the epic's ffLive.
func ffDecodeRecord(rec ffLiveRawRecord) *ffLive {
	live := &ffLive{Config: map[string]any{}, Footprint: ffFootprint(rec.Box)}
	if rec.Neutral || rec.Node == nil {
		live.Entity, live.Type = rec.Name, rec.Type
		live.Position = ffPoint{rec.X, rec.Y}
		return live
	}
	n := rec.Node
	live.Entity, live.Type = n.Name, n.Type
	live.Position = ffPoint{n.X, n.Y}
	if n.Unit != nil {
		live.UnitNumber = *n.Unit
	}
	if n.Direction != nil {
		live.Direction = *n.Direction
	}
	live.Quality = n.Quality
	live.GhostName, live.GhostType = n.GhostName, n.GhostType
	live.ToBeDeconstructed, live.ToBeUpgraded = rec.Deconstruct, rec.Upgrade
	cfg := live.Config
	recipe := n.Recipe
	if recipe == "" {
		recipe = n.GhostRecipe
	}
	if recipe != "" && n.RecipeSource == "" {
		cfg["recipe"] = recipe
		if n.RecipeQuality != "" {
			cfg["recipe_quality"] = n.RecipeQuality
		}
	}
	if len(n.Modules) > 0 {
		mods := append([]string{}, n.Modules...)
		sort.Strings(mods)
		cfg["modules"] = mods
	}
	if n.Type == "inserter" || n.GhostType == "inserter" {
		if n.UseFilters != nil {
			cfg["inserter.filters"] = ffFilterSlots(n.Filters)
			if n.FilterMode != "" {
				cfg["inserter.filter_mode"] = n.FilterMode
			}
		}
		if n.StackSizeOverr != nil {
			cfg["inserter.stack_size_override"] = *n.StackSizeOverr
		}
		if n.PickupX != nil && n.PickupY != nil {
			cfg["inserter.pickup_offset"] = map[string]any{"x": *n.PickupX - n.X, "y": *n.PickupY - n.Y}
		}
		if n.DropX != nil && n.DropY != nil {
			cfg["inserter.drop_offset"] = map[string]any{"x": *n.DropX - n.X, "y": *n.DropY - n.Y}
		}
		if n.PickupLeftLane != nil {
			cfg["inserter.pickup_from_left_lane"] = *n.PickupLeftLane
		}
		if n.PickupRightLane != nil {
			cfg["inserter.pickup_from_right_lane"] = *n.PickupRightLane
		}
	}
	if n.Type == "splitter" || n.Type == "lane-splitter" {
		cfg["splitter.input_priority"] = ffOrNone(n.InputPriority)
		cfg["splitter.output_priority"] = ffOrNone(n.OutputPriority)
		filter := ""
		if len(n.Filters) > 0 {
			filter = ffFilterName(n.Filters[0])
		}
		cfg["splitter.filter"] = filter
	}
	if rec.UndergroundType != "" {
		cfg["underground_type"] = rec.UndergroundType
	}
	if s := rec.Settings; s != nil {
		if c := s.Container; c != nil {
			bar := c.Capacity
			if c.AllowedSlots != nil {
				bar = *c.AllowedSlots
			}
			cfg["container.bar"] = bar
		}
		for k, v := range s.Circuit {
			if v != nil && k != "enabled" {
				cfg["circuit."+ffSnake(k)] = v
			}
		}
		if s.Logistic != nil {
			if sec, ok := s.Logistic["sections"]; ok {
				cfg["logistic.sections"] = sec
			}
		}
		if s.ConstantCombinator != nil {
			if sec, ok := s.ConstantCombinator["sections"]; ok {
				cfg["constant_combinator.sections"] = sec
			}
		}
	}
	for k, v := range rec.Combinator {
		cfg["combinator."+k] = v
	}
	return live
}

func ffOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func ffFilterName(f graphFilter) string {
	if f.Quality != "" && f.Quality != "normal" {
		return f.Name + "@" + f.Quality
	}
	return f.Name
}

// ffFilterSlots renders filter slots as an ordered list indexed from slot 1;
// gaps are "" and trailing empty slots are dropped.
func ffFilterSlots(filters []graphFilter) []string {
	maxIndex := 0
	for _, f := range filters {
		maxIndex = max(maxIndex, f.Index)
	}
	out := make([]string, maxIndex)
	for _, f := range filters {
		if f.Index >= 1 {
			out[f.Index-1] = ffFilterName(f)
		}
	}
	return out
}

func ffLiveKey(l *ffLive) string {
	if l.UnitNumber != 0 {
		return "u" + strconv.FormatInt(l.UnitNumber, 10)
	}
	return fmt.Sprintf("%s|%s|%g|%g", l.Type, l.Entity, l.Position.X, l.Position.Y)
}

// ffReadLive runs the query: tiles and unit batches under the RCON
// concurrency cap, merging records that touch two tiles. Wires are attached to
// the entity that owns them (both ends report their own side).
func ffReadLive(ctx context.Context, r *Resolver, q ffLiveQuery) (*ffLiveSnapshot, error) {
	cmds, err := ffLiveCommands(q)
	if err != nil {
		return nil, err
	}
	limit := q.Concurrency
	if limit < 1 || limit > rconConcurrency {
		limit = rconConcurrency
	}
	payloads := make([]*ffLiveRawPayload, len(cmds))
	errs := make([]error, len(cmds))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range cmds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			response, _, err := executeLuaContext(ctx, r.addr, r.password, cmds[i].Lua)
			if err != nil {
				errs[i] = err
				return
			}
			var p ffLiveRawPayload
			if err := json.Unmarshal([]byte(response), &p); err != nil {
				errs[i] = fmt.Errorf("decode live snapshot: %w", err)
				return
			}
			if p.Error != "" {
				errs[i] = errors.New(p.Error)
				return
			}
			payloads[i] = &p
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	snap := &ffLiveSnapshot{}
	index := map[string]*ffLive{}
	byUnit := map[int64]*ffLive{}
	var wires []graphRawEdge
	for i, p := range payloads {
		snap.Tiles = append(snap.Tiles, ffLiveTile{Area: cmds[i].Area, Units: len(cmds[i].Units), Tick: p.Tick})
		snap.Tick = math.Max(snap.Tick, p.Tick)
		for _, rec := range p.Entities {
			live := ffDecodeRecord(rec)
			k := ffLiveKey(live)
			if _, dup := index[k]; dup {
				continue
			}
			index[k] = live
			snap.Entities = append(snap.Entities, live)
			if live.UnitNumber != 0 {
				byUnit[live.UnitNumber] = live
			}
		}
		wires = append(wires, p.Wires...)
		snap.Absent = append(snap.Absent, p.Absent...)
	}
	seenWire := map[ffWire]map[int64]bool{}
	for _, w := range wires {
		from, okFrom := ffUnitFromID(w.From)
		to, okTo := ffUnitFromID(w.To)
		owner := byUnit[from]
		if !okFrom || !okTo || owner == nil {
			continue
		}
		wire := ffWire{Color: w.Kind, Connector: w.FromConnector, PeerUnit: to, PeerConnector: w.ToConnector}
		if seenWire[wire] == nil {
			seenWire[wire] = map[int64]bool{}
		}
		if seenWire[wire][from] {
			continue
		}
		seenWire[wire][from] = true
		owner.Wires = append(owner.Wires, wire)
	}
	sort.Slice(snap.Entities, func(i, j int) bool {
		a, b := snap.Entities[i], snap.Entities[j]
		if a.Position.X != b.Position.X {
			return a.Position.X < b.Position.X
		}
		if a.Position.Y != b.Position.Y {
			return a.Position.Y < b.Position.Y
		}
		return a.UnitNumber < b.UnitNumber
	})
	for _, l := range snap.Entities {
		sort.Slice(l.Wires, func(i, j int) bool {
			a, b := l.Wires[i], l.Wires[j]
			if a.Color != b.Color {
				return a.Color < b.Color
			}
			if a.Connector != b.Connector {
				return a.Connector < b.Connector
			}
			return a.PeerUnit < b.PeerUnit
		})
	}
	sort.Slice(snap.Absent, func(i, j int) bool { return snap.Absent[i] < snap.Absent[j] })
	return snap, nil
}
