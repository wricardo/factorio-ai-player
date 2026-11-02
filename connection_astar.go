package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	routeMaxRegionTiles  = 65536
	routeDefaultExpanded = 10000
	routeMaxExpanded     = 200000
	routeDefaultMargin   = 32
	routeMaxMargin       = 256
	routeMaxAvoidAreas   = 100
	routeMaxTunnelOffset = 32
	routeMaxCoordinate   = 10_000_000
	routeMinTunnelOffset = 2
)

// connectionRouteOptions is the routing part shared by connectionPlan and
// connectEntities.
type connectionRouteOptions struct {
	routing          ConnectionRouting
	allowUnderground bool
	undergroundItem  *string
	maxUnderground   *int
	avoidAreas       []*FactoryAreaInput
	preferExisting   bool
	maxExpanded      *int
	searchMargin     *int
}

func planRouteOptions(input *ConnectionPlanInput) connectionRouteOptions {
	return connectionRouteOptions{
		routing: input.Routing, allowUnderground: input.AllowUnderground, undergroundItem: input.UndergroundItem,
		maxUnderground: input.MaxUndergroundDistance, avoidAreas: input.AvoidAreas, preferExisting: input.PreferExisting,
		maxExpanded: input.MaxExpandedTiles, searchMargin: input.SearchMargin,
	}
}

func applyRouteOptions(input *ConnectEntitiesInput) connectionRouteOptions {
	return connectionRouteOptions{
		routing: input.Routing, allowUnderground: input.AllowUnderground, undergroundItem: input.UndergroundItem,
		maxUnderground: input.MaxUndergroundDistance, avoidAreas: input.AvoidAreas, preferExisting: input.PreferExisting,
		maxExpanded: input.MaxExpandedTiles, searchMargin: input.SearchMargin,
	}
}

// validateRouteOptions rejects routing fields that the chosen routing would
// ignore, so a caller never believes a filter or limit was applied.
func validateRouteOptions(kind ConnectionKind, o connectionRouteOptions) (bool, error) {
	switch o.routing {
	case "", ConnectionRoutingDirect:
		var ignored []string
		if o.allowUnderground {
			ignored = append(ignored, "allowUnderground")
		}
		if o.undergroundItem != nil {
			ignored = append(ignored, "undergroundItem")
		}
		if o.maxUnderground != nil {
			ignored = append(ignored, "maxUndergroundDistance")
		}
		if len(o.avoidAreas) > 0 {
			ignored = append(ignored, "avoidAreas")
		}
		if o.preferExisting {
			ignored = append(ignored, "preferExisting")
		}
		if o.maxExpanded != nil {
			ignored = append(ignored, "maxExpandedTiles")
		}
		if o.searchMargin != nil {
			ignored = append(ignored, "searchMargin")
		}
		if len(ignored) > 0 {
			return false, fmt.Errorf("%s apply to ASTAR routing only", strings.Join(ignored, ", "))
		}
		return false, nil
	case ConnectionRoutingAstar:
	default:
		return false, fmt.Errorf("unsupported connection routing %q", o.routing)
	}
	if kind != ConnectionKindBelt && kind != ConnectionKindPipe {
		return false, errors.New("ASTAR routing supports BELT and PIPE only")
	}
	if !o.allowUnderground && (o.undergroundItem != nil || o.maxUnderground != nil) {
		return false, errors.New("undergroundItem and maxUndergroundDistance require allowUnderground")
	}
	if o.undergroundItem != nil && *o.undergroundItem == "" {
		return false, errors.New("undergroundItem must not be empty")
	}
	if o.maxUnderground != nil && (*o.maxUnderground < routeMinTunnelOffset || *o.maxUnderground > routeMaxTunnelOffset) {
		return false, fmt.Errorf("maxUndergroundDistance must be between %d and %d", routeMinTunnelOffset, routeMaxTunnelOffset)
	}
	if len(o.avoidAreas) > routeMaxAvoidAreas {
		return false, fmt.Errorf("avoidAreas must contain at most %d areas", routeMaxAvoidAreas)
	}
	for i, area := range o.avoidAreas {
		if _, _, err := worldAreaBoundsLua(area, maxAreaTiles); err != nil {
			return false, fmt.Errorf("avoid area %d: %w", i+1, err)
		}
	}
	if o.maxExpanded != nil && (*o.maxExpanded < 1 || *o.maxExpanded > routeMaxExpanded) {
		return false, fmt.Errorf("maxExpandedTiles must be between 1 and %d", routeMaxExpanded)
	}
	if o.searchMargin != nil && (*o.searchMargin < 0 || *o.searchMargin > routeMaxMargin) {
		return false, fmt.Errorf("searchMargin must be between 0 and %d", routeMaxMargin)
	}
	return true, nil
}

