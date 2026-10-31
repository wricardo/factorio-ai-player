package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// This file implements the direct agent-action mutations: setting the game
// speed and pause state, teleporting the acting character, using a capsule item
// from a player character's inventory at a target position, and repairing a
// damaged entity with repair packs. Every mutation validates its inputs in Go,
// runs one Lua script, decodes the engine snapshot, and announces a summary.

// validateGameSpeedInput accepts a finite speed multiplier in the engine's
// supported range. A nil paused leaves the current pause state untouched.
func validateGameSpeedInput(input GameSpeedInput) error {
	if math.IsNaN(input.Speed) || math.IsInf(input.Speed, 0) {
		return errors.New("speed must be finite")
	}
	if input.Speed < 0.1 || input.Speed > 1000 {
		return errors.New("speed must be between 0.1 and 1000")
	}
	return nil
}

func validateAgentCoordinates(what string, x, y float64) error {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) {
		return fmt.Errorf("%s x and y must be finite", what)
	}
	return nil
}

func validateTeleportInput(input TeleportInput) error {
	if _, _, err := settingsActorFrom(input.Actor); err != nil {
		return err
	}
	if input.Position == nil {
		return errors.New("position is required")
	}
	if input.Surface != nil && *input.Surface == "" {
		return errors.New("surface must not be empty")
	}
	return validateAgentCoordinates("position", input.Position.X, input.Position.Y)
}

// validateCapsuleInput covers player-only actor selection, item, count, and target.
func validateCapsuleInput(input CapsuleInput) error {
	if input.Actor == nil || input.Actor.Player == nil || strings.TrimSpace(*input.Actor.Player) == "" || input.Actor.Character != nil {
		return errors.New("capsule actor must select one player")
	}
	if input.Item == "" {
		return errors.New("item must not be empty")
	}
	if input.Count < 1 || input.Count > 100 {
		return errors.New("count must be between 1 and 100")
	}
	if input.Target == nil {
		return errors.New("target is required")
	}
	return validateAgentCoordinates("target", input.Target.X, input.Target.Y)
}

// validateRepairInput covers the shared actor addressing plus the action bound.
// The entity addressing is validated by settingsTargetFrom.
func validateRepairInput(input RepairInput) error {
	if _, _, err := settingsActorFrom(input.Actor); err != nil {
		return err
	}
	if input.Count < 1 || input.Count > 100 {
		return errors.New("count must be between 1 and 100")
	}
	_, err := settingsTargetFromRef(input.Actor, input.Target)
	return err
}

func setGameSpeedLua(input GameSpeedInput) string {
	var b strings.Builder
	b.WriteString("game.speed=" + formatFloat(input.Speed) + "\n")
	if input.Paused != nil {
		b.WriteString(fmt.Sprintf("game.tick_paused=%t\n", *input.Paused))
	}
	b.WriteString(`rcon.print(helpers.table_to_json({tick=game.tick,speed=game.speed,paused=game.tick_paused}))`)
	return b.String()
}

// setGameSpeed sets the simulation speed and, when paused is given, the pause
// state. Both are sandbox/administrative knobs that affect the whole server.
func setGameSpeed(r *Resolver, input GameSpeedInput) (*GameSpeedResult, error) {
	if err := validateGameSpeedInput(input); err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, luaFail+setGameSpeedLua(input))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[GameSpeedResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("game speed set to %s (paused %t)", formatFloat(result.Speed), result.Paused))
	return result, nil
}

func teleportLua(character uint64, player, surface string, x, y float64, snap bool) string {
	return settingsActorPrefix(character, player) + fmt.Sprintf(`local s=game.surfaces[%s]; if not s then fail("surface not found"); return end
local position={x=%s,y=%s}
local ok,result=pcall(function() return c.teleport(position, s, false, %t, defines.build_check_type.script_ghost) end)
if not ok then fail("teleport failed: "..tostring(result)); return end
if result==false then fail("teleport was rejected by the engine"); return end
local out={tick=game.tick,surface=c.surface.name,force=c.force.name,position={x=c.position.x,y=c.position.y}}
if %d~=0 then out.character=%d else out.player=p.name end
rcon.print(helpers.table_to_json(out))`,
		luaString(surface), luaCoordinate(x), luaCoordinate(y), snap, character, character)
}

// teleportActor moves the acting character to finite coordinates on an existing
// surface. It bypasses travel; reach checks still apply to later mutations.
func teleportActor(r *Resolver, input TeleportInput) (*ActionResult, error) {
	if err := validateTeleportInput(input); err != nil {
		return nil, err
	}
	character, player, err := settingsActorFrom(input.Actor)
	if err != nil {
		return nil, err
	}
	surface := stringValueOr(input.Surface, "nauvis")
	snap := input.SnapToGrid != nil && *input.SnapToGrid
	response, _, err := executeLua(r.addr, r.password, luaFail+teleportLua(character, player, surface, input.Position.X, input.Position.Y, snap))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ActionResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, "teleported to "+positionText(result.Position)+" on "+result.Surface)
	return result, nil
}

