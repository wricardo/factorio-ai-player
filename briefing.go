package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	briefingMaxAttention  = maxAttentionItems
	briefingMaxWatchItems = maxWatchItems
	briefingAlertRows     = maxBriefingRows
	briefingNetworkLimit  = maxBriefingRows
	briefingGhostLimit    = maxBriefingRows
	briefingStockItems    = maxBriefingRows
)

// briefingSeverity classifies a kind. It reports how bad an observation is, not
// what to do about it.
var briefingSeverity = map[AttentionKind]AttentionSeverity{
	AttentionKindNoFuel:         AttentionSeverityCritical,
	AttentionKindNoPower:        AttentionSeverityCritical,
	AttentionKindNoAmmo:         AttentionSeverityCritical,
	AttentionKindEnemy:          AttentionSeverityCritical,
	AttentionKindLowStock:       AttentionSeverityCritical,
	AttentionKindFluidShortage:  AttentionSeverityWarning,
	AttentionKindNoIngredients:  AttentionSeverityWarning,
	AttentionKindOutputFull:     AttentionSeverityWarning,
	AttentionKindNoRecipe:       AttentionSeverityWarning,
	AttentionKindNoResources:    AttentionSeverityWarning,
	AttentionKindLowTemperature: AttentionSeverityWarning,
	AttentionKindBlocked:        AttentionSeverityWarning,
	AttentionKindDamaged:        AttentionSeverityWarning,
	AttentionKindNoResearch:     AttentionSeverityInfo,
	AttentionKindGhostBacklog:   AttentionSeverityInfo,
	AttentionKindDeconstruction: AttentionSeverityInfo,
	AttentionKindDisabled:       AttentionSeverityInfo,
}

// briefingSuggestedAction is the next step for one kind. It is advice, never an
// automatic change.
var briefingSuggestedAction = map[AttentionKind]string{
	AttentionKindNoFuel:         "feed fuel to the entity",
	AttentionKindNoPower:        "connect the entity to a powered electric network",
	AttentionKindNoAmmo:         "load ammunition into the turret",
	AttentionKindEnemy:          "defend the area or clear the threat",
	AttentionKindLowStock:       "restock the item",
	AttentionKindFluidShortage:  "supply the missing fluid",
	AttentionKindNoIngredients:  "supply the missing ingredients",
	AttentionKindOutputFull:     "empty the output or add a consumer",
	AttentionKindNoRecipe:       "set a recipe or research the recipe",
	AttentionKindNoResources:    "move the drill onto resources or remove it",
	AttentionKindLowTemperature: "raise the heat supply",
	AttentionKindBlocked:        "fix the entity connection, filter or path",
	AttentionKindDamaged:        "repair the entity",
	AttentionKindNoResearch:     "start or queue a research",
	AttentionKindGhostBacklog:   "build or cancel the queued ghosts",
	AttentionKindDeconstruction: "finish or cancel the deconstruction orders",
	AttentionKindDisabled:       "enable the entity or check its control behavior",
}

var briefingSeverityRank = map[AttentionSeverity]int{
	AttentionSeverityCritical: 0,
	AttentionSeverityWarning:  1,
	AttentionSeverityInfo:     2,
}

// briefingKindOrder keeps ties deterministic.
var briefingKindOrder = []AttentionKind{
	AttentionKindEnemy, AttentionKindNoPower, AttentionKindNoFuel, AttentionKindNoAmmo, AttentionKindLowStock,
	AttentionKindNoResources, AttentionKindFluidShortage, AttentionKindNoIngredients, AttentionKindOutputFull, AttentionKindNoRecipe,
	AttentionKindLowTemperature, AttentionKindBlocked, AttentionKindDamaged,
	AttentionKindNoResearch, AttentionKindGhostBacklog, AttentionKindDeconstruction, AttentionKindDisabled,
}

