package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// luaInspectSnapshot serializes one entity into the EntityInspection JSON shape.
// inspect and inspectMany share it, so both report the same fields; callers
// must provide entityMetaFieldsLua (entity_meta_fields) and luaInventoryHelpers
// (inventories).
//
// Every type-specific block is guarded by the entity type, so a missing
// transportLines or heldStack means the entity cannot carry one instead of a
// failed read. heldStack carries its quality because InventoryStack.quality is
// non-null, and a client copying the stack into transfer or beltTake would
// otherwise pass an empty quality name.
const luaInspectSnapshot = `local function inspect_snapshot(e,s)
local result={name=e.name,type=e.type,position=e.position,surface=s.name,force=e.force and e.force.name or nil,health=e.health,max_health=e.max_health,inventories={}}
for name,inv in pairs(inventories(e)) do result.inventories[name]=inv.get_contents() end
for name,value in pairs(entity_meta_fields(e)) do result[name]=value end
local ok_direction,direction=pcall(function() return e.direction end); if ok_direction and direction then result.direction=direction end
if e.status then
  result.status=e.status
  for name,value in pairs(defines.entity_status) do if value==e.status then result.status_name=name; break end end
end
if e.type=="furnace" or e.type=="assembling-machine" or e.type=="rocket-silo" then
  local recipe=e.get_recipe(); if recipe then result.recipe=recipe.name end
  result.crafting_progress=e.crafting_progress
end
if e.type=="transport-belt" then
  result.transport_lines={e.get_transport_line(1).get_contents(),e.get_transport_line(2).get_contents()}
end
local ok_marked,marked=pcall(function() return e.to_be_deconstructed() end); result.to_be_deconstructed=ok_marked and marked or false
if e.type=="inserter" then
  result.pickup_position=e.pickup_position
  result.drop_position=e.drop_position
  if e.held_stack.valid_for_read then
    result.held_stack={name=e.held_stack.name,count=e.held_stack.count,quality=e.held_stack.quality and e.held_stack.quality.name or "normal"}
  end
end
if e.type=="mining-drill" then
  result.mining_progress=e.mining_progress
  local target=e.mining_target
  if target then result.mining_target={name=target.name,position=target.position,amount=target.amount} end
  local resources={}
  for _,resource in pairs(s.find_entities_filtered{area=e.mining_area,type="resource"}) do
    resources[#resources+1]={name=resource.name,position=resource.position,amount=resource.amount}
  end
  result.mining_resources=resources
end
local stats=e.force.get_item_production_statistics(s)
if stats then result.force_item_production={produced=stats.input_counts,consumed=stats.output_counts} end
local fluids={}
for name,amount in pairs(e.get_fluid_contents()) do fluids[#fluids+1]={name=name,amount=amount} end
table.sort(fluids,function(a,b) return a.name<b.name end)
result.fluids=fluids
return result
end
`

// inspectManyLimit bounds one batch: resolving more targets than this in a
// single Lua chunk risks the 10 s RCON deadline on a large save.
const inspectManyLimit = maxBatchTargets

// inspectManyTarget is one resolved (or pre-rejected) batch target.
type inspectManyTarget struct {
	Index   int
	Error   string
	Unit    uint64
	Name    string
	X, Y    float64
	Surface string
}