func useCapsuleLua(input CapsuleInput) string {
	return settingsActorPrefix(0, *input.Actor.Player) + fmt.Sprintf(`if not p.connected then fail("player must be connected"); return end
local s=c.surface
local position={x=%s,y=%s}
local item=%s
local requested=%d
local proto=prototypes.item[item]; if not proto then fail("item prototype not found"); return end
local dx=c.position.x-position.x; local dy=c.position.y-position.y
if %s then fail("capsule target is out of reach"); return end
local inv=c.get_main_inventory(); if not inv then fail("character has no main inventory"); return end
local stack=inv.find_item_stack(item); if not stack then fail("character has no "..item.." in the main inventory"); return end
local created={}
local used=0
for i=1,requested do
  if not stack.valid_for_read then break end
  local before=stack.count
  local ok,result=pcall(function() return stack.use_capsule(c,{x=position.x,y=position.y}) end)
  if not ok then fail("capsule use failed: "..tostring(result)); return end
  local after=0; if stack.valid_for_read then after=stack.count end
  if result==nil and after>=before then fail("capsule action had no effect at that position"); return end
  used=used+1
  if result then created[#created+1]={name=result.name,type=result.type,position=result.position,unit_number=result.unit_number} end
end
if used==0 then fail("capsule was not used"); return end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=c.force.name,item=item,used=used,position=position,created=created}))`,
		luaCoordinate(input.Target.X), luaCoordinate(input.Target.Y), luaString(input.Item), input.Count,
		distanceViolation("dx", "dy", "c.reach_distance"))
}

// useCapsule uses a capsule item from a player character's main inventory at a
// target position. Detached characters cannot use capsules, so only a player
// actor is accepted. The target must be within the character's reach distance.
func useCapsule(r *Resolver, input CapsuleInput) (*CapsuleResult, error) {
	if err := validateCapsuleInput(input); err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, luaFail+useCapsuleLua(input))
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[CapsuleResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("used %d %s on %s%s", result.Used, result.Item, result.Surface, positionText(result.Position)))
	return result, nil
}

// repairLua consumes repair packs and raises the entity health directly. This
// is a scripted repair: the engine's asynchronous repair_state action cannot run
// inside one RCON call, so the health delta is applied here.
func repairLua(target settingsTarget, count int) (string, error) {
	prefix, err := target.lua()
	if err != nil {
		return "", err
	}
	return prefix + fmt.Sprintf(`local health_ok,health=pcall(function() return e.health end)
if not health_ok or type(health)~="number" then fail("entity has no health"); return end
if health<=0 then fail("entity is destroyed"); return end
local max_ok,max_health=pcall(function() return e.max_health end)
if not max_ok or type(max_health)~="number" or max_health<=0 then fail("entity has no maximum health"); return end
if health>=max_health then fail("entity is not damaged"); return end
local durability=300
local durability_ok,durability_value=pcall(function() return prototypes.item["repair-pack"].durability end)
if durability_ok and type(durability_value)=="number" and durability_value>0 then durability=durability_value end
local damage=max_health-health
local needed=math.ceil(damage/durability)
local packs=math.min(needed, %d)
local inv=c.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local have=inv.get_item_count({name="repair-pack",quality="normal"})
if have<packs then fail("not enough repair packs: need "..packs..", have "..have); return end
local removed=inv.remove{name="repair-pack",quality="normal",count=packs}
if removed~=packs then
  if removed>0 then inv.insert{name="repair-pack",quality="normal",count=removed} end
  fail("could not remove repair packs"); return
end
local after=math.min(max_health, health+packs*durability)
local set_ok,set_error=pcall(function() e.health=after end)
if not set_ok then
  inv.insert{name="repair-pack",quality="normal",count=packs}
  fail("could not apply repair: "..tostring(set_error)); return
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=e.surface.name,force=e.force.name,name=e.name,unit_number=e.unit_number,position=e.position,before=health,after=after,max_health=max_health,packs_used=packs}))`, count), nil
}

// repairEntity repairs a reachable damaged entity using repair packs from the
// acting character's main inventory. count bounds the number of repair packs
// (repair actions) spent in one call.
func repairEntity(r *Resolver, input RepairInput) (*RepairResult, error) {
	if err := validateRepairInput(input); err != nil {
		return nil, err
	}
	target, err := settingsTargetFromRef(input.Actor, input.Target)
	if err != nil {
		return nil, err
	}
	lua, err := repairLua(target, input.Count)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, luaFail+lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[RepairResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("repaired %s from %s to %s of %s health with %d repair pack(s)",
		result.Name, formatFloat(result.Before), formatFloat(result.After), formatFloat(result.MaxHealth), result.PacksUsed))
	return result, nil
}
