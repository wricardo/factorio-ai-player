package main

import (
	"errors"
	"strings"
)

// This file implements the space platform lifecycle, the platform read, and the
// player controller read.
//
// The engine exposes platform creation only through Lua
// (LuaForce.create_space_platform) and starter-pack application only through
// LuaSpacePlatform.apply_starter_pack, so both are wrapped here instead of
// being reachable from the schema. A platform created without a starter pack
// has no surface at all, which is why the snapshot reports surface null and a
// zero tile count instead of an empty surface.

// luaSpacePlatformInfo snapshots one LuaSpacePlatform into the JSON shape
// SpacePlatformInfo decodes. hub and space_location may be absent on a pending
// platform, so both are read defensively.
const luaSpacePlatformInfo = `
local function platform_info(p)
  local ok_surface,surface=pcall(function() return p.surface end)
  if not ok_surface then surface=nil end
  local ok_hub,hub=pcall(function() return p.hub end)
  if not ok_hub then hub=nil end
  local space_location=nil
  local ok_location,location=pcall(function() return p.space_location end)
  if ok_location and location then space_location=location.name end
  local info={tick=game.tick,name=p.name,force=p.force.name,
    surface=surface and surface.name or nil,
    space_location=space_location,
    hub_unit_number=hub and tostring(hub.unit_number) or nil,
    hub_position=hub and hub.position or nil,
    foundation_tiles=0,entity_count=0,started=surface~=nil}
  if surface then
    info.foundation_tiles=surface.count_tiles_filtered{name="space-platform-foundation"}
    info.entity_count=surface.count_entities_filtered{}
  end
  return info
end
local function platform_by_name(f,name)
  for _,p in pairs(f.platforms) do if p.name==name then return p end end
  return nil
end
`

// spacePlatformForceLua resolves a non-empty force name into f.
func spacePlatformForceLua(force string) string {
	return "local f=game.forces[" + luaString(force) + "]\nif not f then fail(\"force not found\"); return end\n"
}

// spacePlatformsLua lists every platform of one force.
func spacePlatformsLua(in SpacePlatformsInput) (string, error) {
	force := in.Force
	if strings.TrimSpace(force) == "" {
		return "", errors.New("force must not be empty")
	}
	lua := luaFail + luaSpacePlatformInfo + spacePlatformForceLua(force) +
		`local platforms={}
for _,p in pairs(f.platforms) do platforms[#platforms+1]=platform_info(p) end
table.sort(platforms,function(a,b) return a.name<b.name end)
rcon.print(helpers.table_to_json({tick=game.tick,force=f.name,platforms=platforms}))
`
	return lua, nil
}

// createSpacePlatformLua validates the input and builds the creation Lua.
func createSpacePlatformLua(in CreateSpacePlatformInput) (string, error) {
	force := in.Force
	if strings.TrimSpace(force) == "" {
		return "", errors.New("force must not be empty")
	}
	if strings.TrimSpace(in.Planet) == "" {
		return "", errors.New("planet must not be empty")
	}
	if strings.TrimSpace(in.StarterPack) == "" {
		return "", errors.New("starterPack must not be empty")
	}
	quality := "normal"
	if in.StarterPackQuality != nil && strings.TrimSpace(*in.StarterPackQuality) != "" {
		quality = *in.StarterPackQuality
	}
	name := ""
	if in.Name != nil {
		name = *in.Name
	}
	apply := in.ApplyStarterPack
	applyLua := ""
	if apply {
		applyLua = `local applied_ok,applied_error=pcall(function() return p.apply_starter_pack() end)
if not applied_ok then fail("could not apply the starter pack: "..tostring(applied_error)); return end
`
	}
	lua := luaFail + luaSpacePlatformInfo + spacePlatformForceLua(force) +
		"local planet=" + luaString(in.Planet) + "\n" +
		`if not game.planets[planet] then fail("planet not found"); return end
if not prototypes.item[` + luaString(in.StarterPack) + `] then fail("starter pack item not found"); return end
if not prototypes.quality[` + luaString(quality) + `] then fail("quality not found"); return end
local options={planet=planet,starter_pack={name=` + luaString(in.StarterPack) + `,quality=` + luaString(quality) + `}}
local requested_name=` + luaString(name) + `
if requested_name~="" then options.name=requested_name end
if requested_name~="" and platform_by_name(f,requested_name) then fail("a platform with that name already exists"); return end
local created_ok,created=pcall(function() return f.create_space_platform(options) end)
if not created_ok then fail("could not create the platform: "..tostring(created)); return end
if not created then fail("the engine did not create a platform"); return end
local p=created
` + applyLua + `
rcon.print(helpers.table_to_json(platform_info(p)))
`
	return lua, nil
}