// factoryAlertStatusKinds maps an engine entity status to the attention kind it
// produces. Every defines.entity_status value of Factorio 2.0.77 is either here
// or in factoryAlertHealthyStatuses, which TestFactoryAlertStatusCoverage pins.
var factoryAlertStatusKinds = map[string]AttentionKind{
	"no_power":                          AttentionKindNoPower,
	"low_power":                         AttentionKindNoPower,
	"not_plugged_in_electric_network":   AttentionKindNoPower,
	"recharging_after_power_outage":     AttentionKindNoPower,
	"no_fuel":                           AttentionKindNoFuel,
	"low_temperature":                   AttentionKindLowTemperature,
	"no_ammo":                           AttentionKindNoAmmo,
	"broken":                            AttentionKindDamaged,
	"no_recipe":                         AttentionKindNoRecipe,
	"recipe_not_researched":             AttentionKindNoRecipe,
	"recipe_is_parameter":               AttentionKindNoRecipe,
	"no_ingredients":                    AttentionKindNoIngredients,
	"item_ingredient_shortage":          AttentionKindNoIngredients,
	"missing_science_packs":             AttentionKindNoIngredients,
	"waiting_for_source_items":          AttentionKindNoIngredients,
	"waiting_for_more_items":            AttentionKindNoIngredients,
	"no_input_fluid":                    AttentionKindFluidShortage,
	"fluid_ingredient_shortage":         AttentionKindFluidShortage,
	"missing_required_fluid":            AttentionKindFluidShortage,
	"low_input_fluid":                   AttentionKindFluidShortage,
	"pipeline_overextended":             AttentionKindFluidShortage,
	"full_output":                       AttentionKindOutputFull,
	"full_burnt_result_output":          AttentionKindOutputFull,
	"not_enough_space_in_output":        AttentionKindOutputFull,
	"waiting_for_space_in_destination":  AttentionKindOutputFull,
	"waiting_for_space_in_platform_hub": AttentionKindOutputFull,
	"no_minable_resources":              AttentionKindNoResources,
	"no_spot_seedable_by_inputs":        AttentionKindNoResources,
	"no_path":                           AttentionKindBlocked,
	"destination_stop_full":             AttentionKindBlocked,
	"not_connected_to_rail":             AttentionKindBlocked,
	"cant_divide_segments":              AttentionKindBlocked,
	"not_connected_to_hub_or_pad":       AttentionKindBlocked,
	"no_filter":                         AttentionKindBlocked,
	"no_modules_to_transmit":            AttentionKindBlocked,
	"out_of_logistic_network":           AttentionKindBlocked,
	"not_enough_thrust":                 AttentionKindBlocked,
	"disabled_by_control_behavior":      AttentionKindDisabled,
	"disabled_by_script":                AttentionKindDisabled,
	"disabled":                          AttentionKindDisabled,
	"paused":                            AttentionKindDisabled,
	"turned_off_during_daytime":         AttentionKindDisabled,
	"frozen":                            AttentionKindDisabled,
	"marked_for_deconstruction":         AttentionKindDeconstruction,
	"no_research_in_progress":           AttentionKindNoResearch,
}

// factoryAlertHealthyStatuses are engine statuses that describe normal or
// transient operation and produce no attention row.
var factoryAlertHealthyStatuses = map[string]bool{
	"working":                        true,
	"normal":                         true,
	"ghost":                          true,
	"opened_by_circuit_network":      true,
	"closed_by_circuit_network":      true,
	"networks_connected":             true,
	"networks_disconnected":          true,
	"charging":                       true,
	"discharging":                    true,
	"fully_charged":                  true,
	"waiting_for_target_to_be_built": true,
	"waiting_for_train":              true,
	"waiting_at_stop":                true,
	"preparing_rocket_for_launch":    true,
	"waiting_to_launch_rocket":       true,
	"launching_rocket":               true,
	"thrust_not_required":            true,
	"on_the_way":                     true,
	"waiting_in_orbit":               true,
	"waiting_for_rockets_to_arrive":  true,
	"computing_navigation":           true,
	"waiting_for_plants_to_grow":     true,
}

type briefingCollector struct {
	items  []*AttentionItem
	counts map[AttentionKind]int
	// extra counts observations that were reported by count but not
	// materialized as rows, so attentionTotal stays exact.
	extra int
}

func newBriefingCollector() *briefingCollector {
	return &briefingCollector{counts: map[AttentionKind]int{}}
}

func (c *briefingCollector) add(item *AttentionItem) {
	item.Severity = briefingSeverity[item.Kind]
	c.items = append(c.items, item)
}

