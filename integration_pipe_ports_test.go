//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

// A filtered refinery outlet may refuse a pipe without physical blockers when
// that pipe would join an incompatible fluid. A longer route must not bypass
// this engine rule (factorio-ai-player-26g).
func TestIntegrationPipePortPlacement(t *testing.T) {
	t.Setenv(graphqlLogEnv, filepath.Join(t.TempDir(), "graphql-log.jsonl"))
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	character := h.spawnCharacter(t)

	for _, tc := range []struct {
		name    string
		fluid   string
		through bool
	}{
		{name: "EmptySingleTile"},
		{name: "MatchingFluidThroughRun", fluid: "petroleum-gas", through: true},
		{name: "IncompatibleFluidSingleTile", fluid: "water"},
		{name: "IncompatibleFluidThroughRun", fluid: "water", through: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.lua(t, `local c=game.get_entity_by_unit_number(`+character.id+`)
c.get_inventory(defines.inventory.character_main).clear()
rcon.print("ok")`)
			h.grantItems(t, character.id, map[string]int{"pipe": 20})
			out := h.lua(t, fmt.Sprintf(`local s=game.surfaces[%q]
for _,e in pairs(s.find_entities_filtered{area={{3,3},{14,14}}}) do e.destroy() end
local r=s.create_entity{name="oil-refinery",position={8.5,8.5},direction=0,force="player"}
assert(r,"refinery")
r.set_recipe("basic-oil-processing")
local target
for i=1,#r.fluidbox do
  local filter=r.fluidbox.get_filter(i)
  if filter and filter.name=="petroleum-gas" then
    for _,c in pairs(r.fluidbox.get_pipe_connections(i)) do target=c.target_position end
  end
end
assert(target,"petroleum-gas outlet target")
local p=s.create_entity{name="pipe",position={target.x-1,target.y},force="player"}
assert(p,"neighbour pipe")
if %q~="" then p.fluidbox[1]={name=%q,amount=50} end
local manual=s.can_place_entity{name="pipe",position=target,direction=0,force="player",build_check_type=defines.build_check_type.manual}
rcon.print(helpers.table_to_json{target=target,manual=manual,tick=game.tick})`, integrationSurface, tc.fluid, tc.fluid))
			var fixture struct {
				Target struct{ X, Y float64 }
				Manual bool
				Tick   int
			}
			if err := json.Unmarshal([]byte(out), &fixture); err != nil {
				t.Fatalf("refinery fixture: %v: %s", err, out)
			}
			incompatible := tc.fluid == "water"
			if fixture.Manual == incompatible {
				t.Fatalf("engine manual placeability=%t for fluid %q at tick %d", fixture.Manual, tc.fluid, fixture.Tick)
			}
			x, y := fixture.Target.X, fixture.Target.Y
			from, to, direction, needed := x, x, ",direction:0", 1
			if tc.through {
				from, to, direction, needed = x-1, x+1, "", 2
			}
			document := fmt.Sprintf(`mutation { placePipe(input:{actor:{character:%q},surface:%q,from:{x:%g,y:%g},to:{x:%g,y:%g}%s,allowFluidAdjacency:true}) {
  possible placed failed conflicts { kind position { x y } } errors { code }
} }`, character.id, integrationSurface, from, y, to, y, direction)
			apply := func() map[string]any {
				return jsonObject(t, h.mutation(t, document), "data", "placePipe")
			}
			checkWorld := func(placed int) {
				out := h.lua(t, fmt.Sprintf(`local s=game.surfaces[%q]
local pipes=s.find_entities_filtered{name="pipe",area={{3,3},{14,14}}}
local joined=false
local r=s.find_entities_filtered{name="oil-refinery",position={8.5,8.5}}[1]
for i=1,#r.fluidbox do
  local filter=r.fluidbox.get_filter(i)
  if filter and filter.name=="petroleum-gas" then
    for _,c in pairs(r.fluidbox.get_pipe_connections(i)) do
      if c.target and c.target.owner and c.target.owner.name=="pipe" then joined=true end
    end
  end
end
local c=game.get_entity_by_unit_number(%s)
rcon.print(helpers.table_to_json{pipes=#pipes,joined=joined,inventory=c.get_inventory(defines.inventory.character_main).get_item_count("pipe")})`, integrationSurface, character.id))
				var world struct {
					Pipes, Inventory int
					Joined           bool
				}
				if err := json.Unmarshal([]byte(out), &world); err != nil {
					t.Fatalf("pipe readback: %v: %s", err, out)
				}
				if world.Pipes != 1+placed || world.Inventory != 20-placed || world.Joined != (placed > 0) {
					t.Fatalf("pipe world=%+v after placing %d; want pipes=%d inventory=%d joined=%t", world, placed, 1+placed, 20-placed, placed > 0)
				}
			}
			result := apply()
			if incompatible {
				if jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != 0 {
					t.Fatalf("incompatible fluid route was accepted: %v", result)
				}
				collision := false
				for _, raw := range jsonArray(t, result, "conflicts") {
					conflict := raw.(map[string]any)
					position := jsonObject(t, conflict, "position")
					if jsonString(t, conflict, "kind") == "COLLISION" && jsonNumber(t, position, "x") == x && jsonNumber(t, position, "y") == y {
						collision = true
					}
				}
				if !collision {
					t.Fatalf("missing engine refusal at refinery outlet: %v", result)
				}
				checkWorld(0)
				// Clear only the incompatible neighbour; the same route must now
				// work without bypassing placement or fluid adjacency checks.
				h.lua(t, fmt.Sprintf(`local s=game.surfaces[%q]
local p=s.find_entities_filtered{name="pipe",position={%g,%g}}[1]
assert(p,"neighbour")
p.clear_fluid_inside()
rcon.print("ok")`, integrationSurface, x-1, y))
				result = apply()
			}
			if !jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != float64(needed) || jsonNumber(t, result, "failed") != 0 {
				t.Fatalf("compatible refinery outlet placement: %v", result)
			}
			checkWorld(needed)
		})
	}
}