// spacePlatformRefLua validates a platform reference and returns the shared
// lookup prefix; the caller resolves p by name.
func spacePlatformRefLua(force, platform string) (string, error) {
	if strings.TrimSpace(force) == "" {
		return "", errors.New("force must not be empty")
	}
	if strings.TrimSpace(platform) == "" {
		return "", errors.New("platform must not be empty")
	}
	return luaFail + luaSpacePlatformInfo + spacePlatformForceLua(force) +
		"local p=platform_by_name(f," + luaString(platform) + ")\nif not p then fail(\"space platform not found\"); return end\n", nil
}

// applyStarterPackLua applies the pending starter pack of one platform.
func applyStarterPackLua(in SpacePlatformRefInput) (string, error) {
	prefix, err := spacePlatformRefLua(in.Force, in.Platform)
	if err != nil {
		return "", err
	}
	return prefix + `local applied_ok,applied_error=pcall(function() return p.apply_starter_pack() end)
if not applied_ok then fail("could not apply the starter pack: "..tostring(applied_error)); return end
rcon.print(helpers.table_to_json(platform_info(p)))
`, nil
}

// deleteSpacePlatformLua schedules one platform for deletion.
func deleteSpacePlatformLua(in SpacePlatformRefInput) (string, error) {
	prefix, err := spacePlatformRefLua(in.Force, in.Platform)
	if err != nil {
		return "", err
	}
	// The snapshot is taken before destroy because destroy schedules deletion
	// and the platform can disappear from the force list immediately.
	return prefix + `local before=platform_info(p)
p.destroy()
rcon.print(helpers.table_to_json(before))
`, nil
}

// spacePlatformsQuery reads every platform of one force.
func spacePlatformsQuery(r *Resolver, in *SpacePlatformsInput) (*SpacePlatformPage, error) {
	input := SpacePlatformsInput{}
	if in != nil {
		input = *in
	}
	lua, err := spacePlatformsLua(input)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	page, err := factoryDecode[SpacePlatformPage](response)
	if err != nil {
		return nil, err
	}
	return page, nil
}

// createSpacePlatform creates one platform.
func createSpacePlatform(r *Resolver, in CreateSpacePlatformInput) (*SpacePlatformInfo, error) {
	lua, err := createSpacePlatformLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	info, err := factoryDecode[SpacePlatformInfo](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "created space platform "+info.Name)
	return info, nil
}

// applyStarterPack applies one platform's pending starter pack.
func applyStarterPack(r *Resolver, in SpacePlatformRefInput) (*SpacePlatformInfo, error) {
	lua, err := applyStarterPackLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	info, err := factoryDecode[SpacePlatformInfo](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "applied starter pack on "+info.Name)
	return info, nil
}

// deleteSpacePlatform schedules one platform for deletion.
func deleteSpacePlatform(r *Resolver, in SpacePlatformRefInput) (*SpacePlatformInfo, error) {
	lua, err := deleteSpacePlatformLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	info, err := factoryDecode[SpacePlatformInfo](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "scheduled space platform "+info.Name+" for deletion")
	return info, nil
}

// playerStateLua reads one connected player's controller and surface state. The
// controller name is resolved by scanning defines.controllers, so the value
// matches the engine's own enum rather than a hardcoded number.
func playerStateLua(player string) (string, error) {
	if strings.TrimSpace(player) == "" {
		return "", errors.New("player must not be empty")
	}
	return luaFail + `local p=game.get_player(` + luaString(player) + `)
if not p then fail("player not found"); return end
local controller=nil
for name,value in pairs(defines.controllers) do if value==p.controller_type then controller=name end end
local surface=p.surface
local is_platform=false
local platform_name=nil
if surface then
  local ok_platform,platform=pcall(function() return surface.platform end)
  if ok_platform and platform then is_platform=true; platform_name=platform.name end
end
local character=p.character
rcon.print(helpers.table_to_json({tick=game.tick,player=p.name,force=p.force.name,
  controller=controller, surface=surface and surface.name or nil,
  space_platform=is_platform, platform_name=platform_name,
  character=character and tostring(character.unit_number) or nil,
  position=character and character.position or nil}))
`, nil
}

// playerStateQuery reads one player's controller state.
func playerStateQuery(r *Resolver, player string) (*PlayerState, error) {
	lua, err := playerStateLua(player)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	state, err := factoryDecode[PlayerState](response)
	if err != nil {
		return nil, err
	}
	return state, nil
}