// sorted orders by severity, kind, position and name so repeated briefings are
// byte-identical for the same game state.
func (c *briefingCollector) sorted() []*AttentionItem {
	kindRank := map[AttentionKind]int{}
	for i, kind := range briefingKindOrder {
		kindRank[kind] = i
	}
	out := append([]*AttentionItem(nil), c.items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := briefingSeverityRank[a.Severity], briefingSeverityRank[b.Severity]; ra != rb {
			return ra < rb
		}
		if ka, kb := kindRank[a.Kind], kindRank[b.Kind]; ka != kb {
			return ka < kb
		}
		ay, by := math.Inf(1), math.Inf(1)
		ax, bx := math.Inf(1), math.Inf(1)
		if a.Position != nil {
			ax, ay = a.Position.X, a.Position.Y
		}
		if b.Position != nil {
			bx, by = b.Position.X, b.Position.Y
		}
		if ay != by {
			return ay < by
		}
		if ax != bx {
			return ax < bx
		}
		return stringValue(a.Entity) < stringValue(b.Entity)
	})
	return out
}

func (c *briefingCollector) countList() []*AttentionCount {
	out := make([]*AttentionCount, 0, len(c.counts))
	for _, kind := range briefingKindOrder {
		if n := c.counts[kind]; n > 0 {
			out = append(out, &AttentionCount{Kind: kind, Count: n})
		}
	}
	return out
}

// factoryAlertsQuery scans every status-bearing entity of one force in a single
// Lua call and returns only the unhealthy rows plus machine summaries, so a
// status-only consumer never pays for full machine inventories.
func factoryAlertsQuery(r *Resolver, input *FactoryAlertsInput) (*FactoryAlerts, error) {
	if input == nil || input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	area, areaResult := "nil", "nil"
	if input.Area != nil {
		var err error
		area, areaResult, err = worldAreaBoundsLua(input.Area, maxAreaTiles)
		if err != nil {
			return nil, err
		}
	}
	scanKinds := factoryAlertScanKinds()
	known := make(map[string]bool, len(scanKinds))
	supported := make([]string, len(scanKinds))
	for i, kind := range scanKinds {
		known[string(kind)] = true
		supported[i] = string(kind)
	}
	kinds := make([]string, 0, len(input.Kinds))
	for _, kind := range input.Kinds {
		if !known[string(kind)] {
			return nil, fmt.Errorf("unsupported alert kind %q: a factory status scan only produces %s", kind, strings.Join(supported, ", "))
		}
		kinds = append(kinds, luaString(string(kind)))
	}
	lua := luaFail + fmt.Sprintf(`local surface_name,force_name=%s,%s; local area,area_result=%s,%s; local limit=%d; local kind_list={%s}
`, luaString(input.Surface), luaString(input.Force), area, areaResult, input.Limit, strings.Join(kinds, ",")) +
		factoryAlertTablesLua() + factoryAlertsLua
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[FactoryAlerts](response)
}