// inspectManyTargets validates every target independently so one bad address
// fails its own row instead of the whole page.
func inspectManyTargets(input InspectManyInput) ([]inspectManyTarget, error) {
	if len(input.Targets) < 1 || len(input.Targets) > inspectManyLimit {
		return nil, fmt.Errorf("targets must contain 1 to %d entity references", inspectManyLimit)
	}
	surface := "nauvis"
	if input.Surface != nil {
		surface = *input.Surface
	}
	if surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	targets := make([]inspectManyTarget, 0, len(input.Targets))
	for i, target := range input.Targets {
		entry := inspectManyTarget{Index: i, Surface: surface}
		if target == nil {
			entry.Error = "target must not be null"
			targets = append(targets, entry)
			continue
		}
		if target.Surface != nil {
			if *target.Surface == "" {
				entry.Error = "surface must not be empty"
				targets = append(targets, entry)
				continue
			}
			entry.Surface = *target.Surface
		}
		name, unitNumber := "", ""
		if target.Entity != nil {
			name = *target.Entity
		}
		if target.UnitNumber != nil {
			unitNumber = *target.UnitNumber
		}
		x, y := entityRefCoordinates(target)
		var unit uint64
		var err error
		if unitNumber != "" {
			// A unitNumber stays the address; coordinates only narrow the
			// lookup to one tile instead of a surface scan, so accepting both
			// here costs nothing and saves a full scan per target.
			unit, err = entityTarget(name, unitNumber, math.NaN(), math.NaN())
		} else {
			unit, err = entityTarget(name, unitNumber, x, y)
		}
		if err != nil {
			entry.Error = err.Error()
			targets = append(targets, entry)
			continue
		}
		entry.Unit, entry.Name, entry.X, entry.Y = unit, name, x, y
		targets = append(targets, entry)
	}
	return targets, nil
}

// inspectManyTargetsLua emits the target list consumed by the batch chunk.
func inspectManyTargetsLua(targets []inspectManyTarget) string {
	var b strings.Builder
	b.WriteString("local targets={\n")
	for _, target := range targets {
		if target.Error != "" {
			fmt.Fprintf(&b, "  {index=%d,error=%s},\n", target.Index, luaString(target.Error))
			continue
		}
		hasPosition := "false"
		if !math.IsNaN(target.X) && !math.IsNaN(target.Y) {
			hasPosition = "true"
		}
		fmt.Fprintf(&b, "  {index=%d,unit=%d,name=%s,position={x=%s,y=%s},surface=%s,has_position=%s},\n",
			target.Index, target.Unit, luaString(target.Name), luaCoordinate(target.X), luaCoordinate(target.Y), luaString(target.Surface), hasPosition)
	}
	b.WriteString("}\n")
	return b.String()
}

// inspectManyLua inspects every target in one Lua chunk. Targets resolve
// without the actor-force restriction, so resources, trees, item stacks and
// enemy entities resolve too, and each target is attempted once. includeSettings
// and includeDiagnostics embed the shared settings and machine diagnostics
// sections of the same chunk instead of costing another call each.
func inspectManyLua(targets []inspectManyTarget, includeSettings, includeDiagnostics bool) string {
	diagnostics := ""
	if includeDiagnostics {
		diagnostics = machineDiagnosticsSnapshotLua
	}
	var b strings.Builder
	b.WriteString(luaFail)
	b.WriteString(entityMetaFieldsLua)
	b.WriteString(luaInventoryHelpers)
	if includeSettings {
		b.WriteString(settingsLua)
	}
	b.WriteString(luaInspectSnapshot)
	if diagnostics != "" {
		b.WriteString(diagnostics)
	}
	b.WriteString(luaEntityResolve)
	b.WriteString(inspectManyTargetsLua(targets))
	b.WriteString(`local settings_inventory="chest"
local diagnostics_types={["assembling-machine"]=true,furnace=true,["mining-drill"]=true,lab=true,["rocket-silo"]=true}
local results={}
for i=1,#targets do
  local t=targets[i]
  if t.error then
    results[#results+1]={index=t.index,error=t.error}
  else
    local ok,e,err=pcall(locate_entity,t.unit,t.name,t.position,t.surface,p,false,t.has_position)
    if not ok then err=tostring(e); e=nil end
    if not e then
      results[#results+1]={index=t.index,error=err or "entity not found"}
    else
      local row={index=t.index,tick=game.tick,name=e.name,type=e.type,position=e.position,unit_number=e.unit_number}
      local ok_snapshot,snapshot=pcall(inspect_snapshot,e,e.surface)
      if ok_snapshot and snapshot then row.inspection=snapshot else row.error=tostring(snapshot) end
`)
	if includeSettings {
		b.WriteString(`      if row.inspection then
        local ok_settings,settings=pcall(settings_sections,e,settings_inventory)
        if ok_settings and settings then row.settings=settings end
      end
`)
	}
	if diagnostics != "" {
		b.WriteString(`      if row.inspection and diagnostics_types[e.type] then
        local ok_diagnostics,diagnostics=pcall(machine_diagnostics_snapshot,e)
        if ok_diagnostics and diagnostics then row.diagnostics=diagnostics end
      end
`)
	}
	b.WriteString(`      results[#results+1]=row
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=result_surface,force=p.force.name,results=results}))
`)
	return b.String()
}