func routeUndergroundItem(kind ConnectionKind, item *string) string {
	if item != nil && *item != "" {
		return *item
	}
	if kind == ConnectionKindPipe {
		return "pipe-to-ground"
	}
	return "underground-belt"
}

// routeRegion is the engine state the planner reads for one search region.
type routeRegion struct {
	Tick         float64  `json:"tick"`
	Surface      string   `json:"surface"`
	Force        string   `json:"force"`
	Entity       string   `json:"entity"`
	TunnelEntity string   `json:"tunnelEntity"`
	MaxTunnel    int      `json:"maxTunnel"`
	Open         []string `json:"open"`
	OpenTunnel   []string `json:"openTunnel"`
	Fluid        []string `json:"fluid"`
	Tunnel       []string `json:"tunnel"`
	// Feed marks tiles an existing belt points into (BELT routes only).
	Feed []string `json:"feed"`
	// SpanX and SpanY mark tiles covered by an existing same-name underground
	// span on that axis; empty unless a tunnel item is used.
	SpanX           []string       `json:"spanX"`
	SpanY           []string       `json:"spanY"`
	Existing        []routeExisted `json:"existing"`
	Left, Top       int
	Cols, Rows      int
	RouteSurface    string `json:"-"`
	FromX, FromY    int    `json:"-"`
	ToX, ToY        int    `json:"-"`
	UndergroundNeed string `json:"-"`
}

type routeExisted struct {
	X         int `json:"x"`
	Y         int `json:"y"`
	Direction int `json:"direction"`
}