// factoryAlertTablesLua renders the Go status/kind tables as Lua, so the engine
// scan and the Go attention classification can never disagree.
func factoryAlertTablesLua() string {
	statuses := make([]string, 0, len(factoryAlertStatusKinds))
	for status := range factoryAlertStatusKinds {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	var b strings.Builder
	b.WriteString("local status_kind={")
	for _, status := range statuses {
		fmt.Fprintf(&b, "[%s]=%s,", luaString(status), luaString(string(factoryAlertStatusKinds[status])))
	}
	b.WriteString("}\nlocal kind_info={")
	for rank, kind := range briefingKindOrder {
		fmt.Fprintf(&b, "[%s]={severity=%s,rank=%d,action=%s},", luaString(string(kind)),
			luaString(string(briefingSeverity[kind])), rank, luaString(briefingSuggestedAction[kind]))
	}
	b.WriteString("}\nlocal kind_order={")
	for _, kind := range briefingKindOrder {
		fmt.Fprintf(&b, "%s,", luaString(string(kind)))
	}
	b.WriteString("}\n")
	return b.String()
}

// factoryAlertScanKinds lists the kinds the status scan can actually produce, in
// briefingKindOrder order. ENEMY comes from the safety read and GHOST_BACKLOG
// and LOW_STOCK from construction orders and the stock watch, so factoryAlerts
// rejects them instead of accepting a filter that can never match.
func factoryAlertScanKinds() []AttentionKind {
	reachable := make(map[AttentionKind]bool, len(factoryAlertStatusKinds))
	for _, kind := range factoryAlertStatusKinds {
		reachable[kind] = true
	}
	out := make([]AttentionKind, 0, len(reachable))
	for _, kind := range briefingKindOrder {
		if reachable[kind] {
			out = append(out, kind)
		}
	}
	return out
}

const factoryAlertsLua = `
local surface=game.surfaces[surface_name]
if not surface then fail("surface not found"); return end
local force=game.forces[force_name]
if not force then fail("force not found"); return end
local wanted={}
for _,kind in ipairs(kind_list) do wanted[kind]=true end
local status_names={}
for name,value in pairs(defines.entity_status) do status_names[value]=name end
-- Crafting machine types keep the summary shape shared with factoryMachines.
local machine_types={["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true}
local function bucket(name)
  if name=="working" then return "working" end
  if name=="normal" or name=="no_recipe" or name=="no_research_in_progress" then return "idle" end
  if name=="no_fuel" then return "noFuel" end
  if name=="no_power" or name=="low_power" or name=="not_plugged_in_electric_network" then return "noPower" end
  if name=="no_ingredients" or name=="item_ingredient_shortage" or name=="fluid_ingredient_shortage" or name=="missing_science_packs" or name=="missing_required_fluid" or name=="no_input_fluid" then return "noIngredients" end
  if name=="full_output" or name=="full_burnt_result_output" or name=="waiting_for_space_in_destination" then return "outputFull" end
  return "other"
end
-- Rows are ordered by distance from the area center (or the force spawn when no
-- area is given) so the nearest problem comes first.
local base
if area then
  base={x=(area[1][1]+area[2][1])/2,y=(area[1][2]+area[2][2])/2}
else
  local ok,spawn=pcall(function() return force.get_spawn_position(surface) end)
  base=(ok and spawn) or {x=0,y=0}
end
local function distance(position)
  local dx,dy=position.x-base.x,position.y-base.y
  return math.sqrt(dx*dx+dy*dy)
end
-- A container bar cuts usable slots to bar-1, so a bar-limited chest reports the
-- same status as a full one; the waiting inserter's drop target has to be read
-- to name the cause.
local function inventory_slots(inv)
  local bar=0
  local okb,has=pcall(function() return inv.supports_bar() end)
  if okb and has then bar=inv.get_bar() end
  local usable=#inv
  if bar>0 then usable=math.max(0,bar-1) end
  return bar,usable
end
local function inserter_drop_destination(e)
  if e.type~="inserter" and e.type~="loader" and e.type~="loader-1x1" then return nil end
  local candidates={}
  local ok_target,target=pcall(function() return e.drop_target end)
  if ok_target and target then candidates[#candidates+1]=target end
  local ok,drop=pcall(function() return e.drop_position end)
  if ok and drop then
    for _,candidate in pairs(surface.find_entities_filtered{position=drop,limit=8}) do
      candidates[#candidates+1]=candidate
    end
  end
  for _,candidate in ipairs(candidates) do
    if candidate.valid then
      local ok_inv,inv=pcall(function() return candidate.get_inventory(defines.inventory.chest) end)
      if ok_inv and inv then
        local bar,usable=inventory_slots(inv)
        return {name=candidate.name,type=candidate.type,unitNumber=candidate.unit_number,
          position={x=candidate.position.x,y=candidate.position.y},bar=bar,usableSlots=usable}
      end
    end
  end
  return nil
end
local summaries,summary_by_type={},{}
local alerts,counts={},{}
local total,scanned=0,0
for _,entity in pairs(surface.find_entities_filtered{area=area,force=force}) do
  local ok,status=pcall(function() return entity.status end)
  if ok and type(status)=="number" then
    scanned=scanned+1
    local status_name=status_names[status]
    if machine_types[entity.type] then
      local summary=summary_by_type[entity.type]
      if not summary then
        summary={type=entity.type,total=0,working=0,idle=0,noFuel=0,noPower=0,noIngredients=0,outputFull=0,other=0}
        summary_by_type[entity.type]=summary
        summaries[#summaries+1]=summary
      end
      summary.total=summary.total+1
      local key=bucket(status_name)
      summary[key]=summary[key]+1
    end
    local kind=status_name and status_kind[status_name] or nil
    if kind and (#kind_list==0 or wanted[kind]) then
      total=total+1
      counts[kind]=(counts[kind] or 0)+1
      local okd,direction=pcall(function() return entity.direction end)
      local alert={kind=kind,status=status_name,name=entity.name,type=entity.type,position=entity.position,
        unitNumber=entity.unit_number,direction=(okd and direction) or nil,distance=distance(entity.position)}
      if status_name=="waiting_for_space_in_destination" then
        local destination=inserter_drop_destination(entity)
        if destination then
          alert.destination=destination
          if destination.usableSlots==0 then alert.cause="DESTINATION_BAR_LIMITED" end
        end
      end
      alerts[#alerts+1]=alert
    end
  end
end
table.sort(alerts,function(a,b)
  local ra,rb=kind_info[a.kind].rank or 0,kind_info[b.kind].rank or 0
  if ra~=rb then return ra<rb end
  if a.kind~=b.kind then return a.kind<b.kind end
  if a.distance~=b.distance then return a.distance<b.distance end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  if a.name~=b.name then return a.name<b.name end
  return (a.unitNumber or 0)<(b.unitNumber or 0)
end)
local page={}
for i=1,math.min(#alerts,limit) do
  local alert=alerts[i]
  local info=kind_info[alert.kind]
  page[#page+1]={kind=alert.kind,severity=info.severity,status=alert.status,name=alert.name,type=alert.type,
    position=alert.position,unitNumber=alert.unitNumber,direction=alert.direction,suggestedAction=info.action,
    cause=alert.cause,destination=alert.destination}
end
local count_list={}
for _,kind in ipairs(kind_order) do
  if (counts[kind] or 0)>0 then count_list[#count_list+1]={kind=kind,count=counts[kind]} end
end
table.sort(summaries,function(a,b) return a.type<b.type end)
rcon.print(helpers.table_to_json{tick=game.tick,surface=surface.name,force=force.name,area=area_result,scanned=scanned,total=total,alerts=page,attentionCounts=count_list,machines=summaries})`

// briefingInput validates the request and applies defaults.
func briefingInput(input *FactoryBriefingInput) (*FactoryBriefingInput, error) {
	if input == nil {
		return nil, errors.New("input is required")
	}
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.AttentionLimit < 1 || input.AttentionLimit > briefingMaxAttention {
		return nil, fmt.Errorf("attentionLimit must be between 1 and %d", briefingMaxAttention)
	}
	switch input.Window {
	case ProductionWindowOneMinute, ProductionWindowTenMinutes, ProductionWindowOneHour:
	default:
		return nil, fmt.Errorf("invalid production window %q", input.Window)
	}
	if input.Area != nil {
		if _, _, err := worldAreaBoundsLua(input.Area, maxAreaTiles); err != nil {
			return nil, err
		}
	}
	if len(input.WatchItems) > briefingMaxWatchItems {
		return nil, fmt.Errorf("watchItems must contain at most %d items", briefingMaxWatchItems)
	}
	seen := map[string]bool{}
	for i, watch := range input.WatchItems {
		if watch == nil {
			return nil, fmt.Errorf("watch item %d is missing", i+1)
		}
		if watch.Item == "" {
			return nil, fmt.Errorf("watch item %d must name an item", i+1)
		}
		if seen[watch.Item] {
			return nil, fmt.Errorf("watch item %q is duplicated", watch.Item)
		}
		seen[watch.Item] = true
		if math.IsNaN(watch.Minimum) || math.IsInf(watch.Minimum, 0) || watch.Minimum <= 0 {
			return nil, fmt.Errorf("watch item %q minimum must be finite and greater than 0", watch.Item)
		}
	}
	if input.Actor != nil {
		if _, _, err := settingsActorFrom(input.Actor); err != nil {
			return nil, err
		}
	}
	return input, nil
}

func briefingScanArea(area *FactoryAreaInput) *FactoryArea {
	if area == nil {
		return nil
	}
	return &FactoryArea{LeftTopX: area.LeftTopX, LeftTopY: area.LeftTopY, RightBottomX: area.RightBottomX, RightBottomY: area.RightBottomY}
}

// factoryBriefingQuery composes existing domain reads into one attention
// snapshot. Each read is its own RCON call, so each section carries its own tick.
func factoryBriefingQuery(r *Resolver, input *FactoryBriefingInput) (*FactoryBriefing, error) {
	input, err := briefingInput(input)
	if err != nil {
		return nil, err
	}
	out := &FactoryBriefing{Surface: input.Surface, Force: input.Force, Window: input.Window}
	collector := newBriefingCollector()
	warnings := []string{}

	production, err := factoryProductionQuery(r, &FactoryOverviewInput{Surface: input.Surface, Force: input.Force, Window: input.Window})
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}
	out.ProductionTick = production.Tick
	out.Production = production.Items

	alerts, err := briefingAlerts(r, input, collector, &warnings)
	if err != nil {
		return nil, fmt.Errorf("alerts: %w", err)
	}
	out.MachineTick = alerts.Tick
	out.Machines = alerts.Machines

	power, err := electricNetworksQuery(r, &PowerInsightsInput{Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: briefingNetworkLimit})
	if err != nil {
		return nil, fmt.Errorf("power: %w", err)
	}
	out.PowerTick = power.Tick
	out.Power = &BriefingPower{}
	for _, network := range power.Networks {
		out.Power.Networks++
		out.Power.ProductionWatts += network.ProductionWatts
		out.Power.ConsumptionWatts += network.ConsumptionWatts
		out.Power.CapacityWatts += network.MaxProductionWatts
		out.Power.AccumulatorChargeJoules += network.AccumulatorChargeJoules
		out.Power.AccumulatorCapacityJoules += network.AccumulatorCapacityJoules
		out.Power.LowPowerConsumers += network.LowPowerConsumers
	}
	// An observed-load ratio across the summed networks; 0 means the area exposes
	// no generating capacity, not that demand is zero.
	if out.Power.CapacityWatts > 0 {
		out.Power.Satisfaction = out.Power.ConsumptionWatts / out.Power.CapacityWatts
	}
	if power.PageInfo != nil && power.PageInfo.HasMore {
		out.Power.Networks = power.PageInfo.Total
		warnings = append(warnings, fmt.Sprintf("power sums cover the first %d of %d networks", len(power.Networks), power.PageInfo.Total))
	}

	research, err := researchInsightsQuery(r, &ResearchInsightsInput{Force: input.Force, Status: ResearchInsightsStatusInProgress, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("research: %w", err)
	}
	out.ResearchTick = research.Tick
	out.Research = &BriefingResearch{
		Enabled:           research.ResearchEnabled,
		CurrentTechnology: research.CurrentTechnology,
		Progress:          research.Progress,
		Queue:             research.ResearchQueue,
	}
	if research.ResearchEnabled && research.CurrentTechnology == nil && len(research.ResearchQueue) == 0 {
		collector.counts[AttentionKindNoResearch]++
		collector.add(&AttentionItem{Kind: AttentionKindNoResearch, Count: 1, Message: "no research in progress and none queued"})
	}

	safetyTick, err := briefingSafety(r, input, collector, &warnings)
	if err != nil {
		return nil, fmt.Errorf("safety: %w", err)
	}
	out.SafetyTick = safetyTick

	construction, constructionTick, err := briefingConstruction(r, input, collector, &warnings)
	if err != nil {
		return nil, fmt.Errorf("construction: %w", err)
	}
	out.Construction = construction
	out.ConstructionTick = constructionTick

	stockouts, stockTick, err := briefingStock(r, input, collector)
	if err != nil {
		return nil, fmt.Errorf("stock: %w", err)
	}
	out.Stockouts = stockouts
	out.StockTick = stockTick

	all := collector.sorted()
	out.AttentionTotal = len(all) + collector.extra
	out.AttentionCounts = collector.countList()
	if len(all) > input.AttentionLimit {
		all = all[:input.AttentionLimit]
	}
	out.Attention = all
	out.Warnings = warnings
	return out, nil
}