// inspectManyActorLua binds p (the actor) and result_surface, accepting exactly
// one of a player name or a character unit number.
func inspectManyActorLua(actor *ActorInput, surface string) (string, error) {
	character, player, err := settingsActorFrom(actor)
	if err != nil {
		return "", err
	}
	if character != 0 {
		return fmt.Sprintf(`local p=game.get_entity_by_unit_number(%d); if not p or p.type~="character" then fail("character not found"); return end
local result_surface=%s; if not game.surfaces[result_surface] then fail("surface not found"); return end
`, character, luaString(surface)), nil
	}
	return fmt.Sprintf(`local p=game.players[%s]; if not p then fail("player not found"); return end
local result_surface=%s; if not game.surfaces[result_surface] then fail("surface not found"); return end
`, luaString(player), luaString(surface)), nil
}

// batchInspectionRow mirrors one row of the inspectMany Lua response. The
// diagnostics section is the raw engine snapshot; calculateMachineDiagnostics
// turns it into the MachineDiagnostics the schema exposes.
type batchInspectionRow struct {
	Index       int                        `json:"index"`
	Name        *string                    `json:"name"`
	Type        *string                    `json:"type"`
	Position    *Position                  `json:"position"`
	UnitNumber  *string                    `json:"unitNumber"`
	Inspection  *EntityInspection          `json:"inspection"`
	Settings    *SettingsResult            `json:"settings"`
	Diagnostics *machineDiagnosticSnapshot `json:"diagnostics"`
	Error       *string                    `json:"error"`
}

type batchInspectionResponse struct {
	Tick    float64               `json:"tick"`
	Surface string                `json:"surface"`
	Force   string                `json:"force"`
	Results []*batchInspectionRow `json:"results"`
}

// inspectManyQuery runs one backend call for every target instead of one call
// per target, which is what forced Entity.inspection resolvers cost.
func inspectManyQuery(r *Resolver, input InspectManyInput) (*BatchInspectionPage, error) {
	targets, err := inspectManyTargets(input)
	if err != nil {
		return nil, err
	}
	surface := "nauvis"
	if input.Surface != nil {
		surface = *input.Surface
	}
	actorLua, err := inspectManyActorLua(input.Actor, surface)
	if err != nil {
		return nil, err
	}
	includeSettings := input.IncludeSettings != nil && *input.IncludeSettings
	includeDiagnostics := input.IncludeDiagnostics != nil && *input.IncludeDiagnostics
	body := inspectManyLua(targets, includeSettings, includeDiagnostics)
	response, _, err := executeLua(r.addr, r.password, actorLua+body)
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[batchInspectionResponse](response)
	if err != nil {
		return nil, err
	}
	if decoded.Surface == "" {
		decoded.Surface = surface
	}
	results := make([]*BatchInspection, 0, len(decoded.Results))
	for _, row := range decoded.Results {
		entry := &BatchInspection{
			Index:      row.Index,
			Name:       row.Name,
			Type:       row.Type,
			Position:   row.Position,
			UnitNumber: row.UnitNumber,
			Inspection: row.Inspection,
			Settings:   row.Settings,
			Error:      row.Error,
		}
		if row.Diagnostics != nil {
			entry.Diagnostics = calculateMachineDiagnostics(row.Diagnostics)
		}
		if entry.Inspection != nil {
			if entry.Inspection.MiningTarget != nil {
				targetSurface := decoded.Surface
				entry.Inspection.MiningTarget.Surface = &targetSurface
			}
			for _, entity := range entry.Inspection.MiningResources {
				targetSurface := decoded.Surface
				entity.Surface = &targetSurface
			}
		}
		results = append(results, entry)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Index < results[j].Index })
	return &BatchInspectionPage{Tick: decoded.Tick, Surface: decoded.Surface, Force: decoded.Force, Results: results}, nil
}