// routeRegionLua reads one padded bounding box in a single RCON call: placeability
// of the route entity (and the underground entity when tunnels are allowed),
// existing same-name entities, underground entities and pipe-like entities.
func routeRegionLua(surface, force, item, tunnelItem string, pipe bool, left, top, cols, rows int) string {
	kind := "BELT"
	if pipe {
		kind = "PIPE"
	}
	return luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local item=%s; local tunnel_item=%s; local kind=%s
local left,top,cols,rows=%d,%d,%d,%d
local surface=game.surfaces[surface_name]; if not surface then fail("surface not found"); return end
local force=game.forces[force_name]; if not force then fail("force not found"); return end
local item_proto=prototypes.item[item]
if not item_proto or not item_proto.place_result then fail("item does not place an entity"); return end
local want=kind=="PIPE" and "pipe" or "transport-belt"
if item_proto.place_result.type~=want then fail("item places a "..item_proto.place_result.type..", not a "..want); return end
local entity_name=item_proto.place_result.name
local tunnel_name=""; local max_tunnel=0
if tunnel_item~="" then
  local tunnel_proto=prototypes.item[tunnel_item]
  if not tunnel_proto or not tunnel_proto.place_result then fail("undergroundItem does not place an entity"); return end
  local tunnel_want=kind=="PIPE" and "pipe-to-ground" or "underground-belt"
  if tunnel_proto.place_result.type~=tunnel_want then fail("undergroundItem places a "..tunnel_proto.place_result.type..", not a "..tunnel_want); return end
  tunnel_name=tunnel_proto.place_result.name
  local ok,value=pcall(function() return tunnel_proto.place_result.max_underground_distance end)
  if not ok or type(value)~="number" then fail("underground prototype has no max underground distance"); return end
  max_tunnel=value
end
local area={{left,top},{left+cols,top+rows}}
local function grid_rows(name)
  local out={}
  for ty=top,top+rows-1 do
    local parts={}
    for tx=left,left+cols-1 do
      -- build_check_type.manual is the player build rule; build_check_type.script
      -- only reports whether a script may create the box, which overlaps obstacles.
      parts[#parts+1]=surface.can_place_entity{name=name,position={x=tx+0.5,y=ty+0.5},direction=0,force=force,build_check_type=defines.build_check_type.manual} and "1" or "0"
    end
    out[#out+1]=table.concat(parts)
  end
  return out
end
local open_rows=grid_rows(entity_name)
local open_tunnel_rows={}
if tunnel_name~="" then open_tunnel_rows=grid_rows(tunnel_name) end
local function blank()
  local out={}
  for ty=1,rows do out[ty]={} for tx=1,cols do out[ty][tx]="0" end end
  return out
end
local function mark(grid,e)
  local box=e.bounding_box
  local x0=math.floor(box.left_top.x+0.0001); local x1=math.ceil(box.right_bottom.x-0.0001)-1
  local y0=math.floor(box.left_top.y+0.0001); local y1=math.ceil(box.right_bottom.y-0.0001)-1
  for ty=math.max(y0,top),math.min(y1,top+rows-1) do
    for tx=math.max(x0,left),math.min(x1,left+cols-1) do grid[ty-top+1][tx-left+1]="1" end
  end
end
-- Grids that do not apply (feed for PIPE routes, spans without a tunnel item) are
-- empty tables, so joining must not assume rows exist.
local function join(grid)
  local out={}
  if not grid or #grid==0 then return out end
  for ty=1,rows do
    if not grid[ty] then return out end
    out[ty]=table.concat(grid[ty])
  end
  return out
end
local function set_tile(grid,px,py)
  local tx,ty=math.floor(px),math.floor(py)
  if tx<left or ty<top or tx>=left+cols or ty>=top+rows then return end
  grid[ty-top+1][tx-left+1]="1"
end
-- Pipes are pipe-like by type. Machines join a fluid network through a fluidbox
-- pipe connection instead, so their port tiles are marked; a fluidbox with no
-- reported connection yet counts on its own tiles, because placing a pipe on
-- its port creates the link.
local fluid=blank()
if kind=="PIPE" then
  for _,e in pairs(surface.find_entities_filtered{area=area,type=`+pipeLikeTypesLua+`}) do
    if e.valid then mark(fluid,e) end
  end
  local port_types={"assembling-machine","furnace","mining-drill","rocket-silo","boiler","generator","fusion-generator","heat-exchanger","storage-tank","offshore-pump","pump","reactor","fusion-reactor","fluid-wagon","fluid-turret"}
  for _,e in pairs(surface.find_entities_filtered{area=area,type=port_types}) do
    if e.valid then
      local ok_box,box=pcall(function() return e.fluidbox end)
      local ok_count,count=false,0
      if ok_box and box then ok_count,count=pcall(function() return #box end) end
      if ok_box and box and ok_count and type(count)=="number" and count>0 then
        local any=false
        for index=1,count do
          local ok_connections,connections=pcall(function() return box.get_pipe_connections(index) end)
          if ok_connections and connections then
            for _,connection in pairs(connections) do
              any=true
              local target=connection.target_position
              if target then set_tile(fluid,target.x,target.y) end
            end
          end
        end
        if not any then mark(fluid,e) end
      end
    end
  end
end
-- A belt pointing into a tile side-loads its items there, so a new BELT route
-- tile next to one needs the allow flag.
local feed={}
if kind=="BELT" then
  feed=blank()
  local feed_vectors={[0]={0,-1},[4]={1,0},[8]={0,1},[12]={-1,0}}
  for _,e in pairs(surface.find_entities_filtered{area=area,type={"transport-belt","underground-belt","splitter","loader","loader-1x1","linked-belt"}}) do
    if e.valid then
      local feeds=true
      if e.type=="underground-belt" and e.belt_to_ground_type~="output" then feeds=false end
      local vector=feed_vectors[e.direction]
      if feeds and vector then
        local foot=e.bounding_box
        for ty=math.max(top,math.floor(foot.left_top.y)),math.min(top+rows-1,math.ceil(foot.right_bottom.y)-1) do
          for tx=math.max(left,math.floor(foot.left_top.x)),math.min(left+cols-1,math.ceil(foot.right_bottom.x)-1) do
            set_tile(feed,tx+0.5+vector[1],ty+0.5+vector[2])
          end
        end
      end
    end
  end
end
-- Existing same-name underground spans, per axis. A new half inside one would
-- re-pair with the span's other half and break the existing line.
local span_x,span_y={},{}
if tunnel_name~="" then
  span_x,span_y=blank(),blank()
  local pad=max_tunnel
  local span_area={{left-pad,top-pad},{left+cols+pad,top+rows+pad}}
  for _,e in pairs(surface.find_entities_filtered{area=span_area,name=tunnel_name}) do
    if e.valid and e.type=="underground-belt" then
      local ok_type,ground=pcall(function() return e.belt_to_ground_type end)
      if ok_type and ground=="input" then
        local partner=e.neighbours
        if partner and partner.valid then
          local x0,x1=math.min(e.position.x,partner.position.x),math.max(e.position.x,partner.position.x)
          local y0,y1=math.min(e.position.y,partner.position.y),math.max(e.position.y,partner.position.y)
          for ty=math.floor(y0),math.floor(y1) do
            for tx=math.floor(x0),math.floor(x1) do
              if e.direction==0 or e.direction==8 then set_tile(span_y,tx+0.5,ty+0.5) else set_tile(span_x,tx+0.5,ty+0.5) end
            end
          end
        end
      end
    end
  end
end
local tunnel=blank()
for _,e in pairs(surface.find_entities_filtered{area=area,type={"underground-belt","linked-belt","pipe-to-ground"}}) do
  if e.valid then mark(tunnel,e) end
end
local existing={}
for _,e in pairs(surface.find_entities_filtered{area=area,name=entity_name,force=force}) do
  if e.valid then existing[#existing+1]={x=math.floor(e.position.x),y=math.floor(e.position.y),direction=e.direction} end
end
-- Grids that do not apply are omitted instead of encoded as {}, so an empty
-- table never has to be decoded into a string slice.
local payload={tick=game.tick,surface=surface.name,force=force.name,entity=entity_name,tunnelEntity=tunnel_name,maxTunnel=max_tunnel,open=open_rows,openTunnel=open_tunnel_rows,fluid=join(fluid),tunnel=join(tunnel),existing=existing}
if #feed>0 then payload.feed=join(feed) end
if #span_x>0 then payload.spanX=join(span_x) end
if #span_y>0 then payload.spanY=join(span_y) end
rcon.print(helpers.table_to_json(payload))`,
		luaString(surface), luaString(force), luaString(item), luaString(tunnelItem), luaString(kind), left, top, cols, rows)
}

// connectionRequest is the validated input of one ASTAR plan.
type connectionRequest struct {
	surface, force string
	from, to       *PositionInput
	kind           ConnectionKind
	item           string
	allowAdjacency bool
	// allowBeltAdjacency accepts a foreign belt feeding a new BELT tile.
	allowBeltAdjacency bool
	options            connectionRouteOptions
}

func routeTile(p *PositionInput) (int, int) { return int(math.Floor(p.X)), int(math.Floor(p.Y)) }

// buildRouteGrid decodes a region read into the planner's grid.
func buildRouteGrid(region *routeRegion, left, top, cols, rows int, pipe bool, avoid []*FactoryAreaInput) (*routeGrid, error) {
	check := func(name string, got []string, required bool) error {
		if !required && len(got) == 0 {
			return nil
		}
		if len(got) != rows {
			return fmt.Errorf("%s grid has %d rows, want %d", name, len(got), rows)
		}
		for i, row := range got {
			if len(row) != cols {
				return fmt.Errorf("%s grid row %d has %d columns, want %d", name, i+1, len(row), cols)
			}
		}
		return nil
	}
	if err := check("open", region.Open, true); err != nil {
		return nil, err
	}
	if err := check("fluid", region.Fluid, true); err != nil {
		return nil, err
	}
	if err := check("tunnel", region.Tunnel, true); err != nil {
		return nil, err
	}
	if err := check("openTunnel", region.OpenTunnel, false); err != nil {
		return nil, err
	}
	grid := newRouteGrid(left, top, cols, rows)
	fill := func(dst []bool, src []string) {
		for y, row := range src {
			for x := 0; x < cols; x++ {
				dst[y*cols+x] = row[x] == '1'
			}
		}
	}
	fill(grid.open, region.Open)
	fill(grid.fluid, region.Fluid)
	fill(grid.tunnel, region.Tunnel)
	if err := check("feed", region.Feed, false); err != nil {
		return nil, err
	}
	if err := check("spanX", region.SpanX, false); err != nil {
		return nil, err
	}
	if err := check("spanY", region.SpanY, false); err != nil {
		return nil, err
	}
	if len(region.Feed) > 0 {
		grid.feed = make([]bool, cols*rows)
		fill(grid.feed, region.Feed)
	}
	if len(region.SpanX) > 0 {
		grid.spanX = make([]bool, cols*rows)
		fill(grid.spanX, region.SpanX)
	}
	if len(region.SpanY) > 0 {
		grid.spanY = make([]bool, cols*rows)
		fill(grid.spanY, region.SpanY)
	}
	if len(region.OpenTunnel) > 0 {
		grid.openTunnel = make([]bool, cols*rows)
		fill(grid.openTunnel, region.OpenTunnel)
	}
	for _, e := range region.Existing {
		index := grid.index(e.X, e.Y)
		if index < 0 {
			continue
		}
		if pipe {
			grid.existing[index] = routeExistingPipe
		} else {
			grid.existing[index] = (e.Direction / 4) % 4
		}
	}
	for _, area := range avoid {
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				cx, cy := float64(left+x)+0.5, float64(top+y)+0.5
				if cx >= area.LeftTopX && cx <= area.RightBottomX && cy >= area.LeftTopY && cy <= area.RightBottomY {
					grid.avoid[y*cols+x] = true
				}
			}
		}
	}
	return grid, nil
}

// connectionPlanAStar reads the search region once and routes inside it.
func connectionPlanAStar(r *Resolver, req connectionRequest) (*ConnectionPlan, error) {
	pipe := req.kind == ConnectionKindPipe
	options := req.options
	fx, fy := routeTile(req.from)
	tx, ty := routeTile(req.to)
	margin := routeDefaultMargin
	if options.searchMargin != nil {
		margin = *options.searchMargin
	}
	left, top := min(fx, tx)-margin, min(fy, ty)-margin
	cols, rows := max(fx, tx)+margin+1-left, max(fy, ty)+margin+1-top
	if tiles := cols * rows; tiles > routeMaxRegionTiles {
		return nil, fmt.Errorf("search region covers %d tiles, above the %d tile limit; reduce searchMargin or the distance between endpoints", tiles, routeMaxRegionTiles)
	}
	tunnelItem := ""
	if options.allowUnderground {
		tunnelItem = routeUndergroundItem(req.kind, options.undergroundItem)
	}
	response, _, err := executeLua(r.addr, r.password, routeRegionLua(req.surface, req.force, req.item, tunnelItem, pipe, left, top, cols, rows))
	if err != nil {
		return nil, err
	}
	region, err := factoryDecode[routeRegion](response)
	if err != nil {
		return nil, err
	}
	grid, err := buildRouteGrid(region, left, top, cols, rows, pipe, options.avoidAreas)
	if err != nil {
		return nil, err
	}

	maxTunnel := 0
	if options.allowUnderground {
		maxTunnel = region.MaxTunnel
		if options.maxUnderground != nil && *options.maxUnderground < maxTunnel {
			maxTunnel = *options.maxUnderground
		}
		if maxTunnel < routeMinTunnelOffset {
			return nil, fmt.Errorf("underground item %q has max underground distance %d, below the minimum span %d", tunnelItem, region.MaxTunnel, routeMinTunnelOffset)
		}
	}
	maxExpanded := routeDefaultExpanded
	if options.maxExpanded != nil {
		maxExpanded = *options.maxExpanded
	}
	params := routeParams{
		pipe: pipe, fromX: fx, fromY: fy, toX: tx, toY: ty,
		allowTunnel: options.allowUnderground, maxTunnel: maxTunnel,
		preferExisting: options.preferExisting, allowAdjacency: req.allowAdjacency,
		allowBeltAdjacency: req.allowBeltAdjacency, maxExpanded: maxExpanded,
	}
	result := routeAStar(grid, params)

	plan := &ConnectionPlan{
		Tick: region.Tick, Surface: region.Surface, Force: region.Force, Kind: req.kind, Item: req.item,
		From: &Position{X: float64(fx) + 0.5, Y: float64(fy) + 0.5}, To: &Position{X: float64(tx) + 0.5, Y: float64(ty) + 0.5},
		Bend: ConnectionBendAuto, Routing: ConnectionRoutingAstar, StopReason: result.stop,
		Expanded: result.expanded, Steps: []*ConnectionStep{}, Conflicts: []*ConnectionConflict{},
	}
	if tunnelItem != "" {
		plan.UndergroundItem = &tunnelItem
	}
	if result.stop != ConnectionStopReasonFound {
		plan.Conflicts = append(plan.Conflicts, routeFailureConflict(req, grid, params, result, maxExpanded))
		return plan, nil
	}
	for _, step := range result.steps {
		out := &ConnectionStep{
			Position:  &Position{X: float64(step.x) + 0.5, Y: float64(step.y) + 0.5},
			Direction: step.direction, Existing: step.existing, Cost: step.cost,
		}
		switch step.tunnel {
		case "input":
			t := UndergroundBeltTypeInput
			out.UndergroundType = &t
			plan.UndergroundNeeded++
		case "output":
			t := UndergroundBeltTypeOutput
			out.UndergroundType = &t
			plan.UndergroundNeeded++
		default:
			if !step.existing {
				plan.Needed++
			}
		}
		plan.Steps = append(plan.Steps, out)
	}
	plan.Cost = routeCostTotal(result.steps)
	plan.Possible = true
	return plan, nil
}

// routeFailureConflict explains a failed search with one typed conflict.
func routeFailureConflict(req connectionRequest, grid *routeGrid, params routeParams, result routeResult, maxExpanded int) *ConnectionConflict {
	conflict := &ConnectionConflict{Blockers: []string{}}
	switch result.stop {
	case ConnectionStopReasonExpansionLimit:
		conflict.Kind = ConnectionConflictKindExpansionLimit
		conflict.Position = &Position{X: float64(params.fromX) + 0.5, Y: float64(params.fromY) + 0.5}
		conflict.Message = fmt.Sprintf("search stopped after %d expanded states; raise maxExpandedTiles or shorten the route", maxExpanded)
	case ConnectionStopReasonEndpointBlocked:
		conflict.Kind = ConnectionConflictKindCollision
		x, y := params.fromX, params.fromY
		name := "start"
		if routeEndpointOK(grid, params, x, y) {
			x, y, name = params.toX, params.toY, "goal"
		}
		conflict.Position = &Position{X: float64(x) + 0.5, Y: float64(y) + 0.5}
		conflict.Message = fmt.Sprintf("the %s tile cannot hold %s", name, req.item)
	default:
		conflict.Kind = ConnectionConflictKindNoPath
		conflict.Position = &Position{X: float64(params.fromX) + 0.5, Y: float64(params.fromY) + 0.5}
		conflict.Message = "no legal route inside the search region; allow underground spans, raise searchMargin or remove avoidAreas"
	}
	return conflict
}

// routeEndpoints validates the endpoints of an ASTAR plan. Length is bounded by
// the search region instead of the DIRECT tile limit.
func routeEndpoints(from, to *PositionInput) error {
	if from == nil || to == nil {
		return errors.New("from and to are required")
	}
	for _, p := range []*PositionInput{from, to} {
		if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) {
			return errors.New("from and to must be finite")
		}
		if math.Abs(p.X) > routeMaxCoordinate || math.Abs(p.Y) > routeMaxCoordinate {
			return fmt.Errorf("from and to coordinates must be within %d tiles of the origin", routeMaxCoordinate)
		}
	}
	return nil
}

func luaBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// applyRoutePlan places the steps of a feasible ASTAR plan. Planning and placing
// are separate reads, so every tile is re-checked at placement and failures are
// reported per step; inventory is checked first so a shortage places nothing.
func applyRoutePlan(r *Resolver, input ConnectEntitiesInput, plan *ConnectionPlan) (*ConnectEntitiesResult, error) {
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	tunnelItem := ""
	if plan.UndergroundItem != nil {
		tunnelItem = *plan.UndergroundItem
	}
	var steps strings.Builder
	for i, step := range plan.Steps {
		if i > 0 {
			steps.WriteString(",")
		}
		tunnel := ""
		if step.UndergroundType != nil {
			tunnel = strings.ToLower(string(*step.UndergroundType))
		}
		fmt.Fprintf(&steps, "{x=%g,y=%g,direction=%d,existing=%s,tunnel=%q}", step.Position.X, step.Position.Y, step.Direction, luaBool(step.Existing), tunnel)
	}
	lua := luaFail + fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
local inv=actor.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
if c.surface.name~=surface.name then fail("actor is on another surface"); return end
if c.force.name~=%s then fail("actor is on another force"); return end
local item=%s; local tunnel_item=%s
local item_proto=prototypes.item[item]; if not item_proto or not item_proto.place_result then fail("item does not place an entity"); return end
local entity_name=item_proto.place_result.name
local tunnel_name=""
if tunnel_item~="" then
  local tunnel_proto=prototypes.item[tunnel_item]; if not tunnel_proto or not tunnel_proto.place_result then fail("undergroundItem does not place an entity"); return end
  tunnel_name=tunnel_proto.place_result.name
end
local tunnel_type=tunnel_name~="" and prototypes.entity[tunnel_name].type or ""
local steps={%s}
local need_item,need_tunnel=0,0
for _,s in ipairs(steps) do
  if not s.existing then if s.tunnel=="" then need_item=need_item+1 else need_tunnel=need_tunnel+1 end end
end
local have_item=inv.get_item_count{name=item,quality="normal"}
if have_item<need_item then fail("actor has "..have_item.." "..item.." but the run needs "..need_item); return end
if need_tunnel>0 then
  local have_tunnel=inv.get_item_count{name=tunnel_item,quality="normal"}
  if have_tunnel<need_tunnel then fail("actor has "..have_tunnel.." "..tunnel_item.." but the run needs "..need_tunnel); return end
end
local placed,skipped,failed=0,0,0
local entities,errors={},{}
local function fail_at(i,code,message,name,position)
  failed=failed+1
  if #errors<20 then errors[#errors+1]={index=i,code=code,message=message,target={surface=surface.name,entity=name,position=position}} end
end
for i,s in ipairs(steps) do
  if s.existing then
    skipped=skipped+1
  else
    local use_item,name=item,entity_name
    if s.tunnel~="" then use_item,name=tunnel_item,tunnel_name end
    local position={x=s.x,y=s.y}
    local dx=c.position.x-s.x; local dy=c.position.y-s.y
    local ok=true
    if %s then ok=false; fail_at(i,"REACH","out of build reach",name,position) end
    if ok and inv.get_item_count{name=use_item,quality="normal"}<1 then ok=false; fail_at(i,"INVENTORY","actor lacks item",name,position) end
    if ok and inv.remove{name=use_item,quality="normal",count=1}~=1 then ok=false; fail_at(i,"INVENTORY","could not remove item",name,position) end
    if ok then
      local params={name=name,position=position,direction=s.direction,force=c.force,quality="normal",raise_built=true}
      if s.tunnel~="" and tunnel_type=="underground-belt" then params.type=s.tunnel end
      local created,e=pcall(function() return surface.create_entity(params) end)
      if created and e then
        placed=placed+1
        entities[#entities+1]={name=e.name,type=e.type,position={x=e.position.x,y=e.position.y},unitNumber=e.unit_number}
      else
        inv.insert{name=use_item,quality="normal",count=1}
        -- Entity creation is the success test; can_place_entity only explains a refusal.
        if not created then
          fail_at(i,"ENGINE","entity creation failed: "..tostring(e),name,position)
        elseif surface.can_place_entity{name=name,position=position,direction=s.direction,force=c.force,build_check_type=defines.build_check_type.manual} then
          fail_at(i,"ENGINE","entity creation failed",name,position)
        else
          fail_at(i,"COLLISION","cannot place entity at tile",name,position)
        end
      end
    end
  end
end
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=c.force.name,kind=%s,item=item,from={x=%g,y=%g},to={x=%g,y=%g},bend="AUTO",possible=true,placed=placed,skipped=skipped,failed=failed,entities=entities,conflicts={},errors=errors})`,
		luaString(player), character, luaString(plan.Surface), luaString(plan.Force), luaString(plan.Item), luaString(tunnelItem),
		steps.String(), distanceViolation("dx", "dy", "c.build_distance"), luaString(string(plan.Kind)), plan.From.X, plan.From.Y, plan.To.X, plan.To.Y)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ConnectEntitiesResult](response)
	if err != nil {
		return nil, err
	}
	result.Routing = ConnectionRoutingAstar
	result.StopReason = plan.StopReason
	return result, nil
}
