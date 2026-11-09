//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const integrationSurface = "factorio-ai-player-integration"

// integrationExtraMods maps a mod directory name (<name>_<version>) to a mod
// directory linked into the next harness, for fixtures that change prototypes.
// Tests set it before startIntegrationHarness and clear it afterwards.
var integrationExtraMods map[string]string

type integrationHarness struct {
	binary   string
	version  string
	dir      string
	addr     string
	password string
	cmd      *exec.Cmd
	logFile  *os.File
	wait     chan error

	mu      sync.Mutex
	stopped bool
}

func TestIntegration(t *testing.T) {
	// Keep the suite's GraphQL operation log out of the developer's live log.
	_ = os.Setenv(graphqlLogEnv, filepath.Join(t.TempDir(), "graphql-log.jsonl"))
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })

	t.Run("SchemaContract", func(t *testing.T) {
		result := h.query(t, `{
		  __schema { mutationType { fields { name } } }
		}`)
		fields := jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "__schema"), "mutationType", "fields")
		got := make([]string, 0, len(fields))
		for _, field := range fields {
			got = append(got, jsonString(t, field.(map[string]any), "name"))
		}
		for _, required := range []string{"characterSpawn", "place", "remove", "setLampSettings", "setCircuitBehavior", "addChartTag", "removeChartTag", "navigateStart", "screenshot", "connectEntities", "waitUntil", "placeNextTo", "save", "shutdown"} {
			if !slices.Contains(got, required) {
				t.Fatalf("required mutation %q absent from working-tree schema; mutations=%v", required, got)
			}
		}
	})

	t.Run("StatusTick", func(t *testing.T) {
		first := h.status(t)
		time.Sleep(100 * time.Millisecond)
		second := h.status(t)
		if second <= first {
			t.Fatalf("Factorio tick did not advance: first=%v second=%v", first, second)
		}
		t.Logf("real Factorio binary=%s version=%q rcon=%s tick=%v", h.binary, h.version, h.addr, second)
	})

	t.Run("WorldSnapshot", func(t *testing.T) {
		result := h.query(t, `{
		  status { players { name } }
		  characters { pageInfo { total } }
		  entities(input:{surface:"nauvis",area:{leftTopX:-100,leftTopY:-100,rightBottomX:100,rightBottomY:100},limit:1000}) {
		    pageInfo { total }
		    entities { name type }
		  }
		}`)
		data := jsonObject(t, result, "data")
		if players := jsonArray(t, data, "status", "players"); len(players) != 0 {
			t.Fatalf("fresh sandbox has connected players: %v", players)
		}
		if total := jsonNumber(t, jsonObject(t, jsonObject(t, data, "characters"), "pageInfo"), "total"); total != 0 {
			t.Fatalf("fresh sandbox character count=%v, want 0", total)
		}
		entities := jsonObject(t, data, "entities")
		if total := jsonNumber(t, jsonObject(t, entities, "pageInfo"), "total"); total <= 0 {
			t.Fatalf("fresh sandbox has no natural entities: %v", entities)
		}
		machineTypes := map[string]bool{
			"assembling-machine": true, "container": true, "electric-pole": true,
			"furnace": true, "inserter": true, "lamp": true, "transport-belt": true,
		}
		for _, raw := range jsonArray(t, entities, "entities") {
			entity := raw.(map[string]any)
			if machineTypes[jsonString(t, entity, "type")] {
				t.Fatalf("fresh sandbox unexpectedly contains machine: %v", entity)
			}
		}
	})

	h.createTestSurface(t)

	// tile_occupants tests only the tile under the requested position, so a
	// multi-tile prototype whose centre tile is free used to be created on top of
	// a neighbour (create_entity ignores collision boxes). The post-create check
	// must undo that creation, refund the item and refuse the call.
	t.Run("PlaceRejectsFootprintOverlap", func(t *testing.T) {
		character := h.spawnCharacter(t)
		h.grantItems(t, character.id, map[string]int{"inserter": 1, "assembling-machine-1": 1})
		h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:5.5,y:5.5},item:"inserter"}) { unitNumber }
		}`, character.id, integrationSurface))

		// A 3x3 assembler centred at (6.5,6.5) covers x 5..8, y 5..8, which
		// includes the inserter's tile, while its own centre tile is free.
		err := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:6.5,y:6.5},item:"assembling-machine-1"}) { unitNumber }
		}`, character.id, integrationSurface))
		if !strings.Contains(err.Error(), "footprint overlaps inserter") {
			t.Fatalf("overlapping place error=%v, want a footprint overlap refusal", err)
		}
		if got := h.entityCount(t, "assembling-machine-1"); got != 0 {
			t.Fatalf("assembler count=%v after a refused overlap, want 0", got)
		}
		if got := h.entityCount(t, "inserter"); got != 1 {
			t.Fatalf("inserter count=%v after a refused overlap, want the original 1", got)
		}
		// placeMany shares the guard: the overlap is reported as a COLLISION for
		// that index and no entity is left behind.
		batched := h.mutation(t, fmt.Sprintf(`mutation {
		  placeMany(input:{actor:{character:%q},surface:%q,item:"assembling-machine-1",
		    placements:[{position:{x:6.5,y:6.5}}]}) {
		    placed failed errors { index code message }
		  }
		}`, character.id, integrationSurface))
		placed := jsonObject(t, jsonObject(t, batched, "data"), "placeMany")
		if jsonNumber(t, placed, "placed") != 0 || jsonNumber(t, placed, "failed") != 1 {
			t.Fatalf("placeMany placed=%v failed=%v, want 0/1", jsonNumber(t, placed, "placed"), jsonNumber(t, placed, "failed"))
		}
		first := jsonArray(t, placed, "errors")[0].(map[string]any)
		if jsonString(t, first, "code") != "COLLISION" || !strings.Contains(jsonString(t, first, "message"), "footprint overlaps inserter") {
			t.Fatalf("placeMany error=%v, want a COLLISION naming the footprint overlap", first)
		}
		if got := h.entityCount(t, "assembling-machine-1"); got != 0 {
			t.Fatalf("assembler count=%v after a refused placeMany overlap, want 0", got)
		}
		h.destroyCharacter(t, character.id)
	})

	// buildCell promises that a refused cell leaves the world unchanged. Its
	// preflight used to test only each entry's centre tile, so a 3x3 entry whose
	// footprint covered an earlier entry or a neighbour was accepted, built the
	// earlier entries, then failed inside placeMany (factorio-ai-player-t2d).
	t.Run("BuildCellRefusesFootprintOverlapUpFront", func(t *testing.T) {
		character := h.spawnCharacter(t)
		h.grantItems(t, character.id, map[string]int{"transport-belt": 2, "assembling-machine-1": 1, "inserter": 1})
		cell := h.mutation(t, fmt.Sprintf(`mutation {
		  buildCell(input:{actor:{character:%q},surface:%q,entries:[
		    {ref:"belt", place:{item:"transport-belt", position:{x:8.5,y:8.5}}},
		    {ref:"am", place:{item:"assembling-machine-1", position:{x:9.5,y:9.5}}}
		  ]}) {
		    completed placed steps { index ok errors { code message } }
		  }
		}`, character.id, integrationSurface))
		built := jsonObject(t, jsonObject(t, cell, "data"), "buildCell")
		if jsonBool(t, built, "completed") {
			t.Fatalf("buildCell completed=%v for a cell whose assembler covers the belt", jsonBool(t, built, "completed"))
		}
		if got := h.entityCount(t, "assembling-machine-1"); got != 0 {
			t.Fatalf("assembler count=%v, want 0", got)
		}
		if got := h.entityCount(t, "transport-belt"); got != 0 {
			t.Fatalf("transport-belt count=%v after a refused cell, want 0 (nothing written)", got)
		}
		// Two entries of one cell overlapping each other are refused by the
		// intra-layout check, not by the world scan.
		if msg := firstCellErrorMessage(t, built); !strings.Contains(msg, "footprint overlaps entry") {
			t.Fatalf("intra-cell overlap message=%q, want a footprint-overlaps-entry refusal", msg)
		}

		// Same guarantee against a pre-existing world entity: the assembler's
		// footprint covers an inserter that is already built, so nothing in the
		// cell may be written.
		anchor := h.place(t, character.id, "inserter", 8.5, 8.5)
		blocked := h.mutation(t, fmt.Sprintf(`mutation {
		  buildCell(input:{actor:{character:%q},surface:%q,entries:[
		    {ref:"belt", place:{item:"transport-belt", position:{x:12.5,y:12.5}}},
		    {ref:"am", place:{item:"assembling-machine-1", position:{x:9.5,y:9.5}}}
		  ]}) { completed placed steps { index ok errors { code message } } }
		}`, character.id, integrationSurface))
		refused := jsonObject(t, jsonObject(t, blocked, "data"), "buildCell")
		if jsonBool(t, refused, "completed") || jsonNumber(t, refused, "placed") != 0 {
			t.Fatalf("buildCell on a cell covering a built inserter=%v, want a refusal with nothing placed", refused)
		}
		if got := h.entityCount(t, "transport-belt"); got != 0 {
			t.Fatalf("transport-belt count=%v after a cell refused against a world entity, want 0", got)
		}
		if msg := firstCellErrorMessage(t, refused); !strings.Contains(msg, "footprint overlaps inserter") {
			t.Fatalf("world-overlap message=%q, want a footprint-overlaps-inserter refusal", msg)
		}
		h.removeEntity(t, character.id, "inserter", anchor)
		h.destroyCharacter(t, character.id)
	})

	t.Run("ChartTagsRoundTrip", func(t *testing.T) {
		const markerText = "integration marker"
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local force=game.forces.player
local surface=game.surfaces["`+integrationSurface+`"]
if force and surface then
  for _,tag in pairs(force.find_chart_tags(surface)) do
    if tag.text=="`+markerText+`" then tag.destroy() end
  end
end
rcon.print("ok")`)
		})

		added := h.mutation(t, `mutation {
		  addChartTag(input:{surface:"`+integrationSurface+`",force:"player",
		    position:{x:7.5,y:-3.25},text:"`+markerText+`",icon:{type:ITEM,name:"iron-plate"}}) {
		    force surface tag { tagNumber text position { x y } icon { type name quality } }
		  }
		}`)
		tag := jsonObject(t, jsonObject(t, jsonObject(t, added, "data"), "addChartTag"), "tag")
		tagNumber := jsonString(t, tag, "tagNumber")
		if jsonString(t, tag, "text") != markerText {
			t.Fatalf("added chart tag=%v, want text %q", tag, markerText)
		}
		icon := jsonObject(t, tag, "icon")
		if jsonString(t, icon, "type") != "ITEM" || jsonString(t, icon, "name") != "iron-plate" {
			t.Fatalf("added chart tag icon=%v", icon)
		}

		listed := h.query(t, `{
		  chartTags(input:{surface:"`+integrationSurface+`",force:"player",
		    area:{leftTopX:7,leftTopY:-4,rightBottomX:8,rightBottomY:-3},limit:10}) {
		    pageInfo { total limit offset hasMore }
		    tags { tagNumber text position { x y } icon { type name quality } }
		  }
		}`)
		page := jsonObject(t, jsonObject(t, listed, "data"), "chartTags")
		if total := jsonNumber(t, jsonObject(t, page, "pageInfo"), "total"); total != 1 {
			t.Fatalf("chart tag page total=%v, page=%v", total, page)
		}
		tags := jsonArray(t, page, "tags")
		if len(tags) != 1 || jsonString(t, tags[0].(map[string]any), "tagNumber") != tagNumber {
			t.Fatalf("listed chart tags=%v, want tag %s", tags, tagNumber)
		}

		removed := h.mutation(t, `mutation {
		  removeChartTag(input:{surface:"`+integrationSurface+`",force:"player",tagNumber:"`+tagNumber+`"}) {
		    tag { tagNumber text }
		  }
		}`)
		removedTag := jsonObject(t, jsonObject(t, jsonObject(t, removed, "data"), "removeChartTag"), "tag")
		if jsonString(t, removedTag, "tagNumber") != tagNumber || jsonString(t, removedTag, "text") != markerText {
			t.Fatalf("removed chart tag=%v", removedTag)
		}

		after := h.query(t, `{
		  chartTags(input:{surface:"`+integrationSurface+`",force:"player"}) {
		    pageInfo { total }
		  }
		}`)
		if total := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "chartTags"), "pageInfo"), "total"); total != 0 {
			t.Fatalf("chart tag remained after removal: total=%v", total)
		}
	})

	t.Run("PlaceGhosts", func(t *testing.T) {
		const area = `{leftTopX:10,leftTopY:-14,rightBottomX:22,rightBottomY:-4}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})

		placed := h.mutation(t, `mutation {
		  placeGhosts(input:{surface:"`+integrationSurface+`",force:"player",
		    entities:[
		      {name:"transport-belt",position:{x:11.5,y:-11.5}},
		      {name:"inserter",position:{x:13.5,y:-11.5},direction:4},
		      {name:"assembling-machine-1",position:{x:16.5,y:-11.5},recipe:"iron-gear-wheel"}
		    ],
		    tiles:[{name:"stone-path",x:20,y:-6}]
		  }) {
		    entitiesCreated tilesCreated failed
		    ghosts { name type innerName quality position { x y } }
		    errors { index code message }
		  }
		}`)
		result := jsonObject(t, jsonObject(t, placed, "data"), "placeGhosts")
		if created := jsonNumber(t, result, "entitiesCreated"); created != 3 {
			t.Fatalf("entitiesCreated=%v, want 3; result=%v", created, result)
		}
		if tiles := jsonNumber(t, result, "tilesCreated"); tiles != 1 {
			t.Fatalf("tilesCreated=%v, want 1; result=%v", tiles, result)
		}
		if failed := jsonNumber(t, result, "failed"); failed != 0 {
			t.Fatalf("failed=%v, want 0; errors=%v", failed, jsonArray(t, result, "errors"))
		}
		inner := map[string]bool{}
		for _, raw := range jsonArray(t, result, "ghosts") {
			inner[jsonString(t, raw.(map[string]any), "innerName")] = true
		}
		for _, want := range []string{"transport-belt", "inserter", "assembling-machine-1", "stone-path"} {
			if !inner[want] {
				t.Fatalf("ghost %q missing from result %v", want, inner)
			}
		}

		// The engine must hold the plan: ghosts of the force inside the area,
		// and the assembling-machine ghost must still carry its recipe.
		live := h.query(t, fmt.Sprintf(`{
		  ghosts(input:{surface:%q,force:"player",area:%s,limit:100}) {
		    pageInfo { total }
		    ghostEntries { name kind position { x y } }
		  }
		}`, integrationSurface, area))
		page := jsonObject(t, jsonObject(t, live, "data"), "ghosts")
		if total := jsonNumber(t, jsonObject(t, page, "pageInfo"), "total"); total != 4 {
			t.Fatalf("live ghost total=%v, want 4; ghosts=%v", total, jsonArray(t, page, "ghostEntries"))
		}
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local ghost=surface.find_entity("entity-ghost",{x=16.5,y=-11.5})
assert(ghost and ghost.valid, "assembling-machine ghost missing")
assert(ghost.ghost_name=="assembling-machine-1", "ghost inner name is "..tostring(ghost.ghost_name))
local recipe=ghost.get_recipe()
assert(recipe and recipe.name=="iron-gear-wheel", "ghost recipe is "..tostring(recipe and recipe.name))
rcon.print("ok")`)
	})

	t.Run("GhostInspectionReportsPrototype", func(t *testing.T) {
		// A blueprint plan is only auditable if a ghost reports the prototype it
		// would build, so the round trip the ticket describes is exercised
		// end to end: export entities, place them as ghosts, inspect the ghosts.
		const area = `{leftTopX:24,leftTopY:-16,rightBottomX:36,rightBottomY:-6}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })

		exported := h.mutation(t, `mutation {
		  createBlueprintFromEntities(input:{entities:[
		    {name:"assembling-machine-1",position:{x:0.5,y:0.5},direction:0,recipe:"iron-gear-wheel"},
		    {name:"transport-belt",position:{x:3.5,y:0.5},direction:4}
		  ]}) { blueprint entityCount }
		}`)
		blueprint := jsonString(t, jsonObject(t, jsonObject(t, exported, "data"), "createBlueprintFromEntities"), "blueprint")
		// The surface is 64x64 (tiles -32..31), so the plan must stay inside it:
		// a footprint that crosses the edge makes build_blueprint return an
		// empty array without raising, which the mutation now reports as an
		// error (asserted below).
		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  placeBlueprint(input:{surface:%q,force:"player",blueprint:%q,position:{x:27,y:-12}}) {
		    ghosts entityCount entities { name type unitNumber }
		  }
		}`, integrationSurface, blueprint))
		placeResult := jsonObject(t, jsonObject(t, placed, "data"), "placeBlueprint")
		// A placement that cannot create a single ghost must fail loudly instead
		// of reporting 0 of N as success: (30,-10) puts the belt past the map edge.
		edgeErr := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation {
		  placeBlueprint(input:{surface:%q,force:"player",blueprint:%q,position:{x:30,y:-10}}) { ghosts entityCount }
		}`, integrationSurface, blueprint))
		if edgeErr == nil || !strings.Contains(edgeErr.Error(), "placed none") {
			t.Fatalf("an out-of-map blueprint placement must fail with the placed-none error, got %v", edgeErr)
		}
		ghosts := jsonArray(t, placeResult, "entities")
		if count := jsonNumber(t, placeResult, "ghosts"); float64(len(ghosts)) != count || count != 2 {
			t.Fatalf("ghosts=%v entities=%d, want 2; result=%v", count, len(ghosts), placeResult)
		}
		var targets strings.Builder
		for i, raw := range ghosts {
			row := raw.(map[string]any)
			if i > 0 {
				targets.WriteString(",")
			}
			fmt.Fprintf(&targets, `{entity:"entity-ghost",unitNumber:%q}`, jsonString(t, row, "unitNumber"))
		}
		// A real entity in the same read proves the ghost fields stay null
		// instead of leaking the ghost prototype onto everything.
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local chest=surface.create_entity{name="wooden-chest",position={x=30.5,y=-13.5},force="player"}
assert(chest and chest.valid,"chest creation failed")
rcon.print("ok")`)
		targets.WriteString(`,{entity:"wooden-chest",position:{x:30.5,y:-13.5}}`)
		inspected := h.query(t, fmt.Sprintf(`{
		  inspectMany(input:{actor:{character:%q},surface:%q,targets:[%s]}) {
		    results { index unitNumber error inspection { name type direction ghostName ghostQuality ghostRecipe } }
		  }
		}`, character.id, integrationSurface, targets.String()))
		results := jsonArray(t, jsonObject(t, jsonObject(t, inspected, "data"), "inspectMany"), "results")
		if len(results) != 3 {
			t.Fatalf("results=%d, want 3: %v", len(results), results)
		}
		prototypes := map[string]map[string]any{}
		for _, raw := range results {
			row := raw.(map[string]any)
			if message, ok := row["error"]; ok && message != nil {
				t.Fatalf("inspect row failed: %v", row)
			}
			inspection := jsonObject(t, row, "inspection")
			if jsonString(t, inspection, "type") != "entity-ghost" {
				// The real chest: every ghost-only field must be null.
				for _, field := range []string{"ghostName", "ghostQuality", "ghostRecipe"} {
					if value, ok := inspection[field]; !ok || value != nil {
						t.Fatalf("%s on a real entity = %v, want null: %v", field, value, inspection)
					}
				}
				continue
			}
			prototypes[jsonString(t, inspection, "ghostName")] = inspection
		}
		machine, ok := prototypes["assembling-machine-1"]
		if !ok {
			t.Fatalf("ghost prototype set = %v, want assembling-machine-1 and transport-belt", prototypes)
		}
		if recipe, _ := machine["ghostRecipe"].(string); recipe != "iron-gear-wheel" {
			t.Fatalf("ghostRecipe = %v, want the recipe carried by the ghost", machine["ghostRecipe"])
		}
		if quality, _ := machine["ghostQuality"].(string); quality != "normal" {
			t.Fatalf("ghostQuality = %v, want normal", machine["ghostQuality"])
		}
		belt, ok := prototypes["transport-belt"]
		if !ok {
			t.Fatalf("ghost prototype set = %v, want the belt ghost", prototypes)
		}
		if direction := jsonNumber(t, belt, "direction"); direction != 4 {
			t.Fatalf("belt ghost direction = %v, want 4", direction)
		}
	})

	t.Run("CharacterSpawnInventory", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 1, "iron-plate": 1})

		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:4,y:0},item:"wooden-chest"}) {
		    unitNumber position { x y }
		  }
		}`, character.id, integrationSurface))
		place := jsonObject(t, jsonObject(t, placed, "data"), "place")
		unit := jsonString(t, place, "unitNumber")
		position := jsonObject(t, place, "position")
		t.Cleanup(func() { h.removeEntity(t, character.id, "wooden-chest", unit) })

		moved := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{actor:{character:%q},target:{surface:%q,entity:"wooden-chest",unitNumber:%q},inventory:CHEST,direction:TO_ENTITY,item:"iron-plate",count:1}) { moved }
		}`, character.id, integrationSurface, unit))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, moved, "data"), "transfer"), "moved"); got != 1 {
			t.Fatalf("transfer moved=%v, want 1; chest position=%v", got, position)
		}

		inspection := h.query(t, fmt.Sprintf(`{
		  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"wooden-chest",unitNumber:%q}}) {
		    inventories { chest { name count } }
		  }
		}`, character.id, integrationSurface, unit))
		chest := jsonArray(t, jsonObject(t, jsonObject(t, jsonObject(t, inspection, "data"), "inspect"), "inventories"), "chest")
		if !hasItem(chest, "iron-plate", 1) {
			t.Fatalf("transferred iron plate absent from chest: %v", chest)
		}

		returned := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{actor:{character:%q},target:{surface:%q,entity:"wooden-chest",unitNumber:%q},inventory:CHEST,direction:TO_ACTOR,item:"iron-plate",count:1}) { moved }
		}`, character.id, integrationSurface, unit))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, returned, "data"), "transfer"), "moved"); got != 1 {
			t.Fatalf("return transfer moved=%v, want 1", got)
		}
	})

	t.Run("CraftPlanHandCrafting", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		// The sandbox starts with almost nothing researched; hand crafting needs
		// the inserter chain enabled on the character's force.
		h.lua(t, `for _,recipe in pairs(game.forces.player.recipes) do recipe.enabled=true end
rcon.print("ok")`)

		// Empty inventory: the plan must report the non-hand-craftable raws and
		// the mutation must refuse to queue anything.
		planned := h.query(t, fmt.Sprintf(`{
		  craftPlan(input:{actor:{character:%q},recipe:"inserter"}) {
		    complete steps { recipe count } missing { name type amount }
		  }
		}`, character.id))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "craftPlan")
		if complete, _ := plan["complete"].(bool); complete {
			t.Fatalf("plan with an empty inventory must be incomplete: %v", plan)
		}
		missing := jsonArray(t, plan, "missing")
		missingAmount := func(name string) float64 {
			for _, raw := range missing {
				row := raw.(map[string]any)
				if row["name"] == name {
					amount, _ := row["amount"].(float64)
					return amount
				}
			}
			return 0
		}
		if missingAmount("iron-plate") != 4 || missingAmount("copper-plate") != 2 {
			t.Fatalf("missing raws = %v, want iron-plate 4 and copper-plate 2", missing)
		}
		steps := jsonArray(t, plan, "steps")
		if len(steps) == 0 || jsonString(t, steps[len(steps)-1].(map[string]any), "recipe") != "inserter" {
			t.Fatalf("plan steps must end with the requested recipe: %v", steps)
		}

		refused := h.mutation(t, fmt.Sprintf(`mutation {
		  craft(input:{actor:{character:%q},recipe:"inserter",expand:true}) {
		    chain { complete queued { recipe } missing { name } queueSize }
		  }
		}`, character.id))
		refusedResult := jsonObject(t, jsonObject(t, jsonObject(t, refused, "data"), "craft"), "chain")
		if queued := jsonArray(t, refusedResult, "queued"); len(queued) != 0 {
			t.Fatalf("missing raws must queue nothing, got %v", queued)
		}
		if size := jsonNumber(t, refusedResult, "queueSize"); size != 0 {
			t.Fatalf("refused plan left queueSize=%v, want 0", size)
		}

		// Supply the raws: the plan becomes complete and performs bottom-up.
		h.grantItems(t, character.id, map[string]int{"iron-plate": 4, "copper-plate": 2})
		crafted := h.mutation(t, fmt.Sprintf(`mutation {
		  craft(input:{actor:{character:%q},recipe:"inserter",expand:true,wait:true,waitTimeoutSeconds:10}) {
		    chain { complete queued { recipe count } remaining { recipe } missing { name } queueSize waitTimedOut }
		  }
		}`, character.id))
		result := jsonObject(t, jsonObject(t, jsonObject(t, crafted, "data"), "craft"), "chain")
		if complete, _ := result["complete"].(bool); !complete {
			t.Fatalf("craftPlan did not finish: %v", result)
		}
		if timedOut, _ := result["waitTimedOut"].(bool); timedOut {
			t.Fatalf("craftPlan timed out: %v", result)
		}
		if remaining := jsonArray(t, result, "remaining"); len(remaining) != 0 {
			t.Fatalf("craftPlan left %v pending", remaining)
		}
		queued := jsonArray(t, result, "queued")
		if len(queued) < 4 {
			t.Fatalf("queued steps = %v, want cable, circuit, gear, and inserter crafts", queued)
		}

		inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
		items := jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items")
		if !hasItem(items, "inserter", 1) {
			t.Fatalf("hand-crafted inserter missing from inventory: %v", items)
		}
	})

	// craftPlan must not plan probabilistic recycling recipes as producers
	// (factorio-ai-player-c6j), must be resumable by repeating the same call
	// (-il4), craft must say why it refused (-cix), and actors expose
	// cheatMode (-zb6).
	t.Run("CraftPlanResumeAndRefusals", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.lua(t, `for _,recipe in pairs(game.forces.player.recipes) do recipe.enabled=true end
rcon.print("ok")`)

		planned := h.query(t, fmt.Sprintf(`{
		  craftPlan(input:{actor:{character:%q},recipe:"assembling-machine-2"}) {
		    complete steps { recipe } missing { name }
		  }
		}`, character.id))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "craftPlan")
		missingNames := map[string]bool{}
		for _, raw := range jsonArray(t, plan, "missing") {
			missingNames[raw.(map[string]any)["name"].(string)] = true
		}
		if !missingNames["steel-plate"] || missingNames["scrap"] {
			t.Fatalf("missing = %v, want steel-plate reported as missing and no scrap", missingNames)
		}
		for _, raw := range jsonArray(t, plan, "steps") {
			if recipe := raw.(map[string]any)["recipe"].(string); strings.Contains(recipe, "recycling") {
				t.Fatalf("plan uses recycling recipe %q as a producer", recipe)
			}
		}

		err := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation {
		  craft(input:{actor:{character:%q},recipe:"inserter",count:1}) { started }
		}`, character.id))
		if err == nil || !strings.Contains(err.Error(), "iron-plate") || !strings.Contains(err.Error(), "refused") {
			t.Fatalf("craft refusal = %v, want it to name the missing iron-plate", err)
		}

		cheat := h.query(t, fmt.Sprintf(`{ player(input:{character:%q}) { cheatMode } }`, character.id))
		if got := jsonObject(t, jsonObject(t, cheat, "data"), "player")["cheatMode"]; got != nil {
			t.Fatalf("detached character cheatMode = %v, want null (no player to read it from)", got)
		}

		// 20 gear crafts take ~10 s of game time; repeated 1 s UP_TO calls must
		// finish the batch exactly once instead of queueing 20 more each time.
		h.grantItems(t, character.id, map[string]int{"iron-plate": 200})
		for call := 0; ; call++ {
			if call > 40 {
				t.Fatal("craft expand UP_TO never completed")
			}
			resumed := h.mutation(t, fmt.Sprintf(`mutation {
			  craft(input:{actor:{character:%q},recipe:"iron-gear-wheel",count:20,mode:UP_TO,expand:true,wait:true,waitTimeoutSeconds:1}) {
			    chain { complete }
			  }
			}`, character.id))
			chain := jsonObject(t, jsonObject(t, jsonObject(t, resumed, "data"), "craft"), "chain")
			if done, _ := chain["complete"].(bool); done {
				break
			}
		}
		// A direct UP_TO against a covered target queues nothing.
		covered := h.mutation(t, fmt.Sprintf(`mutation {
		  craft(input:{actor:{character:%q},recipe:"iron-gear-wheel",count:20,mode:UP_TO}) { started }
		}`, character.id))
		if started := jsonNumber(t, jsonObject(t, jsonObject(t, covered, "data"), "craft"), "started"); started != 0 {
			t.Fatalf("UP_TO on a covered target started %v crafts, want 0", started)
		}
		inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
		items := jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items")
		if !hasItem(items, "iron-gear-wheel", 20) || !hasItem(items, "iron-plate", 160) {
			t.Fatalf("items = %v, want exactly 20 gears from 40 plates (no duplicated batch)", items)
		}
	})

	t.Run("StockCategoryFilter", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"iron-gear-wheel": 3, "wooden-chest": 1})

		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:6,y:0},item:"wooden-chest"}) { unitNumber }
		}`, character.id, integrationSurface))
		chest := jsonString(t, jsonObject(t, jsonObject(t, jsonObject(t, placed, "data"), "place")), "unitNumber")
		t.Cleanup(func() { h.removeEntity(t, character.id, "wooden-chest", chest) })

		transferred := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{actor:{character:%q},target:{surface:%q,entity:"wooden-chest",unitNumber:%q},inventory:CHEST,direction:TO_ENTITY,item:"iron-gear-wheel",count:2}) { moved }
		}`, character.id, integrationSurface, chest))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, transferred, "data"), "transfer"), "moved"); got != 2 {
			t.Fatalf("transferred %v gear wheels, want 2", got)
		}

		stored := h.query(t, fmt.Sprintf(`{
		  stock(input:{surface:%q,force:"player",item:"iron-gear-wheel",category:STORED,limit:100,totalsLimit:100,itemsLimit:100}) {
		    itemPageInfo { total }
		    totals { item category count }
		    locations { category inventoryKind stack { name count } }
		  }
		}`, integrationSurface))
		storedStock := jsonObject(t, jsonObject(t, stored, "data"), "stock")
		if total := jsonNumber(t, jsonObject(t, storedStock, "itemPageInfo"), "total"); total < 1 {
			t.Fatalf("stored item page total=%v, want at least 1", total)
		}
		for _, raw := range jsonArray(t, storedStock, "locations") {
			location := raw.(map[string]any)
			if category := jsonString(t, location, "category"); category != "stored" {
				t.Fatalf("stored filter returned category %q: %v", category, location)
			}
		}
		for _, raw := range jsonArray(t, storedStock, "totals") {
			total := raw.(map[string]any)
			if category := jsonString(t, total, "category"); category != "stored" {
				t.Fatalf("stored filter returned total category %q: %v", category, total)
			}
		}

		ground := h.query(t, fmt.Sprintf(`{
		  stock(input:{surface:%q,force:"player",item:"iron-gear-wheel",category:GROUND,limit:100,totalsLimit:100,itemsLimit:100}) {
		    itemPageInfo { total }
		    locations { category }
		  }
		}`, integrationSurface))
		groundStock := jsonObject(t, jsonObject(t, ground, "data"), "stock")
		if total := jsonNumber(t, jsonObject(t, groundStock, "itemPageInfo"), "total"); total != 0 {
			t.Fatalf("ground item page total=%v, want 0", total)
		}
	})

	t.Run("LampSettingsRoundTrip", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		before := h.entityCount(t, "small-lamp")
		h.grantItems(t, character.id, map[string]int{"small-lamp": 1})
		unit := h.placeLamp(t, character.id, 2, 0)
		t.Cleanup(func() {
			h.removeEntity(t, character.id, "small-lamp", unit)
			if after := h.entityCount(t, "small-lamp"); after != before {
				t.Errorf("small-lamp count after cleanup=%v, want %v", after, before)
			}
		})

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  setLampSettings(input:{actor:{character:%q},target:{surface:%q,entity:"small-lamp",unitNumber:%q},color:{r:0.25,g:0.5,b:0.75,a:1},alwaysOn:true,useColors:false,colorMode:COLOR_MAPPING}) {
		    tick lamp { color { r g b a } alwaysOn useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		settings := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setLampSettings"), "lamp")
		if !jsonBool(t, settings, "alwaysOn") || jsonBool(t, settings, "useColors") || jsonString(t, settings, "colorMode") != "COLOR_MAPPING" {
			t.Fatalf("unexpected post-write lamp settings: %v", settings)
		}
		color := jsonObject(t, settings, "color")
		for key, want := range map[string]float64{"r": 0.25, "g": 0.5, "b": 0.75, "a": 1} {
			if got := jsonNumber(t, color, key); math.Abs(got-want) > 0.01 {
				t.Fatalf("lamp color %s=%v, want %v; color=%v", key, got, want, color)
			}
		}

		inspection := h.inspectSettings(t, character.id, unit)
		lamp := jsonObject(t, inspection, "lamp")
		if !jsonBool(t, lamp, "alwaysOn") || jsonBool(t, lamp, "useColors") {
			t.Fatalf("inspection did not preserve lamp settings: %v", lamp)
		}
	})

	t.Run("InfinityContainerRoundTrip", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"infinity-chest": 1})
		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-6.5,y:-6.5},item:"infinity-chest"}) { unitNumber }
		}`, character.id, integrationSurface))
		unit := jsonString(t, jsonObject(t, jsonObject(t, placed, "data"), "place"), "unitNumber")
		t.Cleanup(func() { h.removeEntity(t, character.id, "infinity-chest", unit) })

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  configureInfinityContainer(input:{actor:{character:%q},target:{surface:%q,entity:"infinity-chest",unitNumber:%q},
		    filters:[{item:"electric-mining-drill",count:1000,mode:EXACTLY},{item:"iron-plate",quality:"normal",count:50}],
		    removeUnfilteredItems:true}) {
		    tick infinity { filters { index name quality count mode } removeUnfilteredItems }
		  }
		}`, character.id, integrationSurface, unit))
		settings := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "configureInfinityContainer"), "infinity")
		filters := jsonArray(t, settings, "filters")
		if len(filters) != 2 {
			t.Fatalf("infinity filters=%v, want 2", filters)
		}
		first := filters[0].(map[string]any)
		if jsonString(t, first, "name") != "electric-mining-drill" || jsonNumber(t, first, "count") != 1000 || jsonString(t, first, "mode") != "exactly" {
			t.Fatalf("first filter=%v", first)
		}
		second := filters[1].(map[string]any)
		if jsonString(t, second, "name") != "iron-plate" || jsonNumber(t, second, "count") != 50 || jsonString(t, second, "mode") != "at-least" {
			t.Fatalf("second filter=%v", second)
		}
		if !jsonBool(t, settings, "removeUnfilteredItems") {
			t.Fatalf("removeUnfilteredItems not applied: %v", settings)
		}

		withdraw := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{actor:{character:%q},target:{surface:%q,entity:"infinity-chest",unitNumber:%q},
		    inventory:CHEST,direction:TO_ACTOR,item:"electric-mining-drill",count:5}) {
		    moved item inventory direction reason
		  }
		}`, character.id, integrationSurface, unit))
		moved := jsonObject(t, jsonObject(t, withdraw, "data"), "transfer")
		if jsonNumber(t, moved, "moved") != 5 || jsonString(t, moved, "item") != "electric-mining-drill" {
			t.Fatalf("infinity container did not supply drills: %v", moved)
		}
		inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
		drills := 0.0
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items") {
			item := raw.(map[string]any)
			if jsonString(t, item, "name") == "electric-mining-drill" {
				drills += jsonNumber(t, item, "count")
			}
		}
		if drills < 5 {
			t.Fatalf("actor drills=%v after withdrawing from the infinity container, want at least 5", drills)
		}

		// The source must replenish: after a withdrawal, the container's own
		// inventory should return to the configured EXACTLY count.
		time.Sleep(200 * time.Millisecond)
		readback := h.query(t, fmt.Sprintf(`{
		  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"infinity-chest",unitNumber:%q}}) {
		    inventories { chest { name count } }
		  }
		}`, character.id, integrationSurface, unit))
		chestDrills := 0.0
		chestInventory := jsonObject(t, jsonObject(t, jsonObject(t, readback, "data"), "inspect"), "inventories")
		for _, raw := range jsonArray(t, chestInventory, "chest") {
			item := raw.(map[string]any)
			if jsonString(t, item, "name") == "electric-mining-drill" {
				chestDrills += jsonNumber(t, item, "count")
			}
		}
		if chestDrills != 1000 {
			t.Fatalf("infinity container drills=%v after withdrawing 5, want 1000 replenished", chestDrills)
		}

		cleared := h.mutation(t, fmt.Sprintf(`mutation {
		  configureInfinityContainer(input:{actor:{character:%q},target:{surface:%q,entity:"infinity-chest",unitNumber:%q},
		    filters:[],removeUnfilteredItems:false}) {
		    infinity { filters { index name quality count mode } removeUnfilteredItems }
		  }
		}`, character.id, integrationSurface, unit))
		empty := jsonObject(t, jsonObject(t, jsonObject(t, cleared, "data"), "configureInfinityContainer"), "infinity")
		if len(jsonArray(t, empty, "filters")) != 0 || jsonBool(t, empty, "removeUnfilteredItems") {
			t.Fatalf("empty filter list did not clear the container: %v", empty)
		}
	})

	t.Run("CircuitBehaviorCompatibility", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"small-lamp": 2})
		unit := h.placeLamp(t, character.id, -3, 0)
		t.Cleanup(func() { h.removeEntity(t, character.id, "small-lamp", unit) })

		placedCombinator := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-5,y:0},item:"small-lamp"}) { unitNumber }
		}`, character.id, integrationSurface))
		combinatorUnit := jsonString(t, jsonObject(t, jsonObject(t, jsonObject(t, placedCombinator, "data"), "place")), "unitNumber")
		t.Cleanup(func() { h.removeEntity(t, character.id, "small-lamp", combinatorUnit) })

		wired := h.mutation(t, fmt.Sprintf(`mutation {
		  connectWire(input:{actor:{character:%q},
		    from:{target:{surface:%q,entity:"small-lamp",unitNumber:%q},connector:"circuit_red"},
		    to:{target:{surface:%q,entity:"small-lamp",unitNumber:%q},connector:"circuit_red"},
		    color:RED}) { connected color }
		}`, character.id, integrationSurface, combinatorUnit, integrationSurface, unit))
		wire := jsonObject(t, jsonObject(t, wired, "data"), "connectWire")
		if !jsonBool(t, wire, "connected") || jsonString(t, wire, "color") != "RED" {
			t.Fatalf("connect wire = %v, want connected RED", wire)
		}
		network := h.query(t, fmt.Sprintf(`{
		  circuitNetwork(input:{target:{surface:%q,entity:"small-lamp",unitNumber:%q},color:RED}) {
		    color connected connector networkId
		  }
		}`, integrationSurface, unit))
		networkResult := jsonObject(t, jsonObject(t, network, "data"), "circuitNetwork")
		if !jsonBool(t, networkResult, "connected") || jsonString(t, networkResult, "color") != "RED" || jsonString(t, networkResult, "connector") != "circuit_red" {
			t.Fatalf("circuitNetwork = %v, want connected RED on the circuit_red connector", networkResult)
		}

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  setCircuitBehavior(input:{actor:{character:%q},target:{surface:%q,entity:"small-lamp",unitNumber:%q},useColors:true,colorMode:COMPONENTS}) {
		    circuit { useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		circuit := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setCircuitBehavior"), "circuit")
		if !jsonBool(t, circuit, "useColors") || jsonString(t, circuit, "colorMode") != "COMPONENTS" {
			t.Fatalf("unexpected post-write circuit behavior: %v", circuit)
		}
		readback := jsonObject(t, h.inspectSettings(t, character.id, unit), "circuit")
		if !jsonBool(t, readback, "useColors") || jsonString(t, readback, "colorMode") != "COMPONENTS" {
			t.Fatalf("inspection did not preserve circuit behavior: %v", readback)
		}
	})

	t.Run("NavigationMod", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		destinationX := character.x + 4
		result := h.mutation(t, fmt.Sprintf(`mutation {
		  navigateStart(input:{actor:{character:%q},position:{x:%g,y:%g},tolerance:0.75,timeoutTicks:600,wait:true,waitTimeoutSeconds:15}) {
		    id character surface status { category rawName } reason position { x y }
		  }
		}`, character.id, destinationX, character.y))
		job := jsonObject(t, jsonObject(t, result, "data"), "navigateStart")
		status := jsonObject(t, job, "status")
		if category := jsonString(t, status, "category"); category != "SUCCEEDED" {
			t.Fatalf("navigation status=%v, want SUCCEEDED/ARRIVED; job=%v", status, job)
		}
		if raw := jsonString(t, status, "rawName"); raw != "ARRIVED" {
			t.Fatalf("navigation raw status=%q, want ARRIVED; job=%v", raw, job)
		}
		if surface := jsonString(t, job, "surface"); surface != integrationSurface {
			t.Fatalf("navigation surface=%q, want %q", surface, integrationSurface)
		}
	})

	t.Run("DeconstructionInstantSpillsContents", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 1, "iron-plate": 6})

		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:12,y:0},item:"wooden-chest"}) {
		    unitNumber position { x y }
		  }
		}`, character.id, integrationSurface))
		place := jsonObject(t, jsonObject(t, placed, "data"), "place")
		unit := jsonString(t, place, "unitNumber")
		position := jsonObject(t, place, "position")
		x, y := jsonNumber(t, position, "x"), jsonNumber(t, position, "y")

		filled := h.mutation(t, fmt.Sprintf(`mutation {
		  transfer(input:{actor:{character:%q},target:{surface:%q,entity:"wooden-chest",unitNumber:%q},inventory:CHEST,direction:TO_ENTITY,item:"iron-plate",count:5}) {
		    moved
		  }
		}`, character.id, integrationSurface, unit))
		if moved := jsonNumber(t, jsonObject(t, jsonObject(t, filled, "data"), "transfer"), "moved"); moved != 5 {
			t.Fatalf("transfer moved=%v, want 5", moved)
		}

		area := fmt.Sprintf(`{leftTopX:%g,leftTopY:%g,rightBottomX:%g,rightBottomY:%g}`, x-1, y-1, x+1, y+1)
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,type="item-entity"}) do e.destroy() end
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="wooden-chest"}) do e.destroy() end
end
rcon.print("ok")`)
		})

		// No actor: instant mode cannot mine the chest, so it must empty it onto
		// the ground before destroying it instead of voiding the contents.
		result := h.mutation(t, fmt.Sprintf(`mutation {
		  deconstructArea(input:{surface:%q,force:"player",instant:true,area:%s,names:["wooden-chest"]}) {
		    removed skipped items { name count }
		  }
		}`, integrationSurface, area))
		deconstruction := jsonObject(t, jsonObject(t, result, "data"), "deconstructArea")
		if removed := jsonNumber(t, deconstruction, "removed"); removed != 1 {
			t.Fatalf("instant deconstruct removed=%v, want 1; result=%v", removed, deconstruction)
		}
		items := jsonArray(t, deconstruction, "items")
		if !hasItem(items, "iron-plate", 5) {
			t.Fatalf("chest contents were not recovered; items=%v", items)
		}
		if !hasItem(items, "wooden-chest", 1) {
			t.Fatalf("placement item was not recovered; items=%v", items)
		}

		remainder := h.query(t, fmt.Sprintf(`{
		  entities(input:{surface:%q,type:"item-entity",area:%s,limit:100}) { pageInfo { total } }
		}`, integrationSurface, area))
		total := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, remainder, "data"), "entities"), "pageInfo"), "total")
		if total < 1 {
			t.Fatalf("no spilled items on the ground after instant deconstruct; total=%v", total)
		}
	})

	t.Run("PlacementRestrictionToRoutedMutations", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 2, "pipe": 2})
		const area = `{leftTopX:18,leftTopY:18,rightBottomX:24,rightBottomY:24}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid then e.destroy() end end
end
rcon.print("ok")`)
		})

		beltErr := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:20.5,y:20.5},item:"transport-belt"}) { name }
		}`, character.id, integrationSurface))
		if beltErr == nil || !strings.Contains(beltErr.Error(), "placeBelts") {
			t.Fatalf("place belt error = %v, want placeBelts hint", beltErr)
		}
		after := h.query(t, fmt.Sprintf(`{ entities(input:{surface:%q,area:%s,name:"transport-belt",limit:100}) { pageInfo { total } } }`, integrationSurface, area))
		if count := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "entities"), "pageInfo"), "total"); count != 0 {
			t.Fatalf("refused place belt left %v belts", count)
		}
		placedBelt := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,kind:BELT,from:{x:20.5,y:20.5},to:{x:20.5,y:20.5},direction:0}) { placed }
		}`, character.id, integrationSurface))
		if jsonNumber(t, jsonObject(t, jsonObject(t, placedBelt, "data"), "connectEntities"), "placed") != 1 {
			t.Fatalf("connectEntities one belt tile: %v", placedBelt)
		}

		pipeErr := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation {
		  placeMany(input:{actor:{character:%q},surface:%q,item:"pipe",placements:[{position:{x:22.5,y:20.5}}]}) { placed }
		}`, character.id, integrationSurface))
		if pipeErr == nil || !strings.Contains(pipeErr.Error(), "placePipe") {
			t.Fatalf("placeMany pipe error = %v, want placePipe hint", pipeErr)
		}
		placedPipe := h.mutation(t, fmt.Sprintf(`mutation {
		  placePipe(input:{actor:{character:%q},surface:%q,from:{x:22.5,y:20.5},to:{x:22.5,y:20.5},direction:0}) { placed }
		}`, character.id, integrationSurface))
		if jsonNumber(t, jsonObject(t, jsonObject(t, placedPipe, "data"), "placePipe"), "placed") != 1 {
			t.Fatalf("placePipe one tile: %v", placedPipe)
		}
	})

	t.Run("DeconstructionInstantCountsAndItems", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 3})
		const beltX, beltY = 6.0, 10.0
		h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,kind:BELT,from:{x:%g,y:%g},to:{x:%g,y:%g},item:"transport-belt"}) { placed }
		}`, character.id, integrationSurface, beltX, beltY, beltX+2, beltY))
		area := fmt.Sprintf(`{leftTopX:%g,leftTopY:%g,rightBottomX:%g,rightBottomY:%g}`, beltX-1, beltY-1, beltX+3, beltY+1)
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,type="transport-belt"}) do e.destroy() end
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,type="item-entity"}) do e.destroy() end
end
rcon.print("ok")`)
		})

		before := h.query(t, fmt.Sprintf(`{
		  entities(input:{surface:%q,type:"transport-belt",area:%s,limit:100}) { pageInfo { total } }
		}`, integrationSurface, area))
		placed := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, before, "data"), "entities"), "pageInfo"), "total")
		if placed != 3 {
			t.Fatalf("placed belts=%v, want 3", placed)
		}

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  deconstructArea(input:{surface:%q,force:"player",instant:true,area:%s,names:["transport-belt"]}) {
		    removed skipped orders items { name count }
		  }
		}`, integrationSurface, area))
		deconstruction := jsonObject(t, jsonObject(t, result, "data"), "deconstructArea")
		if removed := jsonNumber(t, deconstruction, "removed"); removed != placed {
			t.Fatalf("instant deconstruct removed=%v, want %v; result=%v", removed, placed, deconstruction)
		}
		items := jsonArray(t, deconstruction, "items")
		if !hasItem(items, "transport-belt", placed) {
			t.Fatalf("mined belts were not recovered; items=%v", items)
		}

		after := h.query(t, fmt.Sprintf(`{
		  entities(input:{surface:%q,type:"transport-belt",area:%s,limit:100}) { pageInfo { total } }
		}`, integrationSurface, area))
		if remaining := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "entities"), "pageInfo"), "total"); remaining != 0 {
			t.Fatalf("belts remaining after instant deconstruct=%v, want 0", remaining)
		}
	})

	t.Run("UndergroundNeighbour", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"underground-belt": 2})
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area={{14,2},{22,6}},type="underground-belt"}) do e.destroy() end
end
rcon.print("ok")`)
		})

		first := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:16,y:4},direction:4,item:"underground-belt",undergroundType:INPUT}) { tick unitNumber direction }
		}`, character.id, integrationSurface))
		firstResult := jsonObject(t, jsonObject(t, first, "data"), "place")
		firstUnit := jsonString(t, firstResult, "unitNumber")
		if got := jsonNumber(t, firstResult, "direction"); got != 4 {
			t.Fatalf("place direction=%v, want 4", got)
		}
		second := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:19,y:4},direction:4,item:"underground-belt",undergroundType:OUTPUT}) { tick unitNumber direction }
		}`, character.id, integrationSurface))
		secondResult := jsonObject(t, jsonObject(t, second, "data"), "place")
		secondUnit := jsonString(t, secondResult, "unitNumber")
		if got := jsonNumber(t, secondResult, "direction"); got != 4 {
			t.Fatalf("place direction=%v, want 4", got)
		}

		inspection := h.query(t, fmt.Sprintf(`{
		  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"underground-belt",unitNumber:%q}}) {
		    undergroundNeighbour { name unitNumber position { x y } }
		  }
		}`, character.id, integrationSurface, firstUnit))
		neighbour := jsonObject(t, jsonObject(t, jsonObject(t, inspection, "data"), "inspect"), "undergroundNeighbour")
		if got := jsonString(t, neighbour, "unitNumber"); got != secondUnit {
			t.Fatalf("underground neighbour unit=%q, want %q (first=%s second=%s neighbour=%v)", got, secondUnit, firstUnit, secondUnit, neighbour)
		}
		if name := jsonString(t, neighbour, "name"); name != "underground-belt" {
			t.Fatalf("underground neighbour name=%q", name)
		}
	})

	t.Run("ConnectionPlanAndApply", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 4})
		const area = `{leftTopX:-15,leftTopY:-15,rightBottomX:-10,rightBottomY:-13}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="transport-belt"}) do e.destroy() end
end
rcon.print("ok")`)
		})

		planned := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-14.5,y:-14.5},to:{x:-11.5,y:-14.5},kind:BELT}) {
		    possible needed bend
		    steps { position { x y } direction existing }
		    conflicts { kind blockers message }
		  }
		}`, integrationSurface))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "connectionPlan")
		if !jsonBool(t, plan, "possible") || jsonNumber(t, plan, "needed") != 4 || jsonString(t, plan, "bend") != "HORIZONTAL_FIRST" {
			t.Fatalf("clear belt plan: %v", plan)
		}
		steps := jsonArray(t, plan, "steps")
		if len(steps) != 4 {
			t.Fatalf("belt plan steps=%d, want 4; %v", len(steps), steps)
		}
		for _, raw := range steps {
			step := raw.(map[string]any)
			if jsonNumber(t, step, "direction") != 4 || jsonBool(t, step, "existing") {
				t.Fatalf("unexpected belt step: %v", step)
			}
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:-14.5,y:-14.5},to:{x:-11.5,y:-14.5},kind:BELT}) {
		    possible placed skipped failed
		    entities { name position { x y } unitNumber }
		    conflicts { kind }
		    errors { index code message }
		  }
		}`, character.id, integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != 4 || jsonNumber(t, result, "skipped") != 0 || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("clear belt apply: %v", result)
		}
		if got := len(jsonArray(t, result, "entities")); got != 4 {
			t.Fatalf("placed entities=%d, want 4", got)
		}

		replanned := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-14.5,y:-14.5},to:{x:-11.5,y:-14.5},kind:BELT}) {
		    possible needed
		    steps { existing }
		  }
		}`, integrationSurface))
		second := jsonObject(t, jsonObject(t, replanned, "data"), "connectionPlan")
		if !jsonBool(t, second, "possible") || jsonNumber(t, second, "needed") != 0 {
			t.Fatalf("completed route must need nothing: %v", second)
		}
		for _, raw := range jsonArray(t, second, "steps") {
			if !jsonBool(t, raw.(map[string]any), "existing") {
				t.Fatalf("completed route step not marked existing: %v", raw)
			}
		}

		again := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:-14.5,y:-14.5},to:{x:-11.5,y:-14.5},kind:BELT}) {
		    placed skipped failed
		  }
		}`, character.id, integrationSurface))
		third := jsonObject(t, jsonObject(t, again, "data"), "connectEntities")
		if jsonNumber(t, third, "placed") != 0 || jsonNumber(t, third, "skipped") != 4 {
			t.Fatalf("existing run must be skipped: %v", third)
		}
	})

	t.Run("ConnectionPlanCollision", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"small-lamp": 1, "transport-belt": 4})
		const area = `{leftTopX:-11,leftTopY:-15,rightBottomX:-6,rightBottomY:-13}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="transport-belt"}) do e.destroy() end
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="small-lamp"}) do e.destroy() end
end
rcon.print("ok")`)
		})
		h.placeLamp(t, character.id, -8.5, -14.5)

		planned := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-10.5,y:-14.5},to:{x:-7.5,y:-14.5},kind:BELT}) {
		    possible needed
		    conflicts { kind blockers position { x y } tile }
		  }
		}`, integrationSurface))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "connectionPlan")
		if jsonBool(t, plan, "possible") {
			t.Fatalf("plan over a lamp must not be possible: %v", plan)
		}
		conflicts := jsonArray(t, plan, "conflicts")
		if len(conflicts) == 0 {
			t.Fatalf("no conflicts reported: %v", plan)
		}
		conflict := conflicts[0].(map[string]any)
		if kind := jsonString(t, conflict, "kind"); kind != "COLLISION" {
			t.Fatalf("conflict kind=%q, want COLLISION", kind)
		}
		blockers := jsonArray(t, conflict, "blockers")
		if len(blockers) == 0 || blockers[0].(string) != "small-lamp" {
			t.Fatalf("conflict blockers=%v, want small-lamp", blockers)
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:-10.5,y:-14.5},to:{x:-7.5,y:-14.5},kind:BELT}) {
		    possible placed failed entities { name } conflicts { kind }
		  }
		}`, character.id, integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != 0 || len(jsonArray(t, result, "entities")) != 0 {
			t.Fatalf("conflicting run must place nothing: %v", result)
		}
		after := h.query(t, fmt.Sprintf(`{
		  entities(input:{surface:%q,type:"transport-belt",area:%s,limit:100}) { pageInfo { total } entities { name position { x y } } }
		}`, integrationSurface, area))
		if got := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "entities"), "pageInfo"), "total"); got != 0 {
			t.Fatalf("transport-belt count=%v in the refused area, want 0; entities=%v", got, jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "entities"), "entities"))
		}
	})

	t.Run("ConnectionPipeAdjacency", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"pipe": 5})
		const area = `{leftTopX:-5,leftTopY:-16,rightBottomX:0,rightBottomY:-13}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="pipe"}) do e.destroy() end
end
rcon.print("ok")`)
		})
		h.mutation(t, fmt.Sprintf(`mutation {
		  placePipe(input:{actor:{character:%q},surface:%q,from:{x:-3.5,y:-14.5},to:{x:-3.5,y:-14.5},direction:0}) { placed }
		}`, character.id, integrationSurface))

		exact := fmt.Sprintf(`{ connectionPlan(input:{surface:%q,force:"player",from:{x:-4.5,y:-15.5},to:{x:-1.5,y:-15.5},kind:PIPE}) {
		    possible needed
		    conflicts { kind position { x y } message }
		  } }`, integrationSurface)
		planned := h.query(t, exact)
		plan := jsonObject(t, jsonObject(t, planned, "data"), "connectionPlan")
		if jsonBool(t, plan, "possible") {
			t.Fatalf("pipe route beside an existing pipe must be refused: %v", plan)
		}
		conflicts := jsonArray(t, plan, "conflicts")
		if len(conflicts) == 0 || jsonString(t, conflicts[0].(map[string]any), "kind") != "FLUID_ADJACENCY" {
			t.Fatalf("pipe adjacency conflicts=%v", conflicts)
		}

		allowed := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-4.5,y:-15.5},to:{x:-1.5,y:-15.5},kind:PIPE,allowFluidAdjacency:true}) {
		    possible needed conflicts { kind }
		  }
		}`, integrationSurface))
		allowedPlan := jsonObject(t, jsonObject(t, allowed, "data"), "connectionPlan")
		if !jsonBool(t, allowedPlan, "possible") || jsonNumber(t, allowedPlan, "needed") != 4 {
			t.Fatalf("allowed pipe plan: %v", allowedPlan)
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:-4.5,y:-15.5},to:{x:-1.5,y:-15.5},kind:PIPE,allowFluidAdjacency:true}) {
		    possible placed failed skipped
		  }
		}`, character.id, integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != 4 || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("allowed pipe apply: %v", result)
		}
	})

	t.Run("ConnectionPlanBend", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		planned := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-14.5,y:-9.5},to:{x:-11.5,y:-6.5},kind:BELT}) {
		    possible bend
		    steps { position { x y } direction }
		  }
		}`, integrationSurface))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "connectionPlan")
		if !jsonBool(t, plan, "possible") || jsonString(t, plan, "bend") != "HORIZONTAL_FIRST" {
			t.Fatalf("bend plan: %v", plan)
		}
		steps := jsonArray(t, plan, "steps")
		if len(steps) != 7 {
			t.Fatalf("bend steps=%d, want 7; %v", len(steps), steps)
		}
		if dir := jsonNumber(t, steps[0].(map[string]any), "direction"); dir != 4 {
			t.Fatalf("first step direction=%v, want 4 (east)", dir)
		}
		if dir := jsonNumber(t, steps[3].(map[string]any), "direction"); dir != 8 {
			t.Fatalf("corner step direction=%v, want 8 (south)", dir)
		}
	})

	t.Run("ConnectionPowerPoleChain", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"medium-electric-pole": 3})
		const area = `{leftTopX:-15,leftTopY:12,rightBottomX:-9,rightBottomY:15}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="medium-electric-pole"}) do e.destroy() end
end
rcon.print("ok")`)
		})
		planned := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:-14.5,y:13.5},to:{x:-9.5,y:13.5},kind:POWER_POLE_CHAIN}) {
		    possible item needed bent: bend
		    steps { position { x y } direction }
		  }
		}`, integrationSurface))
		plan := jsonObject(t, jsonObject(t, planned, "data"), "connectionPlan")
		if !jsonBool(t, plan, "possible") || jsonString(t, plan, "item") != "medium-electric-pole" {
			t.Fatalf("pole plan: %v", plan)
		}
		needed := jsonNumber(t, plan, "needed")
		if needed < 2 || needed > 3 {
			t.Fatalf("pole plan needed=%v, want 2..3; %v", needed, plan)
		}
		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:-14.5,y:13.5},to:{x:-9.5,y:13.5},kind:POWER_POLE_CHAIN}) {
		    placed failed skipped
		  }
		}`, character.id, integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if jsonNumber(t, result, "placed") != needed || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("pole apply: %v, plan needed=%v", result, needed)
		}
	})

	t.Run("BuildCellOccupancyAndBuildConnection", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"assembling-machine-1": 2, "transport-belt": 12})
		const area = `{leftTopX:-40,leftTopY:-40,rightBottomX:-20,rightBottomY:-20}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do
    if e.name=="transport-belt" or e.name=="assembling-machine-1" then e.destroy() end
  end
end
rcon.print("ok")`)
		})
		count := func(name string) float64 {
			result := h.query(t, fmt.Sprintf(`{ entities(input:{surface:%q,area:%s,name:%q,limit:1000}) { pageInfo { total } } }`, integrationSurface, area, name))
			return jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "entities"), "pageInfo"), "total")
		}
		beltsBefore := count("transport-belt")
		machinesBefore := count("assembling-machine-1")
		cell := func(dryRun bool) string {
			return fmt.Sprintf(`mutation {
			  buildCell(input:{actor:{character:%q},surface:%q,force:"player",dryRun:%t,entries:[
			    {ref:"gear",place:{item:"assembling-machine-1",position:{x:-30.5,y:-30.5}}},
			    {ref:"feed",connection:{from:{x:-26.5,y:-30.5},to:{x:-22.5,y:-30.5},kind:BELT}},
			    {ref:"recipe",recipe:{ref:"gear",recipe:"iron-gear-wheel"}}
			  ]}) {
			    dryRun completed placed failed skipped
			    steps { index ref kind ok applied unitNumber placed failed message }
			  }
			}`, character.id, integrationSurface, dryRun)
		}

		dry := jsonObject(t, jsonObject(t, h.mutation(t, cell(true)), "data"), "buildCell")
		if !jsonBool(t, dry, "dryRun") || jsonBool(t, dry, "completed") || jsonNumber(t, dry, "placed") != 0 {
			t.Fatalf("dry run: %v", dry)
		}
		if count("transport-belt") != beltsBefore || count("assembling-machine-1") != machinesBefore {
			t.Fatalf("dry run changed the world: %v", dry)
		}

		built := jsonObject(t, jsonObject(t, h.mutation(t, cell(false)), "data"), "buildCell")
		if !jsonBool(t, built, "completed") || jsonNumber(t, built, "failed") != 0 || len(jsonArray(t, built, "steps")) != 3 {
			t.Fatalf("cell apply: %v", built)
		}
		for _, raw := range jsonArray(t, built, "steps") {
			step := raw.(map[string]any)
			if !jsonBool(t, step, "ok") || !jsonBool(t, step, "applied") {
				t.Fatalf("cell step not applied: %v", step)
			}
		}
		if got := count("transport-belt") - beltsBefore; got != 5 {
			t.Fatalf("belts placed = %v, want 5", got)
		}
		recipe := h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local m=surface.find_entities_filtered{name="assembling-machine-1",position={-30.5,-30.5},radius=0.5}[1]
rcon.print(m and m.get_recipe() and m.get_recipe().name or "none")`)
		if !strings.Contains(recipe, "iron-gear-wheel") {
			t.Fatalf("machine recipe = %q, want iron-gear-wheel", recipe)
		}

		occupancy := jsonObject(t, jsonObject(t, h.query(t, fmt.Sprintf(`{
		  occupancy(input:{surface:%q,force:"player",area:%s,limit:100}) {
		    pageInfo { total }
		    entries { name direction footprint { leftTopX leftTopY rightBottomX rightBottomY } }
		  }
		}`, integrationSurface, area)), "data"), "occupancy")
		var machineFound bool
		belts := 0
		for _, raw := range jsonArray(t, occupancy, "entries") {
			entry := raw.(map[string]any)
			footprint := jsonObject(t, entry, "footprint")
			switch jsonString(t, entry, "name") {
			case "assembling-machine-1":
				machineFound = true
				if jsonNumber(t, footprint, "leftTopX") != -32 || jsonNumber(t, footprint, "rightBottomX") != -29 ||
					jsonNumber(t, footprint, "leftTopY") != -32 || jsonNumber(t, footprint, "rightBottomY") != -29 {
					t.Fatalf("machine footprint = %v, want the 3x3 tiles -32..-29", footprint)
				}
			case "transport-belt":
				belts++
				if jsonNumber(t, footprint, "rightBottomX")-jsonNumber(t, footprint, "leftTopX") != 1 {
					t.Fatalf("belt footprint = %v, want one tile", footprint)
				}
			}
		}
		if !machineFound || belts != 5 {
			t.Fatalf("occupancy machine=%v belts=%d, want machine and 5 belts: %v", machineFound, belts, occupancy)
		}

		connection := func(dryRun bool) string {
			return fmt.Sprintf(`mutation {
			  buildConnection(dryRun:%t,input:{actor:{character:%q},surface:%q,force:"player",from:{x:-26.5,y:-26.5},to:{x:-22.5,y:-26.5},kind:BELT}) {
			    dryRun
			    plan { possible needed stopReason conflicts { kind message } }
			    result { possible placed failed actor { player character } }
			  }
			}`, dryRun, character.id, integrationSurface)
		}
		planned := jsonObject(t, jsonObject(t, h.mutation(t, connection(true)), "data"), "buildConnection")
		if !jsonBool(t, planned, "dryRun") || planned["result"] != nil || jsonNumber(t, jsonObject(t, planned, "plan"), "needed") != 5 {
			t.Fatalf("buildConnection dry run: %v", planned)
		}
		if got := count("transport-belt") - beltsBefore; got != 5 {
			t.Fatalf("buildConnection dry run changed belts: %v", got)
		}
		placed := jsonObject(t, jsonObject(t, h.mutation(t, connection(false)), "data"), "buildConnection")
		if placed["result"] == nil {
			t.Fatalf("buildConnection returned no result: %v", placed)
		}
		result := jsonObject(t, placed, "result")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "placed") != 5 || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("buildConnection apply: %v", placed)
		}
		// result.actor is ActorIdentity!: selecting it must not hit the null check,
		// and it must name the actor the mutation was called with.
		actor := result["actor"]
		if actor == nil {
			t.Fatalf("buildConnection result.actor is null: %v", result)
		}
		if got := jsonString(t, actor.(map[string]any), "character"); got != character.id {
			t.Fatalf("buildConnection result.actor.character = %q, want %q", got, character.id)
		}
	})

	t.Run("WaitUntilTicks", func(t *testing.T) {
		current := h.status(t)
		started := time.Now()
		result := h.mutation(t, `mutation {
		  waitUntil(input:{timeoutSeconds:5,pollSeconds:0.05,condition:{ticks:20}}) {
		    tick condition satisfied timedOut polls waitedSeconds
		    last { tick count detail }
		  }
		}`)
		wait := jsonObject(t, jsonObject(t, result, "data"), "waitUntil")
		if !jsonBool(t, wait, "satisfied") || jsonBool(t, wait, "timedOut") {
			t.Fatalf("ticks wait: %v", wait)
		}
		if condition := jsonString(t, wait, "condition"); condition != "TICKS" {
			t.Fatalf("condition=%q, want TICKS", condition)
		}
		if tick := jsonNumber(t, wait, "tick"); tick < current+20 {
			t.Fatalf("satisfied at tick=%v before target=%v", tick, current+20)
		}
		if count := jsonNumber(t, jsonObject(t, wait, "last"), "count"); count < 20 {
			t.Fatalf("elapsed ticks=%v before target=20", count)
		}
		if polls := jsonNumber(t, wait, "polls"); polls < 1 {
			t.Fatalf("polls=%v", polls)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("ticks wait took %v", elapsed)
		}
	})

	t.Run("WaitUntilTimeout", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		started := time.Now()
		result := h.mutation(t, fmt.Sprintf(`mutation {
		  waitUntil(input:{timeoutSeconds:1,pollSeconds:0.2,condition:{inventory:{actor:{character:%q},item:"iron-plate",count:999}}}) {
		    condition satisfied timedOut polls waitedSeconds last { count detail }
		  }
		}`, character.id))
		wait := jsonObject(t, jsonObject(t, result, "data"), "waitUntil")
		if jsonBool(t, wait, "satisfied") || !jsonBool(t, wait, "timedOut") {
			t.Fatalf("inventory wait: %v", wait)
		}
		if polls := jsonNumber(t, wait, "polls"); polls < 2 {
			t.Fatalf("polls=%v, want at least 2", polls)
		}
		last := jsonObject(t, wait, "last")
		if count := jsonNumber(t, last, "count"); count != 0 {
			t.Fatalf("observed count=%v, want 0", count)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("timeout overshot: %v", elapsed)
		}
	})

	t.Run("WaitUntilMachineStatus", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"stone-furnace": 1})
		const area = `{leftTopX:-15,leftTopY:-3,rightBottomX:-13,rightBottomY:-1}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="stone-furnace"}) do e.destroy() end
end
rcon.print("ok")`)
		})
		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-14.5,y:-2.5},item:"stone-furnace"}) { unitNumber }
		}`, character.id, integrationSurface))
		unit := jsonString(t, jsonObject(t, jsonObject(t, placed, "data"), "place"), "unitNumber")

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  waitUntil(input:{timeoutSeconds:5,pollSeconds:0.1,condition:{machineStatus:{target:{surface:%q,entity:"stone-furnace",unitNumber:%q},statuses:["no_fuel"]}}}) {
		    condition satisfied timedOut last { status detail }
		  }
		}`, integrationSurface, unit))
		wait := jsonObject(t, jsonObject(t, result, "data"), "waitUntil")
		if !jsonBool(t, wait, "satisfied") || jsonBool(t, wait, "timedOut") {
			t.Fatalf("machine status wait: %v", wait)
		}
		if condition := jsonString(t, wait, "condition"); condition != "MACHINE_STATUS" {
			t.Fatalf("condition=%q, want MACHINE_STATUS", condition)
		}
		if got := jsonString(t, jsonObject(t, wait, "last"), "status"); got != "no_fuel" {
			t.Fatalf("observed status=%q, want no_fuel", got)
		}
	})

	t.Run("PrototypeQuery", func(t *testing.T) {
		plate := h.query(t, `{
		  prototype(name:"iron-plate") {
		    name item { name stackSize stackable placeResult } entity { name }
		  }
		}`)
		plateProto := jsonObject(t, jsonObject(t, plate, "data"), "prototype")
		item := jsonObject(t, plateProto, "item")
		if stack := jsonNumber(t, item, "stackSize"); stack != 100 {
			t.Fatalf("iron-plate stackSize=%v, want 100", stack)
		}
		if placeResult := item["placeResult"]; placeResult != nil {
			t.Fatalf("iron-plate placeResult=%v, want null", placeResult)
		}
		if entity := plateProto["entity"]; entity != nil {
			t.Fatalf("iron-plate entity=%v, want null", entity)
		}

		furnace := h.query(t, `{
		  prototype(name:"stone-furnace") {
		    entity { name type tileWidth tileHeight maxHealth mineable fluidBoxCount
		      collisionBox { leftTop { x y } rightBottom { x y } }
		      craftingCategories }
		  }
		}`)
		furnaceProto := jsonObject(t, jsonObject(t, furnace, "data"), "prototype")
		furnaceEntity := jsonObject(t, furnaceProto, "entity")
		if width := jsonNumber(t, furnaceEntity, "tileWidth"); width != 2 {
			t.Fatalf("stone-furnace tileWidth=%v, want 2", width)
		}
		if height := jsonNumber(t, furnaceEntity, "tileHeight"); height != 2 {
			t.Fatalf("stone-furnace tileHeight=%v, want 2", height)
		}
		if kind := jsonString(t, furnaceEntity, "type"); kind != "furnace" {
			t.Fatalf("stone-furnace type=%q, want furnace", kind)
		}
		if health := jsonNumber(t, furnaceEntity, "maxHealth"); health != 200 {
			t.Fatalf("stone-furnace maxHealth=%v, want 200", health)
		}
		if mineable := jsonBool(t, furnaceEntity, "mineable"); !mineable {
			t.Fatalf("stone-furnace must be mineable")
		}
		box := jsonObject(t, furnaceEntity, "collisionBox")
		if left := jsonNumber(t, jsonObject(t, box, "leftTop"), "x"); left >= 0 {
			t.Fatalf("stone-furnace collision left=%v, want negative", left)
		}
		if right := jsonNumber(t, jsonObject(t, box, "rightBottom"), "x"); right <= 0 {
			t.Fatalf("stone-furnace collision right=%v, want positive", right)
		}
		categories := jsonArray(t, furnaceEntity, "craftingCategories")
		if len(categories) == 0 {
			t.Fatalf("stone-furnace craftingCategories is empty")
		}

		pipe := h.query(t, `{ prototype(name:"pipe") { entity { fluidBoxCount } } }`)
		pipeEntity := jsonObject(t, jsonObject(t, jsonObject(t, pipe, "data"), "prototype"), "entity")
		if boxes := jsonNumber(t, pipeEntity, "fluidBoxCount"); boxes != 1 {
			t.Fatalf("pipe fluidBoxCount=%v, want 1", boxes)
		}

		pole := h.query(t, `{ prototype(name:"medium-electric-pole") { entity { wireReach supplyArea } } }`)
		poleEntity := jsonObject(t, jsonObject(t, jsonObject(t, pole, "data"), "prototype"), "entity")
		if reach := jsonNumber(t, poleEntity, "wireReach"); reach < 7 {
			t.Fatalf("medium-electric-pole wireReach=%v, want at least 7", reach)
		}

		inserter := h.query(t, `{ prototype(name:"inserter") { entity { name type inserterPickup { x y } inserterDrop { x y } } } }`)
		inserterEntity := jsonObject(t, jsonObject(t, jsonObject(t, inserter, "data"), "prototype"), "entity")
		for _, field := range []string{"inserterPickup", "inserterDrop"} {
			position := jsonObject(t, inserterEntity, field)
			jsonNumber(t, position, "x")
			jsonNumber(t, position, "y")
		}
	})

	t.Run("PlaceNextTo", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"stone-furnace": 1, "wooden-chest": 1})
		const area = `{leftTopX:-13,leftTopY:-6,rightBottomX:-7,rightBottomY:-2}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="stone-furnace"}) do e.destroy() end
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="wooden-chest"}) do e.destroy() end
end
rcon.print("ok")`)
		})

		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-10,y:-4},item:"stone-furnace"}) {
		    unitNumber position { x y }
		  }
		}`, character.id, integrationSurface))
		furnace := jsonObject(t, jsonObject(t, placed, "data"), "place")
		furnaceUnit := jsonString(t, furnace, "unitNumber")
		furnacePosition := jsonObject(t, furnace, "position")
		furnaceX := jsonNumber(t, furnacePosition, "x")
		furnaceY := jsonNumber(t, furnacePosition, "y")

		prototype := h.query(t, `{ prototype(name:"stone-furnace") { entity { collisionBox { rightBottom { x } } } }
		  chest: prototype(name:"wooden-chest") { entity { collisionBox { rightBottom { x } } } } }`)
		right := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, prototype, "data"), "prototype"), "entity"), "collisionBox"), "rightBottom"), "x")
		chestHalf := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, prototype, "data"), "chest"), "entity"), "collisionBox"), "rightBottom")), "x")

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  placeNextTo(input:{actor:{character:%q},surface:%q,item:"wooden-chest",
		    relativeTo:{surface:%q,entity:"stone-furnace",unitNumber:%q},side:EAST,gap:1}) {
		    name unitNumber position { x y } relativeTo side gap
		  }
		}`, character.id, integrationSurface, integrationSurface, furnaceUnit))
		placedNextTo := jsonObject(t, jsonObject(t, result, "data"), "placeNextTo")
		position := jsonObject(t, placedNextTo, "position")
		chestX := jsonNumber(t, position, "x")
		minimumX := furnaceX + right + 1
		if chestX-chestHalf < minimumX-0.001 {
			t.Fatalf("chest left edge=%v, want at least %v (chest x=%v half=%v furnace x=%v right=%v)", chestX-chestHalf, minimumX, chestX, chestHalf, furnaceX, right)
		}
		if chestX <= furnaceX {
			t.Fatalf("chest x=%v, want east of furnace x=%v", chestX, furnaceX)
		}
		if got := jsonNumber(t, position, "y"); math.Abs(got-furnaceY) > 1 {
			t.Fatalf("chest y=%v, want within 1 tile of furnace y=%v", got, furnaceY)
		}
		if got := jsonString(t, placedNextTo, "relativeTo"); got != furnaceUnit {
			t.Fatalf("relativeTo=%q, want %q", got, furnaceUnit)
		}
		if side := jsonString(t, placedNextTo, "side"); side != "EAST" {
			t.Fatalf("side=%q, want EAST", side)
		}
		if got := jsonNumber(t, placedNextTo, "gap"); got != 1 {
			t.Fatalf("gap=%v, want 1", got)
		}
		after := h.query(t, fmt.Sprintf(`{
		  entities(input:{surface:%q,type:"container",area:%s,limit:10}) {
		    pageInfo { total }
		    entities { name position { x y } }
		  }
		}`, integrationSurface, area))
		containers := jsonObject(t, jsonObject(t, jsonObject(t, after, "data"), "entities"))
		if total := jsonNumber(t, jsonObject(t, containers, "pageInfo"), "total"); total != 1 {
			t.Fatalf("wooden-chest count=%v, want 1; %v", total, containers)
		}
	})

	t.Run("WorldPlacementBlockers", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"small-lamp": 1})
		const area = `{leftTopX:-14,leftTopY:5,rightBottomX:-11,rightBottomY:8}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="small-lamp"}) do e.destroy() end
end
rcon.print("ok")`)
		})
		h.placeLamp(t, character.id, -12.5, 6.5)

		blocked := h.query(t, fmt.Sprintf(`{
		  worldPlacement(input:{surface:%q,force:"player",prototype:"small-lamp",position:{x:-12.5,y:6.5}}) {
		    canPlace blockers terrain tile position { x y }
		  }
		}`, integrationSurface))
		blockedResult := jsonObject(t, jsonObject(t, blocked, "data"), "worldPlacement")
		if jsonBool(t, blockedResult, "canPlace") {
			t.Fatalf("occupied tile must be refused: %v", blockedResult)
		}
		blockers := jsonArray(t, blockedResult, "blockers")
		if len(blockers) == 0 || blockers[0].(string) != "small-lamp" {
			t.Fatalf("blockers=%v, want small-lamp", blockers)
		}
		if tile := jsonString(t, blockedResult, "tile"); tile == "" {
			t.Fatalf("refused tile reported no ground tile: %v", blockedResult)
		}

		clear := h.query(t, fmt.Sprintf(`{
		  worldPlacement(input:{surface:%q,force:"player",prototype:"small-lamp",position:{x:-10.5,y:6.5}}) {
		    canPlace blockers terrain tile
		  }
		}`, integrationSurface))
		clearResult := jsonObject(t, jsonObject(t, clear, "data"), "worldPlacement")
		if !jsonBool(t, clearResult, "canPlace") {
			t.Fatalf("clear tile must be placeable: %v", clearResult)
		}
		if blockers := jsonArray(t, clearResult, "blockers"); len(blockers) != 0 {
			t.Fatalf("clear tile blockers=%v, want none", blockers)
		}
		if terrain := jsonBool(t, clearResult, "terrain"); terrain {
			t.Fatalf("clear tile reported terrain blockage: %v", clearResult)
		}
	})

	t.Run("MapArea", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 1})
		const area = `{leftTopX:-16,leftTopY:7,rightBottomX:-12,rightBottomY:10}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="wooden-chest"}) do e.destroy() end
  surface.set_tiles{{name="lab-dark-1",position={x=-13,y=9}}}
end
rcon.print("ok")`)
		})
		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-14.5,y:8.5},item:"wooden-chest"}) { unitNumber position { x y } }
		}`, character.id, integrationSurface))
		chest := jsonObject(t, jsonObject(t, placed, "data"), "place")
		chestUnit := jsonString(t, chest, "unitNumber")
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
surface.set_tiles{{name="water",position={x=-13,y=9}}}
rcon.print("ok")`)

		mapped := h.query(t, fmt.Sprintf(`{
		  mapArea(input:{surface:%q,force:"player",area:%s,maxRows:16,maxColumns:16}) {
		    cellSize columns rows truncated legend { char name type unitNumber count }
		  }
		}`, integrationSurface, area))
		areaResult := jsonObject(t, jsonObject(t, mapped, "data"), "mapArea")
		if size := jsonNumber(t, areaResult, "cellSize"); size != 1 {
			t.Fatalf("cellSize=%v, want 1", size)
		}
		if columns := jsonNumber(t, areaResult, "columns"); columns != 4 {
			t.Fatalf("columns=%v, want 4", columns)
		}
		rows := jsonArray(t, areaResult, "rows")
		if len(rows) != 3 {
			t.Fatalf("rows=%d, want 3; %v", len(rows), rows)
		}
		for i, raw := range rows {
			if row := raw.(string); len(row) != 4 {
				t.Fatalf("row %d=%q, want 4 columns", i, row)
			}
		}

		legend := jsonArray(t, areaResult, "legend")
		chestChar := ""
		waterChar := ""
		for _, raw := range legend {
			entry := raw.(map[string]any)
			switch jsonString(t, entry, "name") {
			case "wooden-chest":
				chestChar = jsonString(t, entry, "char")
				if got := jsonString(t, entry, "unitNumber"); got != chestUnit {
					t.Fatalf("chest legend unit=%q, want %q", got, chestUnit)
				}
				if count := jsonNumber(t, entry, "count"); count != 1 {
					t.Fatalf("chest legend count=%v, want 1", count)
				}
			case "water":
				waterChar = jsonString(t, entry, "char")
				if count := jsonNumber(t, entry, "count"); count != 1 {
					t.Fatalf("water legend count=%v, want 1", count)
				}
			}
		}
		if chestChar == "" {
			t.Fatalf("no wooden-chest legend entry: %v", legend)
		}
		if waterChar == "" {
			t.Fatalf("no water legend entry: %v", legend)
		}
		// The chest sits at (-14.5, 8.5): column 1, row 1. The water tile sits
		// at the top-left tile of column 3, row 2.
		if got := string(rows[1].(string)[1]); got != chestChar {
			t.Fatalf("row 1=%q, cell 2=%q want chest char %q", rows[1], got, chestChar)
		}
		if got := string(rows[2].(string)[3]); got != waterChar {
			t.Fatalf("row 2=%q, cell 4=%q want water char %q", rows[2], got, waterChar)
		}

		factory := h.query(t, fmt.Sprintf(`{
		  mapArea(input:{surface:%q,force:"player",area:%s,maxRows:16,maxColumns:16,layer:FACTORY}) {
		    legend { name }
		  }
		}`, integrationSurface, area))
		factoryLegend := jsonArray(t, jsonObject(t, jsonObject(t, factory, "data"), "mapArea"), "legend")
		if !legendHasName(factoryLegend, "wooden-chest") {
			t.Fatalf("FACTORY layer dropped the chest: %v", factoryLegend)
		}
		resources := h.query(t, fmt.Sprintf(`{
		  mapArea(input:{surface:%q,force:"player",area:%s,maxRows:16,maxColumns:16,layer:RESOURCES}) {
		    legend { name }
		  }
		}`, integrationSurface, area))
		resourceLegend := jsonArray(t, jsonObject(t, jsonObject(t, resources, "data"), "mapArea"), "legend")
		if legendHasName(resourceLegend, "wooden-chest") {
			t.Fatalf("RESOURCES layer must omit the chest: %v", resourceLegend)
		}
	})

	t.Run("FactoryBriefing", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"stone-furnace": 1, "wooden-chest": 2})
		const area = `{leftTopX:18,leftTopY:-26,rightBottomX:30,rightBottomY:-18}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		placed := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:22,y:-22},item:"stone-furnace"}) { unitNumber position { x y } }
		}`, character.id, integrationSurface))
		furnace := jsonObject(t, jsonObject(t, placed, "data"), "place")
		furnaceUnit := jsonString(t, furnace, "unitNumber")
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local ghost=surface.create_entity{name="entity-ghost",inner_name="wooden-chest",position={x=27.5,y=-22.5},force="player"}
assert(ghost and ghost.valid)
rcon.print("ok")`)

		result := h.query(t, fmt.Sprintf(`{
		  factoryBriefing(input:{
		    surface:%q, force:"player", area:%s, attentionLimit:20,
		    watchItems:[{item:"wooden-chest",minimum:5}], actor:{character:%q}
		  }) {
		    surface force window
		    productionTick machineTick powerTick researchTick safetyTick constructionTick stockTick
		    machines { type noFuel total }
		    attention { kind severity entity unitNumber position { x y } count message }
		    attentionTotal
		    attentionCounts { kind count }
		    construction { ghosts ghostsBuildable deconstructionOrders upgradeOrders }
		    stockouts { item minimum have shortfall actorHas }
		    warnings
		  }
		}`, integrationSurface, area, character.id))
		briefing := jsonObject(t, jsonObject(t, result, "data"), "factoryBriefing")

		ticks := []string{"productionTick", "machineTick", "powerTick", "researchTick", "safetyTick", "constructionTick", "stockTick"}
		previous := 0.0
		for _, name := range ticks {
			tick := jsonNumber(t, briefing, name)
			if tick <= 0 || tick < previous {
				t.Fatalf("%s=%v after previous=%v: sections are read in order, so ticks must be positive and non-decreasing; %v", name, tick, previous, briefing)
			}
			previous = tick
		}

		var noFuel map[string]any
		var ghost map[string]any
		for _, raw := range jsonArray(t, briefing, "attention") {
			item := raw.(map[string]any)
			switch jsonString(t, item, "kind") {
			case "NO_FUEL":
				noFuel = item
			case "GHOST_BACKLOG":
				ghost = item
			}
		}
		if noFuel == nil {
			t.Fatalf("no NO_FUEL attention for the placed furnace: %v", briefing["attention"])
		}
		if entity := jsonString(t, noFuel, "entity"); entity != "stone-furnace" {
			t.Fatalf("NO_FUEL entity=%q, want stone-furnace", entity)
		}
		if severity := jsonString(t, noFuel, "severity"); severity != "CRITICAL" {
			t.Fatalf("NO_FUEL severity=%q, want CRITICAL", severity)
		}
		if unit := jsonString(t, noFuel, "unitNumber"); unit != furnaceUnit {
			t.Fatalf("NO_FUEL unit=%q, want %q", unit, furnaceUnit)
		}
		if ghost == nil {
			t.Fatalf("no GHOST_BACKLOG attention: %v", briefing["attention"])
		}
		if count := jsonNumber(t, ghost, "count"); count != 1 {
			t.Fatalf("ghost count=%v, want 1", count)
		}
		construction := jsonObject(t, briefing, "construction")
		if ghosts := jsonNumber(t, construction, "ghosts"); ghosts != 1 {
			t.Fatalf("construction ghosts=%v, want 1", ghosts)
		}

		furnaceRow := false
		for _, raw := range jsonArray(t, briefing, "machines") {
			summary := raw.(map[string]any)
			if jsonString(t, summary, "type") == "furnace" {
				furnaceRow = true
				if noFuelCount := jsonNumber(t, summary, "noFuel"); noFuelCount != 1 {
					t.Fatalf("furnace summary noFuel=%v, want 1", noFuelCount)
				}
			}
		}
		if !furnaceRow {
			t.Fatalf("no furnace machine summary: %v", briefing["machines"])
		}

		stockouts := jsonArray(t, briefing, "stockouts")
		if len(stockouts) != 1 {
			t.Fatalf("stockouts=%v, want the wooden-chest shortage", stockouts)
		}
		stockout := stockouts[0].(map[string]any)
		if item := jsonString(t, stockout, "item"); item != "wooden-chest" {
			t.Fatalf("stockout item=%q", item)
		}
		if shortfall := jsonNumber(t, stockout, "shortfall"); shortfall <= 0 {
			t.Fatalf("stockout shortfall=%v", shortfall)
		}
		if held := jsonNumber(t, stockout, "actorHas"); held != 2 {
			t.Fatalf("stockout actorHas=%v, want the granted 2 wooden-chest", held)
		}
		lowStock := false
		for _, raw := range jsonArray(t, briefing, "attentionCounts") {
			entry := raw.(map[string]any)
			if jsonString(t, entry, "kind") == "LOW_STOCK" && jsonNumber(t, entry, "count") == 1 {
				lowStock = true
			}
		}
		if !lowStock {
			t.Fatalf("no LOW_STOCK count: %v", briefing["attentionCounts"])
		}
	})

	t.Run("PowerConsumersFilters", func(t *testing.T) {
		const area = `{leftTopX:18,leftTopY:-16,rightBottomX:30,rightBottomY:-8}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
surface.freeze_daytime=true
surface.daytime=0
local function make(name,x,y) local e=surface.create_entity{name=name,position={x=x,y=y},force="player"}; assert(e and e.valid,name); return e end
make("solar-panel",22.5,-12.5)
make("medium-electric-pole",24.5,-12.5)
make("small-lamp",26.5,-12.5)
make("small-lamp",19.5,-9.5)
rcon.print("ok")`)

		all := h.query(t, fmt.Sprintf(`{
		  powerConsumers(input:{surface:%q,force:"player",area:%s,limit:100}) {
		    pageInfo { total }
		    powerEntities { name type position { x y } status networkId connectedToProducingNetwork }
		  }
		}`, integrationSurface, area))
		var powered, unpowered map[string]any
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, all, "data"), "powerConsumers"), "powerEntities") {
			entity := raw.(map[string]any)
			if jsonString(t, entity, "name") != "small-lamp" {
				continue
			}
			if jsonNumber(t, jsonObject(t, entity, "position"), "x") == 26.5 {
				powered = entity
			} else {
				unpowered = entity
			}
		}
		if powered == nil || unpowered == nil {
			t.Fatalf("both lamps must appear unfiltered: %v", all)
		}
		if !jsonBool(t, powered, "connectedToProducingNetwork") {
			t.Fatalf("powered lamp must report connectedToProducingNetwork: %v", powered)
		}
		if jsonBool(t, unpowered, "connectedToProducingNetwork") {
			t.Fatalf("unpowered lamp must report not connected: %v", unpowered)
		}
		poweredNetwork := jsonString(t, powered, "networkId")
		poweredStatus := jsonString(t, powered, "status")
		unpoweredStatus := jsonString(t, unpowered, "status")

		issues := h.query(t, fmt.Sprintf(`{
		  powerConsumers(input:{surface:%q,force:"player",area:%s,limit:100,onlyIssues:true}) {
		    pageInfo { total }
		    powerEntities { name position { x y } status connectedToProducingNetwork }
		  }
		}`, integrationSurface, area))
		issuePage := jsonObject(t, jsonObject(t, issues, "data"), "powerConsumers")
		issueEntities := jsonArray(t, issuePage, "powerEntities")
		if len(issueEntities) != 1 {
			t.Fatalf("onlyIssues returned %d entities, want only the unpowered lamp: %v", len(issueEntities), issueEntities)
		}
		if x := jsonNumber(t, jsonObject(t, issueEntities[0].(map[string]any), "position"), "x"); x != 19.5 {
			t.Fatalf("onlyIssues entity x=%v, want the unpowered lamp at 19.5", x)
		}
		if total := jsonNumber(t, jsonObject(t, issuePage, "pageInfo"), "total"); total != 1 {
			t.Fatalf("onlyIssues total=%v, want 1 (filter applies before pagination)", total)
		}

		byNetwork := h.query(t, fmt.Sprintf(`{
		  powerConsumers(input:{surface:%q,force:"player",area:%s,limit:100,networkId:%q}) {
		    powerEntities { name position { x y } networkId }
		  }
		}`, integrationSurface, area, poweredNetwork))
		sawPowered := false
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, byNetwork, "data"), "powerConsumers"), "powerEntities") {
			entity := raw.(map[string]any)
			if got := jsonString(t, entity, "networkId"); got != poweredNetwork {
				t.Fatalf("networkId filter leaked network %q (want %q): %v", got, poweredNetwork, entity)
			}
			if jsonString(t, entity, "name") == "small-lamp" {
				if x := jsonNumber(t, jsonObject(t, entity, "position"), "x"); x != 26.5 {
					t.Fatalf("network filter returned lamp at x=%v, want 26.5", x)
				}
				sawPowered = true
			}
		}
		if !sawPowered {
			t.Fatalf("network %q lost the powered lamp", poweredNetwork)
		}

		byStatus := h.query(t, fmt.Sprintf(`{
		  powerConsumers(input:{surface:%q,force:"player",area:%s,limit:100,status:%q}) {
		    powerEntities { name position { x y } status }
		  }
		}`, integrationSurface, area, unpoweredStatus))
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, byStatus, "data"), "powerConsumers"), "powerEntities") {
			entity := raw.(map[string]any)
			if got := jsonString(t, entity, "status"); got != unpoweredStatus {
				t.Fatalf("status filter leaked %q (want %q): %v", got, unpoweredStatus, entity)
			}
			if jsonString(t, entity, "name") == "small-lamp" && poweredStatus != unpoweredStatus {
				if x := jsonNumber(t, jsonObject(t, entity, "position"), "x"); x != 19.5 {
					t.Fatalf("status filter returned the powered lamp: %v", entity)
				}
			}
		}

		notConnected := h.query(t, fmt.Sprintf(`{
		  powerConsumers(input:{surface:%q,force:"player",area:%s,limit:100,connectedToProducingNetwork:false}) {
		    powerEntities { name position { x y } connectedToProducingNetwork }
		  }
		}`, integrationSurface, area))
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, notConnected, "data"), "powerConsumers"), "powerEntities") {
			entity := raw.(map[string]any)
			if jsonBool(t, entity, "connectedToProducingNetwork") {
				t.Fatalf("connectedToProducingNetwork:false returned a connected entity: %v", entity)
			}
		}

		networks := h.query(t, fmt.Sprintf(`{
		  electricNetworks(input:{surface:%q,force:"player",area:%s,limit:100,networkId:%q}) {
		    pageInfo { total }
		    networks { id }
		  }
		}`, integrationSurface, area, poweredNetwork))
		networkList := jsonArray(t, jsonObject(t, jsonObject(t, networks, "data"), "electricNetworks"), "networks")
		if len(networkList) != 1 || jsonString(t, networkList[0].(map[string]any), "id") != poweredNetwork {
			t.Fatalf("electricNetworks networkId filter: %v", networkList)
		}
	})

	// Sandbox and creative saves are powered by energy interfaces, so their
	// rated output must count as capacity; otherwise maxProductionWatts is 0 and
	// loadRatio is null even though the network is fully supplied.
	t.Run("ElectricNetworksEnergyInterfaceCapacity", func(t *testing.T) {
		const area = `{leftTopX:34,leftTopY:-16,rightBottomX:46,rightBottomY:-8}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
surface.freeze_daytime=true
surface.daytime=0
local function make(name,x,y) local e=surface.create_entity{name=name,position={x=x,y=y},force="player"}; assert(e and e.valid,name); return e end
make("electric-energy-interface",38.5,-12.5)
make("medium-electric-pole",40.5,-12.5)
make("small-lamp",42.5,-12.5)
rcon.print("ok")`)

		result := h.query(t, fmt.Sprintf(`{
		  electricNetworks(input:{surface:%q,force:"player",area:%s,limit:100}) {
		    networks { id maxProductionWatts consumptionWatts loadRatio lowPowerConsumers }
		  }
		}`, integrationSurface, area))
		list := jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "electricNetworks"), "networks")
		if len(list) != 1 {
			t.Fatalf("want one network for the energy interface, got %v", list)
		}
		network := list[0].(map[string]any)
		if capacity := jsonNumber(t, network, "maxProductionWatts"); capacity < 4e11 {
			t.Fatalf("energy interface capacity missing from maxProductionWatts: %v", network)
		}
		loadRatio, present := network["loadRatio"]
		if !present || loadRatio == nil {
			t.Fatalf("loadRatio must be a ratio, not null, when an energy interface supplies the network: %v", network)
		}
		if loadRatio.(float64) != 0 {
			t.Fatalf("idle energy interface network loadRatio=%v, want 0", loadRatio)
		}
		if low := jsonNumber(t, network, "lowPowerConsumers"); low != 0 {
			t.Fatalf("lowPowerConsumers=%v, want 0", low)
		}
	})

	t.Run("ConnectionAStarBeltAroundObstacle", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 40})
		const area = `{leftTopX:5,leftTopY:16,rightBottomX:22,rightBottomY:28}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
for _,y in ipairs({21.5,22.5,23.5}) do
  local tree=surface.create_entity{name="tree-01",position={x=12.5,y=y},force="neutral"}
  assert(tree and tree.valid,"tree")
end
rcon.print("ok")`)

		plan := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:8.5,y:22.5},to:{x:16.5,y:22.5},kind:BELT,routing:ASTAR,searchMargin:4}) {
		    possible routing stopReason needed undergroundNeeded cost expanded
		    steps { position { x y } direction existing undergroundType cost }
		    conflicts { kind }
		  }
		}`, integrationSurface))
		planned := jsonObject(t, jsonObject(t, plan, "data"), "connectionPlan")
		if !jsonBool(t, planned, "possible") || jsonString(t, planned, "stopReason") != "FOUND" || jsonString(t, planned, "routing") != "ASTAR" {
			t.Fatalf("detour plan: %v", planned)
		}
		steps := jsonArray(t, planned, "steps")
		if len(steps) <= 9 {
			t.Fatalf("a detour around three trees must be longer than the straight 9 tiles, got %d", len(steps))
		}
		if undergroundNeeded := jsonNumber(t, planned, "undergroundNeeded"); undergroundNeeded != 0 {
			t.Fatalf("undergroundNeeded=%v without allowUnderground", undergroundNeeded)
		}
		type tile struct{ x, y float64 }
		planTiles := map[tile]float64{}
		for _, raw := range steps {
			step := raw.(map[string]any)
			position := jsonObject(t, step, "position")
			x, y := jsonNumber(t, position, "x"), jsonNumber(t, position, "y")
			if x == 12.5 && y >= 21.5 && y <= 23.5 {
				t.Fatalf("route crosses a tree at (%v,%v)", x, y)
			}
			planTiles[tile{x, y}] = jsonNumber(t, step, "direction")
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",from:{x:8.5,y:22.5},to:{x:16.5,y:22.5},kind:BELT,routing:ASTAR,searchMargin:4}) {
		    possible routing stopReason placed skipped failed
		    entities { name position { x y } unitNumber }
		    errors { code message }
		  }
		}`, character.id, integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("apply: %v", result)
		}
		if got, want := jsonNumber(t, result, "placed"), jsonNumber(t, planned, "needed"); got != want {
			t.Fatalf("placed=%v, plan needed=%v: plan and apply disagree", got, want)
		}
		for _, raw := range jsonArray(t, result, "entities") {
			position := jsonObject(t, raw.(map[string]any), "position")
			key := tile{jsonNumber(t, position, "x"), jsonNumber(t, position, "y")}
			if _, ok := planTiles[key]; !ok {
				t.Fatalf("placed a belt off the plan at %v", key)
			}
		}

		stretch := h.query(t, fmt.Sprintf(`{
		  beltStretch(target:{surface:%q,entity:"transport-belt",position:{x:8.5,y:22.5}}) {
		    truncated
		    nodes { name position { x y } direction }
		    ends { kind }
		  }
		}`, integrationSurface))
		walk := jsonObject(t, jsonObject(t, stretch, "data"), "beltStretch")
		nodes := jsonArray(t, walk, "nodes")
		if len(nodes) != len(steps) {
			t.Fatalf("belt stretch has %d nodes, plan has %d steps: the placed belts are not one connected line", len(nodes), len(steps))
		}
		first := jsonObject(t, nodes[0].(map[string]any), "position")
		lastNode := jsonObject(t, nodes[len(nodes)-1].(map[string]any), "position")
		if jsonNumber(t, first, "x") != 8.5 || jsonNumber(t, lastNode, "x") != 16.5 || jsonNumber(t, lastNode, "y") != 22.5 {
			t.Fatalf("stretch endpoints: first=%v last=%v", first, lastNode)
		}
	})

	t.Run("ConnectionAStarUndergroundBelt", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 20, "underground-belt": 2})
		const area = `{leftTopX:5,leftTopY:16,rightBottomX:22,rightBottomY:28}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
for y=19.5,24.5,1 do
  local wall=surface.create_entity{name="stone-wall",position={x=12.5,y=y},force="player"}
  assert(wall and wall.valid,"wall")
end
rcon.print("ok")`)

		const route = `from:{x:9.5,y:21.5},to:{x:15.5,y:21.5},kind:BELT,routing:ASTAR,searchMargin:2`
		blocked := h.query(t, fmt.Sprintf(`{ connectionPlan(input:{surface:%q,force:"player",%s}) { possible stopReason conflicts { kind } } }`, integrationSurface, route))
		noPath := jsonObject(t, jsonObject(t, blocked, "data"), "connectionPlan")
		if jsonBool(t, noPath, "possible") || jsonString(t, noPath, "stopReason") != "NO_PATH" {
			t.Fatalf("wall without tunnels must be NO_PATH: %v", noPath)
		}
		if conflicts := jsonArray(t, noPath, "conflicts"); len(conflicts) != 1 || jsonString(t, conflicts[0].(map[string]any), "kind") != "NO_PATH" {
			t.Fatalf("NO_PATH conflict: %v", conflicts)
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",%s,allowUnderground:true}) {
		    possible stopReason placed failed
		    entities { name position { x y } unitNumber }
		    errors { code message }
		  }
		}`, character.id, integrationSurface, route))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("tunnel apply: %v", result)
		}
		var halves []map[string]any
		for _, raw := range jsonArray(t, result, "entities") {
			entity := raw.(map[string]any)
			if jsonString(t, entity, "name") == "underground-belt" {
				halves = append(halves, entity)
			}
		}
		if len(halves) != 2 {
			t.Fatalf("underground halves placed=%d, want 2: %v", len(halves), result)
		}
		first := halves[0]
		inspection := h.query(t, fmt.Sprintf(`{
		  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"underground-belt",unitNumber:%q}}) {
		    undergroundNeighbour { unitNumber }
		  }
		}`, character.id, integrationSurface, jsonString(t, first, "unitNumber")))
		neighbour := jsonObject(t, jsonObject(t, jsonObject(t, inspection, "data"), "inspect"), "undergroundNeighbour")
		if got := jsonString(t, neighbour, "unitNumber"); got != jsonString(t, halves[1], "unitNumber") {
			t.Fatalf("underground halves are not paired: neighbour=%v halves=%v", neighbour, halves)
		}

		stretch := h.query(t, fmt.Sprintf(`{
		  beltStretch(target:{surface:%q,entity:"transport-belt",position:{x:9.5,y:21.5}}) {
		    nodes { name position { x y } }
		  }
		}`, integrationSurface))
		nodes := jsonArray(t, jsonObject(t, jsonObject(t, stretch, "data"), "beltStretch"), "nodes")
		lastNode := jsonObject(t, nodes[len(nodes)-1].(map[string]any), "position")
		if jsonNumber(t, lastNode, "x") != 15.5 || jsonNumber(t, lastNode, "y") != 21.5 {
			t.Fatalf("tunnel run must reach the goal: last=%v nodes=%d", lastNode, len(nodes))
		}
	})

	t.Run("ConnectionAStarPipeToGround", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"pipe": 10, "pipe-to-ground": 2})
		const area = `{leftTopX:5,leftTopY:16,rightBottomX:22,rightBottomY:28}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
for y=19.5,24.5,1 do
  local pipe=surface.create_entity{name="pipe",position={x=12.5,y=y},force="player"}
  assert(pipe and pipe.valid,"pipe")
end
rcon.print("ok")`)

		const route = `from:{x:9.5,y:21.5},to:{x:15.5,y:21.5},kind:PIPE,routing:ASTAR,searchMargin:2`
		touching := h.query(t, fmt.Sprintf(`{ connectionPlan(input:{surface:%q,force:"player",%s}) { possible stopReason } }`, integrationSurface, route))
		if refused := jsonObject(t, jsonObject(t, touching, "data"), "connectionPlan"); jsonBool(t, refused, "possible") {
			t.Fatalf("a surface pipe beside a foreign network must be refused: %v", refused)
		}

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  connectEntities(input:{actor:{character:%q},surface:%q,force:"player",%s,allowUnderground:true}) {
		    possible placed failed
		    entities { name position { x y } unitNumber }
		    errors { code message }
		  }
		}`, character.id, integrationSurface, route))
		result := jsonObject(t, jsonObject(t, applied, "data"), "connectEntities")
		if !jsonBool(t, result, "possible") || jsonNumber(t, result, "failed") != 0 {
			t.Fatalf("pipe-to-ground apply: %v", result)
		}
		var halves []string
		for _, raw := range jsonArray(t, result, "entities") {
			entity := raw.(map[string]any)
			if jsonString(t, entity, "name") == "pipe-to-ground" {
				halves = append(halves, jsonString(t, entity, "unitNumber"))
			}
		}
		if len(halves) != 2 {
			t.Fatalf("pipe-to-ground halves=%d, want 2: %v", len(halves), result)
		}

		// The two halves must join each other underground and must not connect
		// to the foreign pipe line they pass beneath.
		out := h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local entrance,exit
for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="pipe-to-ground"}) do
  if e.unit_number==`+halves[0]+` then entrance=e elseif e.unit_number==`+halves[1]+` then exit=e end
end
assert(entrance and exit,"halves")
local joined=false
for _,c in pairs(entrance.fluidbox.get_pipe_connections(1)) do
  if c.connection_type=="underground" and c.target and c.target.owner and c.target.owner.unit_number==exit.unit_number then joined=true end
end
local foreign=0
for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`,name="pipe"}) do
  if e.position.x==12.5 then
    for _,n in pairs(e.neighbours[1] or {}) do
      if n.name=="pipe-to-ground" then foreign=foreign+1 end
    end
  end
end
rcon.print(helpers.table_to_json({joined=joined,foreign=foreign}))`)
		var check struct {
			Joined  bool `json:"joined"`
			Foreign int  `json:"foreign"`
		}
		if err := json.Unmarshal([]byte(out), &check); err != nil {
			t.Fatalf("decode connectivity check %q: %v", out, err)
		}
		if !check.Joined {
			t.Fatalf("pipe-to-ground halves are not joined underground: %s", out)
		}
		if check.Foreign != 0 {
			t.Fatalf("the foreign pipe line connected to the new run: %s", out)
		}
	})

	t.Run("ConnectionAStarSearchLimits", func(t *testing.T) {
		limited := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:9.5,y:21.5},to:{x:15.5,y:21.5},kind:BELT,routing:ASTAR,searchMargin:2,maxExpandedTiles:1}) {
		    possible stopReason expanded conflicts { kind message }
		  }
		}`, integrationSurface))
		plan := jsonObject(t, jsonObject(t, limited, "data"), "connectionPlan")
		if jsonBool(t, plan, "possible") || jsonString(t, plan, "stopReason") != "EXPANSION_LIMIT" || jsonNumber(t, plan, "expanded") != 1 {
			t.Fatalf("expansion limit: %v", plan)
		}
		conflicts := jsonArray(t, plan, "conflicts")
		if len(conflicts) != 1 || jsonString(t, conflicts[0].(map[string]any), "kind") != "EXPANSION_LIMIT" {
			t.Fatalf("expansion conflict: %v", conflicts)
		}

		avoided := h.query(t, fmt.Sprintf(`{
		  connectionPlan(input:{surface:%q,force:"player",from:{x:9.5,y:21.5},to:{x:15.5,y:21.5},kind:BELT,routing:ASTAR,searchMargin:0,
		    avoidAreas:[{leftTopX:11,leftTopY:20,rightBottomX:14,rightBottomY:23}]}) {
		    possible stopReason
		  }
		}`, integrationSurface))
		if refused := jsonObject(t, jsonObject(t, avoided, "data"), "connectionPlan"); jsonBool(t, refused, "possible") || jsonString(t, refused, "stopReason") != "NO_PATH" {
			t.Fatalf("an avoided column across the whole region must be NO_PATH: %v", refused)
		}
	})

	t.Run("MapAreaOutOfMapIsNotWater", func(t *testing.T) {
		mapped := h.query(t, fmt.Sprintf(`{
		  mapArea(input:{surface:%q,force:"player",area:{leftTopX:28,leftTopY:-31,rightBottomX:36,rightBottomY:-29},maxRows:16,maxColumns:16}) {
		    rows legend { char name }
		  }
		}`, integrationSurface))
		areaResult := jsonObject(t, jsonObject(t, mapped, "data"), "mapArea")
		if legendHasName(jsonArray(t, areaResult, "legend"), "water") {
			t.Fatalf("tiles past the map edge are out-of-map, not water: %v", areaResult)
		}
	})

	t.Run("NearestPlacement", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 1})
		const area = `{leftTopX:-33,leftTopY:17,rightBottomX:-18,rightBottomY:31}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
  surface.set_tiles{{name="lab-dark-1",position={x=-28,y=21}}}
end
rcon.print("ok")`)
		})
		h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:-25.5,y:25.5},item:"wooden-chest"}) { unitNumber }
		}`, character.id, integrationSurface))

		nearest := func(extra string) map[string]any {
			t.Helper()
			result := h.query(t, fmt.Sprintf(`{
			  nearestPlacement(input:{surface:%q,force:"player",prototype:"stone-furnace",origin:{x:-25.5,y:25.5},%s}) {
			    found position { x y } distance blockers terrain tile expanded
			  }
			}`, integrationSurface, extra))
			return jsonObject(t, jsonObject(t, result, "data"), "nearestPlacement")
		}
		furnaceTouchesChest := func(x, y float64) bool {
			return math.Abs(x+25.5) < 1.5 && math.Abs(y-25.5) < 1.5
		}

		base := nearest(`maxRadius:8`)
		if !jsonBool(t, base, "found") {
			t.Fatalf("a free tile exists within 8 tiles: %v", base)
		}
		position := jsonObject(t, base, "position")
		x, y := jsonNumber(t, position, "x"), jsonNumber(t, position, "y")
		if furnaceTouchesChest(x, y) {
			t.Fatalf("furnace footprint at (%v,%v) overlaps the chest", x, y)
		}
		distance := jsonNumber(t, base, "distance")
		if got := math.Hypot(x+25.5, y-25.5); math.Abs(got-distance) > 0.001 {
			t.Fatalf("distance=%v, want %v from the origin", distance, got)
		}
		blockers := jsonArray(t, base, "blockers")
		if len(blockers) != 1 || blockers[0].(string) != "wooden-chest" {
			t.Fatalf("origin blockers=%v, want the chest", blockers)
		}
		// Nothing nearer may be legal: probe every whole-tile center strictly
		// closer than the answer.
		closer := h.lua(t, fmt.Sprintf(`local surface=game.surfaces["`+integrationSurface+`"]
local limit=%f
local count=0
for ix=-40,-10 do for iy=15,35 do
  local d=math.sqrt((ix+25.5)^2+(iy-25.5)^2)
  if d<limit-0.000001 and surface.can_place_entity{name="stone-furnace",position={x=ix,y=iy},force="player"} then count=count+1 end
end end
rcon.print(count)`, distance))
		if strings.TrimSpace(closer) != "0" {
			t.Fatalf("%s legal furnace positions are nearer than the reported %v", closer, distance)
		}
		expandedBase := jsonNumber(t, base, "expanded")
		if expandedBase < 2 {
			t.Fatalf("expanded=%v: a blocked origin must evaluate several candidates", expandedBase)
		}

		tiny := nearest(`maxRadius:0.5`)
		if jsonBool(t, tiny, "found") || tiny["position"] != nil || tiny["distance"] != nil {
			t.Fatalf("no candidate within 0.5 tiles is legal: %v", tiny)
		}
		if tinyBlockers := jsonArray(t, tiny, "blockers"); len(tinyBlockers) != 1 {
			t.Fatalf("not-found result must still explain the origin: %v", tiny)
		}

		spaced := nearest(`maxRadius:10,minDistanceFromEntities:2`)
		if !jsonBool(t, spaced, "found") {
			t.Fatalf("spaced search: %v", spaced)
		}
		sp := jsonObject(t, spaced, "position")
		sx, sy := jsonNumber(t, sp, "x"), jsonNumber(t, sp, "y")
		clearX := math.Max(0, math.Abs(sx+25.5)-1.5)
		clearY := math.Max(0, math.Abs(sy-25.5)-1.5)
		if math.Max(clearX, clearY) < 2-0.001 {
			t.Fatalf("furnace at (%v,%v) leaves only %v tiles around the chest, want at least 2", sx, sy, math.Max(clearX, clearY))
		}

		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
for dx=-1,1 do for dy=-1,1 do
  surface.create_entity{name="iron-ore",position={x=-22.5+dx,y=19.5+dy},amount=100}
end end
rcon.print("ok")`)
		oreQuery := func(extra string) map[string]any {
			t.Helper()
			result := h.query(t, fmt.Sprintf(`{
			  nearestPlacement(input:{surface:%q,force:"player",prototype:"stone-furnace",origin:{x:-22,y:20},maxRadius:8,%s}) {
			    found position { x y } distance
			  }
			}`, integrationSurface, extra))
			return jsonObject(t, jsonObject(t, result, "data"), "nearestPlacement")
		}
		onOre := oreQuery(`avoidResources:false`)
		if d := jsonNumber(t, onOre, "distance"); d != 0 {
			t.Fatalf("ore does not block a furnace, so the origin itself is legal: %v", onOre)
		}
		offOre := oreQuery(`avoidResources:true`)
		if !jsonBool(t, offOre, "found") {
			t.Fatalf("avoidResources search: %v", offOre)
		}
		op := jsonObject(t, offOre, "position")
		ox, oy := jsonNumber(t, op, "x"), jsonNumber(t, op, "y")
		if math.Abs(ox+22.5) < 2.5 && math.Abs(oy-19.5) < 2.5 {
			t.Fatalf("furnace at (%v,%v) overlaps the 3x3 ore block", ox, oy)
		}

		poleProto := h.query(t, `{ prototype(name:"small-electric-pole") { entity { supplyArea } } }`)
		supply := jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, poleProto, "data"), "prototype"), "entity"), "supplyArea")
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
assert(surface.create_entity{name="small-electric-pole",position={x=-30.5,y=28.5},force="player"})
rcon.print("ok")`)
		powered := h.query(t, fmt.Sprintf(`{
		  nearestPlacement(input:{surface:%q,force:"player",prototype:"stone-furnace",origin:{x:-26,y:22},maxRadius:12,requirePowerCoverage:true}) {
		    found position { x y }
		  }
		}`, integrationSurface))
		pw := jsonObject(t, jsonObject(t, powered, "data"), "nearestPlacement")
		if !jsonBool(t, pw, "found") {
			t.Fatalf("power coverage search: %v", pw)
		}
		pp := jsonObject(t, pw, "position")
		px, py := jsonNumber(t, pp, "x"), jsonNumber(t, pp, "y")
		if !(-30.5+supply > px-1 && -30.5-supply < px+1 && 28.5+supply > py-1 && 28.5-supply < py+1) {
			t.Fatalf("furnace at (%v,%v) is outside the pole supply area (%v)", px, py, supply)
		}

		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
surface.set_tiles{{name="water",position={x=-28,y=21}}}
rcon.print("ok")`)
		wet := h.query(t, fmt.Sprintf(`{
		  nearestPlacement(input:{surface:%q,force:"player",prototype:"stone-furnace",origin:{x:-22,y:30},maxRadius:20,requireWaterAdjacency:true}) {
		    found position { x y }
		  }
		}`, integrationSurface))
		wr := jsonObject(t, jsonObject(t, wet, "data"), "nearestPlacement")
		if !jsonBool(t, wr, "found") {
			t.Fatalf("water adjacency search: %v", wr)
		}
		wp := jsonObject(t, wr, "position")
		wx, wy := jsonNumber(t, wp, "x"), jsonNumber(t, wp, "y")
		if !(math.Abs(wx+27.5) < 2.5 && math.Abs(wy-21.5) < 2.5) {
			t.Fatalf("furnace at (%v,%v) is not next to the water tile at (-27.5,21.5)", wx, wy)
		}
		if math.Abs(wx+27.5) < 1.5 && math.Abs(wy-21.5) < 1.5 {
			t.Fatalf("furnace at (%v,%v) is on the water tile", wx, wy)
		}
	})

	t.Run("FuelManyPartialSuccess", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"stone-furnace": 2, "coal": 20})
		const area = `{leftTopX:-33,leftTopY:-31,rightBottomX:-24,rightBottomY:-24}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		units := make([]string, 0, 2)
		for _, x := range []float64{-30, -27} {
			placed := h.mutation(t, fmt.Sprintf(`mutation {
			  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:-28},item:"stone-furnace"}) { unitNumber }
			}`, character.id, integrationSurface, x))
			units = append(units, jsonString(t, jsonObject(t, jsonObject(t, placed, "data"), "place"), "unitNumber"))
		}
		// The second furnace already holds a full stack, so it cannot take more.
		h.lua(t, `for _,e in pairs(game.surfaces["`+integrationSurface+`"].find_entities_filtered{area=`+luaBounds(area)+`,name="stone-furnace"}) do
  if e.unit_number==`+units[1]+` then assert(e.get_fuel_inventory().insert{name="coal",count=50}==50) end
end
rcon.print("ok")`)

		applied := h.mutation(t, fmt.Sprintf(`mutation {
		  fuelMany(input:{actor:{character:%q},item:"coal",countPerTarget:5,targets:[
		    {surface:%q,entity:"stone-furnace",unitNumber:%q},
		    {surface:%q,entity:"stone-furnace",unitNumber:%q},
		    {surface:%q,entity:"stone-furnace",unitNumber:"999999"}]}) {
		    fueled failed moved
		    results { index entity unitNumber requested moved }
		    errors { index code message target { entity unitNumber } }
		  }
		}`, character.id, integrationSurface, units[0], integrationSurface, units[1], integrationSurface))
		result := jsonObject(t, jsonObject(t, applied, "data"), "fuelMany")
		if jsonNumber(t, result, "fueled") != 1 || jsonNumber(t, result, "failed") != 2 || jsonNumber(t, result, "moved") != 5 {
			t.Fatalf("partial success counts: %v", result)
		}
		results := jsonArray(t, result, "results")
		if len(results) != 1 || jsonString(t, results[0].(map[string]any), "unitNumber") != units[0] || jsonNumber(t, results[0].(map[string]any), "moved") != 5 {
			t.Fatalf("results: %v", results)
		}
		codes := map[float64]string{}
		for _, raw := range jsonArray(t, result, "errors") {
			entry := raw.(map[string]any)
			codes[jsonNumber(t, entry, "index")] = jsonString(t, entry, "code")
		}
		if codes[2] != "INVENTORY" || codes[3] != "LOOKUP" || len(codes) != 2 {
			t.Fatalf("typed errors by index: %v", codes)
		}

		inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
		coal := 0.0
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items") {
			item := raw.(map[string]any)
			if jsonString(t, item, "name") == "coal" {
				coal += jsonNumber(t, item, "count")
			}
		}
		if coal != 15 {
			t.Fatalf("actor coal=%v, want 20-5=15: only the fueled target may consume items", coal)
		}
		fueled := h.lua(t, `for _,e in pairs(game.surfaces["`+integrationSurface+`"].find_entities_filtered{area=`+luaBounds(area)+`,name="stone-furnace"}) do
  if e.unit_number==`+units[0]+` then rcon.print(e.get_fuel_inventory().get_item_count("coal")) end
end`)
		if strings.TrimSpace(fueled) != "5" {
			t.Fatalf("empty furnace fuel=%q, want 5", fueled)
		}
	})

	t.Run("TransferManyRoundTrip", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 2, "stone-furnace": 1, "iron-plate": 20})
		const area = `{leftTopX:-23,leftTopY:-31,rightBottomX:-14,rightBottomY:-24}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		var refs []string
		for _, spec := range []struct {
			item string
			x    float64
		}{{"wooden-chest", -21.5}, {"wooden-chest", -19.5}, {"stone-furnace", -16}} {
			placed := h.mutation(t, fmt.Sprintf(`mutation {
			  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:-27.5},item:%q}) { unitNumber }
			}`, character.id, integrationSurface, spec.x, spec.item))
			unit := jsonString(t, jsonObject(t, jsonObject(t, placed, "data"), "place"), "unitNumber")
			refs = append(refs, fmt.Sprintf(`{surface:%q,entity:%q,unitNumber:%q}`, integrationSurface, spec.item, unit))
		}
		targets := strings.Join(refs, ",")

		moved := h.mutation(t, fmt.Sprintf(`mutation {
		  transferMany(input:{actor:{character:%q},inventory:CHEST,direction:TO_ENTITY,item:"iron-plate",countPerTarget:8,targets:[%s]}) {
		    moved transferredStacks failed inventory direction
		    results { index entity requested available capacity moved }
		    errors { index code }
		  }
		}`, character.id, targets))
		result := jsonObject(t, jsonObject(t, moved, "data"), "transferMany")
		if jsonNumber(t, result, "moved") != 16 || jsonNumber(t, result, "failed") != 1 || jsonNumber(t, result, "transferredStacks") < 2 {
			t.Fatalf("deposit: %v", result)
		}
		if jsonString(t, result, "inventory") != "CHEST" || jsonString(t, result, "direction") != "TO_ENTITY" {
			t.Fatalf("typed echo: %v", result)
		}
		for _, raw := range jsonArray(t, result, "results") {
			if jsonNumber(t, raw.(map[string]any), "moved") != 8 {
				t.Fatalf("each chest takes 8: %v", result["results"])
			}
		}
		errs := jsonArray(t, result, "errors")
		if len(errs) != 1 || jsonString(t, errs[0].(map[string]any), "code") != "VALIDATION" || jsonNumber(t, errs[0].(map[string]any), "index") != 3 {
			t.Fatalf("a furnace has no chest inventory: %v", errs)
		}

		back := h.mutation(t, fmt.Sprintf(`mutation {
		  transferMany(input:{actor:{character:%q},inventory:CHEST,direction:TO_ACTOR,item:"iron-plate",countPerTarget:8,targets:[%s]}) {
		    moved failed
		  }
		}`, character.id, refs[0]+","+refs[1]))
		returned := jsonObject(t, jsonObject(t, back, "data"), "transferMany")
		if jsonNumber(t, returned, "moved") != 16 || jsonNumber(t, returned, "failed") != 0 {
			t.Fatalf("withdraw: %v", returned)
		}
		inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
		plates := 0.0
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items") {
			item := raw.(map[string]any)
			if jsonString(t, item, "name") == "iron-plate" {
				plates += jsonNumber(t, item, "count")
			}
		}
		if plates != 20 {
			t.Fatalf("actor plates=%v after the round trip, want all 20 back", plates)
		}
	})

	t.Run("BeltStretchEndGaps", func(t *testing.T) {
		const area = `{leftTopX:17,leftTopY:-33,rightBottomX:31,rightBottomY:-19}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		// One row per scenario, two tiles apart so rows never touch. Belts face
		// east (direction 4).
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local function make(name,x,y,dir,extra)
  local spec={name=name,position={x=x,y=y},direction=dir,force="player"}
  if extra then for k,v in pairs(extra) do spec[k]=v end end
  local e=surface.create_entity(spec); assert(e and e.valid,name.." at "..x..","..y); return e
end
local function run(y,from,to) for x=from,to do make("transport-belt",x+0.5,y,4) end end
-- A: one missing tile, then the line continues
run(-31.5,18,20); run(-31.5,22,24)
-- B: a chest sits in the gap tile
run(-29.5,18,20); make("wooden-chest",21.5,-29.5); run(-29.5,22,24)
-- C: natural end with nothing ahead
run(-27.5,18,20)
-- D: a belt faces the run head-on
run(-25.5,18,20); make("transport-belt",21.5,-25.5,12)
-- E: underground entrance with no exit
make("underground-belt",18.5,-23.5,4,{type="input"})
-- F: underground pair that ends the run naturally
make("underground-belt",18.5,-21.5,4,{type="input"}); make("underground-belt",21.5,-21.5,4,{type="output"})
-- G: a tree blocks the gap tile
run(-20.5,18,20)
local tree=surface.create_entity{name="tree-01",position={x=21.5,y=-20.5},force="neutral"}; assert(tree and tree.valid,"tree")
run(-20.5,22,24)
rcon.print("ok")`)

		forwardEnd := func(entity string, x, y float64) map[string]any {
			t.Helper()
			result := h.query(t, fmt.Sprintf(`{
			  beltStretch(target:{surface:%q,entity:%q,position:{x:%g,y:%g}}) {
			    ends { kind position { x y } expectedTarget { x y } blocker gap reason }
			  }
			}`, integrationSurface, entity, x, y))
			var forward map[string]any
			for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "beltStretch"), "ends") {
				end := raw.(map[string]any)
				if jsonString(t, end, "kind") == "boundary" && jsonNumber(t, jsonObject(t, end, "position"), "x") > 18.6 {
					forward = end
				}
			}
			if forward == nil {
				// An underground entrance alone has only the seed as its end.
				for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "beltStretch"), "ends") {
					end := raw.(map[string]any)
					if jsonString(t, end, "reason") != "NOT_APPLICABLE" {
						forward = end
					}
				}
			}
			if forward == nil {
				t.Fatalf("no forward boundary for %s at (%v,%v): %v", entity, x, y, result)
			}
			return forward
		}

		missing := forwardEnd("transport-belt", 18.5, -31.5)
		if jsonString(t, missing, "reason") != "MISSING_BELT" || !jsonBool(t, missing, "gap") || missing["blocker"] != nil {
			t.Fatalf("missing belt: %v", missing)
		}
		if target := jsonObject(t, missing, "expectedTarget"); jsonNumber(t, target, "x") != 21.5 || jsonNumber(t, target, "y") != -31.5 {
			t.Fatalf("missing belt expectedTarget: %v", target)
		}

		chest := forwardEnd("transport-belt", 18.5, -29.5)
		if jsonString(t, chest, "reason") != "BLOCKED" || !jsonBool(t, chest, "gap") || jsonString(t, chest, "blocker") != "wooden-chest" {
			t.Fatalf("blocked by a chest: %v", chest)
		}

		natural := forwardEnd("transport-belt", 18.5, -27.5)
		if jsonString(t, natural, "reason") != "NATURAL_END" || jsonBool(t, natural, "gap") || natural["blocker"] != nil {
			t.Fatalf("natural end: %v", natural)
		}
		if target := jsonObject(t, natural, "expectedTarget"); jsonNumber(t, target, "x") != 21.5 {
			t.Fatalf("natural end expectedTarget: %v", target)
		}

		headOn := forwardEnd("transport-belt", 18.5, -25.5)
		if jsonString(t, headOn, "reason") != "HEAD_ON" || !jsonBool(t, headOn, "gap") || jsonString(t, headOn, "blocker") != "transport-belt" {
			t.Fatalf("head-on belt: %v", headOn)
		}

		unpaired := forwardEnd("underground-belt", 18.5, -23.5)
		if jsonString(t, unpaired, "reason") != "UNPAIRED_UNDERGROUND" || !jsonBool(t, unpaired, "gap") {
			t.Fatalf("unpaired underground entrance: %v", unpaired)
		}

		paired := h.query(t, fmt.Sprintf(`{
		  beltStretch(target:{surface:%q,entity:"underground-belt",position:{x:18.5,y:-21.5}}) {
		    ends { kind position { x y } gap reason expectedTarget { x y } }
		  }
		}`, integrationSurface))
		var pairedEnd map[string]any
		for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, paired, "data"), "beltStretch"), "ends") {
			end := raw.(map[string]any)
			if jsonNumber(t, jsonObject(t, end, "position"), "x") > 21 {
				pairedEnd = end
			}
		}
		if pairedEnd == nil || jsonString(t, pairedEnd, "reason") != "NATURAL_END" || jsonBool(t, pairedEnd, "gap") {
			t.Fatalf("a paired underground exit with nothing ahead is a natural end: %v", pairedEnd)
		}
		if target := jsonObject(t, pairedEnd, "expectedTarget"); jsonNumber(t, target, "x") != 22.5 {
			t.Fatalf("underground exit expectedTarget: %v", target)
		}

		tree := forwardEnd("transport-belt", 18.5, -20.5)
		if jsonString(t, tree, "reason") != "BLOCKED" || !jsonBool(t, tree, "gap") || jsonString(t, tree, "blocker") != "tree-01" {
			t.Fatalf("blocked by a tree: %v", tree)
		}
	})

	t.Run("FactoryGraphInserterTargets", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"assembling-machine-1": 1, "inserter": 1, "iron-chest": 1})
		const area = `{leftTopX:-32,leftTopY:-32,rightBottomX:-22,rightBottomY:-25}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		place := func(item string, x, y float64, direction int) (string, map[string]any) {
			result := h.mutation(t, fmt.Sprintf(`mutation {
			  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:%g},direction:%d,item:%q}) { unitNumber position { x y } }
			}`, character.id, integrationSurface, x, y, direction, item))
			placed := jsonObject(t, jsonObject(t, result, "data"), "place")
			return jsonString(t, placed, "unitNumber"), jsonObject(t, placed, "position")
		}
		assembler, assemblerPosition := place("assembling-machine-1", -28.5, -28.5, 0)
		if jsonNumber(t, assemblerPosition, "x") != -28.5 {
			t.Fatalf("assembler center=%v, want -28.5", assemblerPosition)
		}
		inserter, _ := place("inserter", -26.5, -28.5, 12)
		place("iron-chest", -26, -29, 0)

		graph := h.query(t, fmt.Sprintf(`{
		  factoryGraph(area:%s, force:"player", surface:%q) {
		    inserters { inserter { unitNumber position { x y } } pickup { name } drop { name } }
		  }
		}`, area, integrationSurface))
		inserters := jsonArray(t, jsonObject(t, jsonObject(t, graph, "data"), "factoryGraph"), "inserters")
		if len(inserters) != 1 {
			t.Fatalf("factoryGraph inserters=%v, want one", inserters)
		}
		connected := inserters[0].(map[string]any)
		if got := jsonString(t, jsonObject(t, connected, "inserter"), "unitNumber"); got != inserter {
			t.Fatalf("factoryGraph inserter unit=%q, want %q", got, inserter)
		}
		if name := jsonString(t, jsonObject(t, connected, "pickup"), "name"); name != "assembling-machine-1" {
			t.Fatalf("factoryGraph pickup=%q, want assembling-machine-1: %v", name, connected)
		}
		if name := jsonString(t, jsonObject(t, connected, "drop"), "name"); name != "iron-chest" {
			t.Fatalf("factoryGraph drop=%q, want iron-chest: %v", name, connected)
		}

		inspection := h.query(t, fmt.Sprintf(`{
		  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"assembling-machine-1",unitNumber:%q}}) {
		    logistics { inserters { inserter { unitNumber } pickup { name } drop { name } } }
		  }
		}`, character.id, integrationSurface, assembler))
		logistics := jsonObject(t, jsonObject(t, jsonObject(t, inspection, "data"), "inspect"), "logistics")
		served := jsonArray(t, logistics, "inserters")
		if len(served) != 1 {
			t.Fatalf("inspection inserters=%v, want the serving inserter", served)
		}
		entry := served[0].(map[string]any)
		if got := jsonString(t, jsonObject(t, entry, "inserter"), "unitNumber"); got != inserter {
			t.Fatalf("inspection inserter unit=%q, want %q", got, inserter)
		}
		if name := jsonString(t, jsonObject(t, entry, "drop"), "name"); name != "iron-chest" {
			t.Fatalf("inspection drop=%q, want iron-chest: %v", name, entry)
		}
	})

	t.Run("BlueprintCatalog", func(t *testing.T) {
		const bookName = "integration-catalog"
		const copyName = "integration-catalog-copy"
		exported := h.mutation(t, `mutation { createBlueprintFromEntities(input:{entities:[{name:"wooden-chest",position:{x:0.5,y:0.5}}]}) { blueprint entityCount } }`)
		blueprint := jsonString(t, jsonObject(t, jsonObject(t, exported, "data"), "createBlueprintFromEntities"), "blueprint")

		// Give addAreaToBook something to collect, then remove it with the test.
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local a=surface.create_entity{name="wooden-chest",position={x=20.5,y=20.5},force="player"}
local b=surface.create_entity{name="iron-chest",position={x=21.5,y=20.5},force="player"}
assert(a and a.valid and b and b.valid,"catalog fixture entities failed")
rcon.print("ok")`)
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area={{9,9},{24,24}}}) do if e.valid then e.destroy() end end
end
rcon.print("ok")`)
		})

		created := h.mutation(t, `mutation { createBlueprintBook(input:{name:"`+bookName+`",label:"Integration",description:"catalog test"}) { name label description entryCount slotCount characters } }`)
		book := jsonObject(t, jsonObject(t, created, "data"), "createBlueprintBook")
		if name := jsonString(t, book, "name"); name != bookName {
			t.Fatalf("created book=%v, want %q", book, bookName)
		}
		if jsonNumber(t, book, "entryCount") != 0 || jsonNumber(t, book, "slotCount") != 0 {
			t.Fatalf("new book is not empty: %v", book)
		}

		added := h.mutation(t, fmt.Sprintf(`mutation { addBlueprintToBook(input:{name:%q,blueprint:%q,label:"single"}) { book { slotCount entryCount activeIndex } entry { index label entityCount } } }`, bookName, blueprint))
		addedEntry := jsonObject(t, jsonObject(t, added, "data"), "addBlueprintToBook", "entry")
		if index := jsonNumber(t, addedEntry, "index"); index != 1 {
			t.Fatalf("first entry index=%v, want 1: %v", index, addedEntry)
		}
		if count := jsonNumber(t, addedEntry, "entityCount"); count != 1 {
			t.Fatalf("first entry entityCount=%v, want 1: %v", count, addedEntry)
		}

		areaAdded := h.mutation(t, fmt.Sprintf(`mutation { addAreaToBook(input:{name:%q,surface:%q,area:{leftTopX:19,leftTopY:19,rightBottomX:23,rightBottomY:22},label:"area"}) { book { slotCount entryCount activeIndex } entry { index label entityCount } } }`, bookName, integrationSurface))
		areaEntry := jsonObject(t, jsonObject(t, areaAdded, "data"), "addAreaToBook", "entry")
		if index := jsonNumber(t, areaEntry, "index"); index != 2 {
			t.Fatalf("area entry index=%v, want 2: %v", index, areaEntry)
		}
		if count := jsonNumber(t, areaEntry, "entityCount"); count != 2 {
			t.Fatalf("area entry captured %v entities, want the 2 fixture chests: %v", count, areaEntry)
		}

		listed := h.query(t, `{ blueprintBooks { name label description entryCount slotCount activeIndex characters } }`)
		books := jsonArray(t, jsonObject(t, listed, "data"), "blueprintBooks")
		if len(books) != 1 {
			t.Fatalf("books=%v, want exactly the integration book (storage is empty in a fresh sandbox)", books)
		}
		row := books[0].(map[string]any)
		if jsonString(t, row, "name") != bookName || jsonNumber(t, row, "entryCount") != 2 || jsonNumber(t, row, "slotCount") != 2 {
			t.Fatalf("book row=%v", row)
		}

		read := h.query(t, fmt.Sprintf(`{ blueprintBook(name:%q,includeBlueprint:true){ name label description entryCount slotCount activeIndex blueprint entries { index name label entityCount blueprint } } }`, bookName))
		readBook := jsonObject(t, jsonObject(t, read, "data"), "blueprintBook")
		if bp := jsonString(t, readBook, "blueprint"); len(bp) < 20 || !strings.HasPrefix(bp, "0") {
			t.Fatalf("book string=%q, want a blueprint-book string", bp)
		}
		entries := jsonArray(t, readBook, "entries")
		if len(entries) != 2 {
			t.Fatalf("entries=%v, want 2", entries)
		}
		for i, raw := range entries {
			entry := raw.(map[string]any)
			if index := jsonNumber(t, entry, "index"); index != float64(i+1) {
				t.Fatalf("entry %d index=%v", i+1, index)
			}
			if bp := jsonString(t, entry, "blueprint"); len(bp) < 20 || !strings.HasPrefix(bp, "0") {
				t.Fatalf("entry %d blueprint=%q, want an exported string", i+1, bp)
			}
		}

		updated := h.mutation(t, fmt.Sprintf(`mutation { updateBlueprintBook(input:{name:%q,label:"Renamed",description:"second",activeIndex:2}) { name label description activeIndex } }`, bookName))
		updatedBook := jsonObject(t, jsonObject(t, updated, "data"), "updateBlueprintBook")
		if jsonString(t, updatedBook, "label") != "Renamed" || jsonNumber(t, updatedBook, "activeIndex") != 2 {
			t.Fatalf("updated book=%v", updatedBook)
		}

		relabeled := h.mutation(t, fmt.Sprintf(`mutation { updateBlueprintInBook(input:{name:%q,index:1,label:"renamed-entry"}) { entry { index label entityCount } } }`, bookName))
		relabeledEntry := jsonObject(t, jsonObject(t, relabeled, "data"), "updateBlueprintInBook", "entry")
		if label := jsonString(t, relabeledEntry, "label"); label != "renamed-entry" {
			t.Fatalf("relabeled entry=%v", relabeledEntry)
		}

		moved := h.mutation(t, fmt.Sprintf(`mutation { moveBlueprintInBook(input:{name:%q,fromIndex:1,toIndex:2}) { entry { index label } } }`, bookName))
		movedEntry := jsonObject(t, jsonObject(t, moved, "data"), "moveBlueprintInBook", "entry")
		if label := jsonString(t, movedEntry, "label"); label != "renamed-entry" || jsonNumber(t, movedEntry, "index") != 2 {
			t.Fatalf("moved entry=%v", movedEntry)
		}

		removed := h.mutation(t, fmt.Sprintf(`mutation { removeBlueprintFromBook(input:{name:%q,index:2}) { book { entryCount slotCount } entry { index label } } }`, bookName))
		removedResult := jsonObject(t, jsonObject(t, removed, "data"), "removeBlueprintFromBook")
		if count := jsonNumber(t, jsonObject(t, removedResult, "book"), "entryCount"); count != 1 {
			t.Fatalf("after remove entryCount=%v, want 1: %v", count, removedResult)
		}
		if label := jsonString(t, jsonObject(t, removedResult, "entry"), "label"); label != "renamed-entry" {
			t.Fatalf("removed entry=%v", removedResult)
		}

		refilled := h.mutation(t, fmt.Sprintf(`mutation { addBlueprintToBook(input:{name:%q,blueprint:%q,index:2,label:"refill"}) { book { entryCount slotCount } entry { index label } } }`, bookName, blueprint))
		refillEntry := jsonObject(t, jsonObject(t, refilled, "data"), "addBlueprintToBook", "entry")
		if index := jsonNumber(t, refillEntry, "index"); index != 2 {
			t.Fatalf("hole refill index=%v, want 2: %v", index, refillEntry)
		}

		placed := h.mutation(t, fmt.Sprintf(`mutation { placeBlueprintFromBook(input:{name:%q,index:2,surface:%q,position:{x:10.5,y:10.5}}) { ghosts entityCount entities { name } } }`, bookName, integrationSurface))
		placeResult := jsonObject(t, jsonObject(t, placed, "data"), "placeBlueprintFromBook")
		if ghosts := jsonNumber(t, placeResult, "ghosts"); ghosts != 1 {
			t.Fatalf("ghosts=%v, want 1 from the single-chest refill entry: %v", ghosts, placeResult)
		}

		latest := h.query(t, fmt.Sprintf(`{ blueprintBook(name:%q){ blueprint } }`, bookName))
		latestString := jsonString(t, jsonObject(t, jsonObject(t, latest, "data"), "blueprintBook"), "blueprint")
		imported := h.mutation(t, fmt.Sprintf(`mutation { importBlueprintBook(input:{name:%q,blueprint:%q,label:"Copy"}) { name label entryCount slotCount } }`, copyName, latestString))
		copied := jsonObject(t, jsonObject(t, imported, "data"), "importBlueprintBook")
		if jsonNumber(t, copied, "entryCount") != 2 || jsonNumber(t, copied, "slotCount") != 2 {
			t.Fatalf("imported copy=%v", copied)
		}
		copyRead := h.query(t, fmt.Sprintf(`{ blueprintBook(name:%q){ entries { index label } } }`, copyName))
		copyEntries := jsonArray(t, jsonObject(t, jsonObject(t, copyRead, "data"), "blueprintBook"), "entries")
		if len(copyEntries) != 2 || jsonString(t, copyEntries[1].(map[string]any), "label") != "refill" {
			t.Fatalf("copied entries=%v", copyEntries)
		}

		// A book string is not a single blueprint, so the entry path must name
		// the real problem instead of writing a nested book by accident.
		bookAsEntry := h.graphqlExpectingError(t, "mutation", fmt.Sprintf(`mutation { addBlueprintToBook(input:{name:%q,blueprint:%q}) { entry { index } } }`, bookName, latestString))
		if bookAsEntry == nil || !strings.Contains(bookAsEntry.Error(), "not a single blueprint") {
			t.Fatalf("adding a book string as an entry must be rejected, got %v", bookAsEntry)
		}

		deleted := h.mutation(t, fmt.Sprintf(`mutation { deleteBlueprintBook(input:{name:%q}) { name bookCount } }`, copyName))
		deletedResult := jsonObject(t, jsonObject(t, deleted, "data"), "deleteBlueprintBook")
		if jsonNumber(t, deletedResult, "bookCount") != 1 || jsonString(t, deletedResult, "name") != copyName {
			t.Fatalf("deleted=%v", deletedResult)
		}
		h.mutation(t, fmt.Sprintf(`mutation { deleteBlueprintBook(input:{name:%q}) { name bookCount } }`, bookName))
		remaining := h.query(t, `{ blueprintBooks { name } }`)
		if books := jsonArray(t, jsonObject(t, remaining, "data"), "blueprintBooks"); len(books) != 0 {
			t.Fatalf("catalog still holds %v after deleting both books", books)
		}
	})

	t.Run("FactorioformStateRoundTrip", func(t *testing.T) {
		const manifest = "integration-ftf"
		h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
local a=surface.create_entity{name="wooden-chest",position={x=40.5,y=40.5},force="player"}
local b=surface.create_entity{name="wooden-chest",position={x=41.5,y=40.5},force="player"}
assert(a and a.valid and b and b.valid,"factorioform fixture failed")
rcon.print("ok")`)
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `storage.factorio_ai_player_factorioform=nil
local surface=game.surfaces["`+integrationSurface+`"]
if surface then for _,e in pairs(surface.find_entities_filtered{area={{40,40},{43,41}}}) do if e.valid then e.destroy() end end end
rcon.print("ok")`)
		})
		importDoc := func(address string, x float64, overwrite bool) string {
			return fmt.Sprintf(`mutation { factorioformImport(input:{name:%q,surface:%q,address:%q,overwrite:%t,target:{entity:"wooden-chest",position:{x:%v,y:40.5}}}) {
			  replacedUnitNumber resource { address unitNumber entity direction quality position { x y } } manifest { name resourceCount } } }`,
				manifest, integrationSurface, address, overwrite, x)
		}
		imported := jsonObject(t, h.mutation(t, importDoc("chest.one", 40.5, false)), "data", "factorioformImport")
		resource := jsonObject(t, imported, "resource")
		unitOne := jsonString(t, resource, "unitNumber")
		if jsonString(t, resource, "entity") != "wooden-chest" || jsonString(t, resource, "quality") != "normal" || unitOne == "" {
			t.Fatalf("imported resource = %v", resource)
		}
		if err := h.graphqlExpectingError(t, "mutation", importDoc("chest.one", 41.5, false)); err == nil || !strings.Contains(err.Error(), "overwrite") {
			t.Fatalf("remap without overwrite err = %v", err)
		}
		if err := h.graphqlExpectingError(t, "mutation", importDoc("chest.two", 40.5, false)); err == nil || !strings.Contains(err.Error(), "already mapped") {
			t.Fatalf("duplicate unit err = %v", err)
		}
		remapped := jsonObject(t, h.mutation(t, importDoc("chest.one", 41.5, true)), "data", "factorioformImport")
		if jsonString(t, remapped, "replacedUnitNumber") != unitOne {
			t.Fatalf("remap result = %v, want replaced %s", remapped, unitOne)
		}
		state := jsonArray(t, h.query(t, fmt.Sprintf(`{ factorioformState(name:%q) { name surface force resourceCount resources { address unitNumber entity } } }`, manifest)), "data", "factorioformState")
		if len(state) != 1 || len(jsonArray(t, state[0].(map[string]any), "resources")) != 1 {
			t.Fatalf("state = %v", state)
		}
		summaries := jsonArray(t, h.query(t, `{ factorioformState { name resourceCount resources { address } } }`), "data", "factorioformState")
		if len(summaries) != 1 || len(jsonArray(t, summaries[0].(map[string]any), "resources")) != 0 {
			t.Fatalf("summaries = %v", summaries)
		}
		forgotten := jsonObject(t, h.mutation(t, fmt.Sprintf(`mutation { factorioformForget(input:{name:%q,addresses:["chest.one","nope"]}) { forgotten missing manifestDeleted remaining } }`, manifest)), "data", "factorioformForget")
		if len(jsonArray(t, forgotten, "forgotten")) != 1 || len(jsonArray(t, forgotten, "missing")) != 1 || jsonNumber(t, forgotten, "remaining") != 0 {
			t.Fatalf("forget = %v", forgotten)
		}
		jsonObject(t, h.mutation(t, fmt.Sprintf(`mutation { factorioformForget(input:{name:%q}) { manifestDeleted } }`, manifest)), "data", "factorioformForget")
		if err := h.graphqlExpectingError(t, "query", fmt.Sprintf(`{ factorioformState(name:%q) { name } }`, manifest)); err == nil {
			t.Fatal("state of a forgotten manifest must error")
		}
	})

	t.Run("BuildGhostsConsumesItems", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.destroyCharacter(t, character.id)
		})
		h.grantItems(t, character.id, map[string]int{"wooden-chest": 2})
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities_filtered{area={{9,-9},{20,1}}}) do if e.valid then e.destroy() end end
end
rcon.print("ok")`)
		})
		inventory := func() float64 {
			result := h.query(t, fmt.Sprintf(`{ inspect(input:{actor:{character:%q},target:{surface:%q,unitNumber:%q}}) { inventories { main { name count } } } }`, character.id, integrationSurface, character.id))
			main := jsonArray(t, jsonObject(t, jsonObject(t, result, "data"), "inspect", "inventories"), "main")
			for _, raw := range main {
				row := raw.(map[string]any)
				if jsonString(t, row, "name") == "wooden-chest" {
					return jsonNumber(t, row, "count")
				}
			}
			return 0
		}
		place := func(x float64) {
			h.mutation(t, fmt.Sprintf(`mutation { placeGhosts(input:{surface:%q,force:"player",entities:[{name:"wooden-chest",position:{x:%g,y:-6.5}}]}) { ghosts { name } } }`, integrationSurface, x))
		}
		build := func() map[string]any {
			built := h.mutation(t, fmt.Sprintf(`mutation {
			  buildGhosts(input:{actor:{character:%q},surface:%q,force:"player",area:{leftTopX:8,leftTopY:-9,rightBottomX:22,rightBottomY:1},limit:10}) {
			    revived failed consumed { name count } missing { name count quality } errors { code } entities { name }
			  }
			}`, character.id, integrationSurface))
			return jsonObject(t, jsonObject(t, built, "data"), "buildGhosts")
		}

		if got := inventory(); got != 2 {
			t.Fatalf("character starts with %v wooden-chests, want 2", got)
		}
		place(12.5)
		first := build()
		if jsonNumber(t, first, "revived") != 1 || jsonNumber(t, first, "failed") != 0 {
			t.Fatalf("first build=%v, want one revival", first)
		}
		consumed := jsonArray(t, first, "consumed")
		if len(consumed) != 1 || jsonString(t, consumed[0].(map[string]any), "name") != "wooden-chest" || jsonNumber(t, consumed[0].(map[string]any), "count") != 1 {
			t.Fatalf("consumed=%v, want one wooden-chest", consumed)
		}
		if got := inventory(); got != 1 {
			t.Fatalf("character holds %v wooden-chests after the build, want 1; revival must consume stock", got)
		}
		place(14.5)
		second := build()
		if jsonNumber(t, second, "revived") != 1 {
			t.Fatalf("second build=%v, want one revival", second)
		}
		if got := inventory(); got != 0 {
			t.Fatalf("character holds %v wooden-chests after the second build, want 0", got)
		}
		place(16.5)
		third := build()
		if jsonNumber(t, third, "revived") != 0 || jsonNumber(t, third, "failed") != 1 {
			t.Fatalf("third build=%v, want a refused revival without stock", third)
		}
		missing := jsonArray(t, third, "missing")
		if len(missing) != 1 || jsonString(t, missing[0].(map[string]any), "name") != "wooden-chest" || jsonNumber(t, missing[0].(map[string]any), "count") != 1 {
			t.Fatalf("missing=%v, want one wooden-chest", missing)
		}
		errors := jsonArray(t, third, "errors")
		if len(errors) != 1 || jsonString(t, errors[0].(map[string]any), "code") != "MATERIALS" {
			t.Fatalf("errors=%v, want a MATERIALS failure", errors)
		}
		if entities := jsonArray(t, third, "entities"); len(entities) != 0 {
			t.Fatalf("a ghost without stock must not be built, entities=%v", entities)
		}
	})

	t.Run("GraphSnapshotPause", func(t *testing.T) {
		h.lua(t, `game.tick_paused=false; game.surfaces["`+integrationSurface+`"].create_entity{name="wooden-chest",position={20.5,300.5},force="player"}; rcon.print("ok")`)
		t.Cleanup(func() { h.lua(t, `game.tick_paused=false; rcon.print("ok")`) })
		for _, initiallyPaused := range []bool{false, true} {
			t.Run(fmt.Sprintf("initiallyPaused=%t", initiallyPaused), func(t *testing.T) {
				h.lua(t, fmt.Sprintf(`game.tick_paused=%t; rcon.print("ok")`, initiallyPaused))
				opts := graphExportOptions{Force: "player", Surfaces: []string{integrationSurface},
					Out: filepath.Join(t.TempDir(), "snapshot"), LeafMax: 1, Concurrency: 4,
					ChunkPage: 20000, Pause: true}
				_, m, err := exportGraphContext(context.Background(), opts, io.Discard, h.addr, h.password)
				if err != nil {
					t.Fatal(err)
				}
				for _, surface := range m.Surfaces {
					if surface.TickBefore != surface.TickAfter {
						t.Fatalf("paused export advanced ticks: %v..%v", surface.TickBefore, surface.TickAfter)
					}
					for _, leaf := range surface.Leaves {
						if leaf.Tick != surface.TickBefore {
							t.Fatalf("leaf tick %v differs from census %v", leaf.Tick, surface.TickBefore)
						}
					}
				}
				want := fmt.Sprintf("%t", initiallyPaused)
				if got := strings.TrimSpace(h.lua(t, `rcon.print(tostring(game.tick_paused))`)); got != want {
					t.Fatalf("pause state after export = %q, want %q", got, want)
				}
				opts.Surfaces = []string{"missing-snapshot-pause-surface"}
				if _, _, err := exportGraphContext(context.Background(), opts, io.Discard, h.addr, h.password); err == nil {
					t.Fatal("export on missing surface succeeded")
				}
				if got := strings.TrimSpace(h.lua(t, `rcon.print(tostring(game.tick_paused))`)); got != want {
					t.Fatalf("pause state after failed export = %q, want %q", got, want)
				}
			})
		}
	})

	t.Run("GraphExportCoverage", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		// The fixture straddles the chunk border at x=0 so leafMax 1 splits it
		// into several leaves and ownership crosses leaf boundaries.
		const area = `{leftTopX:-12,leftTopY:296,rightBottomX:12,rightBottomY:316}`
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
for _,e in pairs(surface.find_entities_filtered{area=`+luaBounds(area)+`}) do if e.valid and e.type~="character" then e.destroy() end end
rcon.print("ok")`)
		})
		fixture := h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local function make(spec) spec.force="player"; local e=s.create_entity(spec); if not e then error("could not create "..spec.name) end; return e end
local beacon=make{name="beacon",position={x=-4.5,y=300.5}}
beacon.get_module_inventory().insert{name="speed-module",count=1}
local assembler=make{name="assembling-machine-2",position={x=-1.5,y=300.5}}
assembler.set_recipe("iron-gear-wheel")
make{name="straight-rail",position={x=3,y=299},direction=defines.direction.north}
make{name="straight-rail",position={x=3,y=301},direction=defines.direction.north}
local combinator=make{name="constant-combinator",position={x=-3.5,y=304.5}}
local lamp=make{name="small-lamp",position={x=-1.5,y=304.5}}
combinator.get_wire_connector(defines.wire_connector_id.circuit_red,true).connect_to(lamp.get_wire_connector(defines.wire_connector_id.circuit_red,true))
make{name="roboport",position={x=6,y=306}}
make{name="passive-provider-chest",position={x=8.5,y=306.5}}
for x=-1.5,0.5 do make{name="pipe",position={x=x,y=310.5}} end
local splitter=make{name="splitter",position={x=-8.5,y=309},direction=defines.direction.east}
splitter.splitter_output_priority="left"
local splitUp=make{name="transport-belt",position={x=-7.5,y=308.5},direction=defines.direction.east}
local splitDown=make{name="transport-belt",position={x=-7.5,y=309.5},direction=defines.direction.east}
rcon.print("ok")`)
		if !strings.Contains(fixture, "ok") {
			t.Fatalf("graph fixture: %s", fixture)
		}

		out := filepath.Join(t.TempDir(), "snapshot")
		dir, err := exportGraph(graphExportOptions{Force: "player", Surfaces: []string{integrationSurface}, Out: out,
			LeafMax: 1, Concurrency: 4, ChunkPage: 20000}, io.Discard, h.addr, h.password)
		if err != nil {
			t.Fatalf("graph export: %v", err)
		}
		m, err := readGraphManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		if m.Status != graphStatusComplete {
			t.Fatalf("export status %s: %+v", m.Status, m.Surfaces)
		}
		if s := m.Surfaces[0]; len(s.Leaves) < 2 || s.Mobile < 1 {
			t.Fatalf("want several leaves and the spawned character as mobile, got %d leaves, %d mobile", len(s.Leaves), s.Mobile)
		}
		for _, typ := range []string{"AFFECTS", "RAIL_LINK", "CIRCUIT_WIRE", "ON_CIRCUIT", "IN_LOGISTIC_NETWORK", "FLUID_LINK"} {
			if m.EdgeTypes[typ] == 0 {
				t.Errorf("no %s edge in the export: %v", typ, m.EdgeTypes)
			}
		}
		nodes := map[string]graphNodeOut{}
		if err := eachGraphLine(filepath.Join(dir, graphSnapshotNodes), func(line []byte) error {
			var n graphNodeOut
			if err := json.Unmarshal(line, &n); err != nil {
				return err
			}
			if name, _ := n.Props["name"].(string); name != "" {
				nodes[name] = n
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got := nodes["assembling-machine-2"].Props["recipe"]; got != "iron-gear-wheel" {
			t.Errorf("assembler recipe=%v, want iron-gear-wheel", got)
		}
		if got, _ := nodes["beacon"].Props["modules"].([]any); len(got) != 1 || got[0] != "speed-module:normal:1" {
			t.Errorf("beacon modules=%v, want speed-module:normal:1", nodes["beacon"].Props["modules"])
		}
		if _, ok := nodes["passive-provider-chest"].Props["logisticNetwork"]; !ok {
			t.Errorf("chest inside roboport coverage has no logistic network: %v", nodes["passive-provider-chest"].Props)
		}
		splitterID, _ := nodes["splitter"].Props["id"].(string)
		ports := map[string]string{}
		// Three pipes in a row are two physical links; each port reports its own
		// side, and canonicalisation must fold them into two undirected edges.
		if m.EdgeTypes["FLUID_LINK"] != 2 {
			t.Errorf("FLUID_LINK=%d, want 2 for three pipes in a row", m.EdgeTypes["FLUID_LINK"])
		}
		if err := eachGraphLine(filepath.Join(dir, graphSnapshotEdges), func(line []byte) error {
			var e graphEdgeOut
			if err := json.Unmarshal(line, &e); err != nil {
				return err
			}
			if e.Type == "FLUID_LINK" && (e.Props["ports"] != float64(2) || e.Props["dirA"] == nil || e.Props["dirB"] == nil) {
				t.Errorf("fluid link %s must carry both ports: %v", e.Key, e.Props)
			}
			if e.Type == "BELT_OUT" && e.From == splitterID {
				ports[e.Props["fromPort"].(string)] = e.To
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// A splitter is a :Splitter and :Belt with both priorities, and each of
		// its two output belts is reachable through the port it leaves from.
		splitter := nodes["splitter"]
		if splitter.Props["outputPriority"] != "left" || splitter.Props["inputPriority"] != "none" {
			t.Errorf("splitter priorities=%v", splitter.Props)
		}
		if strings.Join(splitter.Labels, ",") != "Entity,Belt,Splitter" {
			t.Errorf("splitter labels=%v", splitter.Labels)
		}
		if ports["left"] == "" || ports["right"] == "" || ports["left"] == ports["right"] {
			t.Errorf("splitter output ports=%v, want one belt per side", ports)
		}
	})

	t.Run("GraphSplitterPortsAllDirections", func(t *testing.T) {
		h.createTestSurface(t)
		// The engine labels a splitter's lines left_split_line/right_split_line
		// for the two lanes of ONE output belt and secondary_*_split_line for the
		// second output, so line names identify lanes, not sides; belt_neighbours
		// keys are just ordering. The port therefore has to be geometric, and
		// this test pins it in all four orientations.
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
s.request_to_generate_chunks({0,320},4)
s.force_generate_chunk_requests()
local function make(spec) spec.force="player"; local e=s.create_entity(spec); if not e then error("could not create "..spec.name) end; return e end
make{name="splitter",position={x=0.5,y=330.5},direction=defines.direction.east}
make{name="transport-belt",position={x=1.5,y=330.5},direction=defines.direction.east}
make{name="transport-belt",position={x=1.5,y=331.5},direction=defines.direction.east}
make{name="splitter",position={x=5.5,y=330.5},direction=defines.direction.north}
make{name="transport-belt",position={x=5.5,y=329.5},direction=defines.direction.north}
make{name="transport-belt",position={x=6.5,y=329.5},direction=defines.direction.north}
make{name="splitter",position={x=10.5,y=330.5},direction=defines.direction.south}
make{name="transport-belt",position={x=10.5,y=331.5},direction=defines.direction.south}
make{name="transport-belt",position={x=11.5,y=331.5},direction=defines.direction.south}
make{name="splitter",position={x=15.5,y=330.5},direction=defines.direction.west}
make{name="transport-belt",position={x=14.5,y=330.5},direction=defines.direction.west}
make{name="transport-belt",position={x=14.5,y=331.5},direction=defines.direction.west}
rcon.print("ok")`)
		dir, err := exportGraph(graphExportOptions{Force: "player", Surfaces: []string{integrationSurface}, Out: filepath.Join(t.TempDir(), "s"),
			LeafMax: 2000, Concurrency: 4, ChunkPage: 20000, Keep: 5}, io.Discard, h.addr, h.password)
		if err != nil {
			t.Fatalf("graph export: %v", err)
		}
		type pos struct{ x, y float64 }
		ids := map[pos]string{}
		var splitters []pos
		if err := eachGraphLine(filepath.Join(dir, graphSnapshotNodes), func(line []byte) error {
			var n graphNodeOut
			if err := json.Unmarshal(line, &n); err != nil {
				return err
			}
			x, ok := n.Props["x"].(float64)
			if !ok {
				return nil
			}
			y, _ := n.Props["y"].(float64)
			ids[pos{x, y}] = n.ID
			if kind, _ := n.Props["type"].(string); kind == "splitter" {
				splitters = append(splitters, pos{x, y})
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		outputs := map[string]map[string]string{}
		if err := eachGraphLine(filepath.Join(dir, graphSnapshotEdges), func(line []byte) error {
			var e graphEdgeOut
			if err := json.Unmarshal(line, &e); err != nil {
				return err
			}
			port, _ := e.Props["fromPort"].(string)
			if e.Type == "BELT_OUT" && port != "" {
				if outputs[e.From] == nil {
					outputs[e.From] = map[string]string{}
				}
				outputs[e.From][port] = e.To
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// A multi-tile splitter reports the centre of its footprint, not the
		// create position, so it is matched to the nearest expected footprint.
		splitterNear := func(expect pos) (pos, string) {
			best := pos{}
			bestID := ""
			bestDist := 0.0
			for _, p := range splitters {
				d := math.Abs(p.x-expect.x) + math.Abs(p.y-expect.y)
				if d < 1.5 && (bestID == "" || d < bestDist) {
					best, bestID, bestDist = p, ids[p], d
				}
			}
			return best, bestID
		}
		// left is the side counter-clockwise of travel: north of an eastward
		// splitter, west of a northward one, and so on.
		for _, tc := range []struct {
			name        string
			splitter    pos
			left, right pos
		}{
			{"east", pos{0.5, 330.5}, pos{1.5, 330.5}, pos{1.5, 331.5}},
			{"north", pos{5.5, 330.5}, pos{5.5, 329.5}, pos{6.5, 329.5}},
			{"south", pos{10.5, 330.5}, pos{11.5, 331.5}, pos{10.5, 331.5}},
			{"west", pos{15.5, 330.5}, pos{14.5, 331.5}, pos{14.5, 330.5}},
		} {
			at, id := splitterNear(tc.splitter)
			if id == "" {
				t.Errorf("%s: no splitter near %v (found %v)", tc.name, tc.splitter, splitters)
				continue
			}
			got := outputs[id]
			if len(got) != 2 {
				t.Errorf("%s splitter at %v ports=%v, want two", tc.name, at, got)
				continue
			}
			if got["left"] != ids[tc.left] || got["right"] != ids[tc.right] {
				t.Errorf("%s splitter left=%s right=%s, want left=%s (%v) right=%s (%v)", tc.name,
					got["left"], got["right"], ids[tc.left], tc.left, ids[tc.right], tc.right)
			}
		}
	})

	t.Run("InserterDropLaneRule", func(t *testing.T) {
		h.createTestSurface(t)
		// The engine decides the lane of an in-line drop; graph_lane cannot derive
		// it from geometry (the drop point has no perpendicular offset). This pins
		// the rule the exporter encodes: short and long-handed arms, belt moving
		// toward and away, all land on the right transport line (index 2), and a
		// perpendicular drop lands on the far lane.
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
s.request_to_generate_chunks({8,336},2)
s.force_generate_chunk_requests()
local function make(spec)
  spec.force="player"
  local e=s.create_entity(spec)
  if not e then error("could not create "..spec.name.." at "..tostring(spec.position.x)..","..tostring(spec.position.y)) end
  return e
end
local D=defines.direction
local function lane(name,x,insdir,beltdy,beltdir,chestdy,proto)
  local chest=make{name="wooden-chest",position={x=x+0.5,y=340.5+chestdy}}
  chest.insert{name="iron-plate",count=60}
  make{name=proto,position={x=x+0.5,y=340.5},direction=insdir}
  make{name="transport-belt",position={x=x+0.5,y=340.5+beltdy},direction=beltdir}
  rcon.print(name.." ok")
end
for x=0,48,6 do
  make{name="electric-energy-interface",position={x=x+3.5,y=340.5}}
  make{name="medium-electric-pole",position={x=x+3.5,y=342.5}}
end
lane("short_away",0,D.north,1,D.south,-1,"inserter")
lane("short_toward",6,D.north,1,D.north,-1,"inserter")
lane("short_east",12,D.north,1,D.east,-1,"inserter")
lane("short_west",18,D.north,1,D.west,-1,"inserter")
lane("long_away",24,D.north,2,D.south,-2,"long-handed-inserter")
lane("long_toward",30,D.north,2,D.north,-2,"long-handed-inserter")
lane("long_east",36,D.north,2,D.east,-2,"long-handed-inserter")
lane("long_west",42,D.north,2,D.west,-2,"long-handed-inserter")
rcon.print("lanes ready")`)
		time.Sleep(3 * time.Second)
		got := h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local function lines(x,y)
  local belt=s.find_entity("transport-belt",{x=x,y=y})
  if not belt then return "missing" end
  local out={}
  for i=1,belt.get_max_transport_line_index() do
    local n=0
    for _,it in pairs(belt.get_transport_line(i).get_contents()) do n=n+it.count end
    out[#out+1]=tostring(i).."="..tostring(n)
  end
  local shape="?"
  local ok,sh=pcall(function() return belt.belt_shape end)
  if ok then shape=tostring(sh) end
  return shape.." "..table.concat(out," ")
end
rcon.print("short_away "..lines(0.5,341.5))
rcon.print("short_toward "..lines(6.5,341.5))
rcon.print("short_east "..lines(12.5,341.5))
rcon.print("short_west "..lines(18.5,341.5))
rcon.print("long_away "..lines(24.5,342.5))
rcon.print("long_toward "..lines(30.5,342.5))
rcon.print("long_east "..lines(36.5,342.5))
rcon.print("long_west "..lines(42.5,342.5))
rcon.print("done")`)
		// In-line drops always land on line 2; perpendicular drops land on the lane
		// away from the inserter (belt east -> right line 2, belt west -> left 1).
		// Counts vary with the inserter's hand and belt backlog, so only the line
		// that receives items is asserted.
		laneCounts := map[string]map[int]int{}
		for _, line := range strings.Split(got, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[1] != "straight" {
				continue
			}
			counts := map[int]int{}
			for _, part := range fields[2:] {
				var index, count int
				if _, err := fmt.Sscanf(part, "%d=%d", &index, &count); err == nil {
					counts[index] = count
				}
			}
			laneCounts[fields[0]] = counts
		}
		for _, want := range []struct {
			name  string
			right bool
		}{
			{"short_away", true}, {"short_toward", true}, {"short_east", true}, {"short_west", false},
			{"long_away", true}, {"long_toward", true}, {"long_east", true}, {"long_west", false},
		} {
			counts, ok := laneCounts[want.name]
			if !ok {
				t.Errorf("%s: no lane readback in %q", want.name, got)
				continue
			}
			if want.right {
				if counts[1] != 0 || counts[2] == 0 {
					t.Errorf("%s: lanes=%v, want items only on line 2", want.name, counts)
				}
			} else if counts[1] == 0 || counts[2] != 0 {
				t.Errorf("%s: lanes=%v, want items only on line 1", want.name, counts)
			}
		}
	})

	t.Run("GraphDetailFields", func(t *testing.T) {
		h.createTestSurface(t)
		// One drill over two ore types (the copper-on-iron-bus cause), a furnace
		// with a product counter, an underground belt carrying different items on
		// its surface and tunnel lines, and an inserter with lane flags.
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
s.request_to_generate_chunks({8,344},2)
s.force_generate_chunk_requests()
local function make(spec)
  spec.force="player"
  local e=s.create_entity(spec)
  if not e then error("could not create "..spec.name.." at "..tostring(spec.position.x)..","..tostring(spec.position.y)) end
  return e
end
local D=defines.direction
for dx=-2,2 do for dy=-2,2 do
  s.create_entity{name="iron-ore",position={x=10.5+dx,y=348.5+dy},amount=500,force="neutral"}
end end
s.create_entity{name="copper-ore",position={x=11.5,y=348.5},amount=500,force="neutral"}
make{name="electric-mining-drill",position={x=10.5,y=348.5},direction=D.north}
make{name="electric-furnace",position={x=10.5,y=351.5},direction=D.north}
local ugIn=make{name="underground-belt",position={x=20.5,y=350.5},direction=D.east,type="input"}
local ugOut=make{name="underground-belt",position={x=23.5,y=350.5},direction=D.east,type="output"}
ugIn.get_transport_line(1).insert_at_back({name="iron-plate",count=3})
ugIn.get_transport_line(2).insert_at_back({name="copper-plate",count=2})
-- different item on the tunnel side of the same belt half
ugIn.get_transport_line(3).insert_at_back({name="copper-plate",count=1})
make{name="inserter",position={x=26.5,y=350.5},direction=D.north}
make{name="electric-energy-interface",position={x=26.5,y=348.5}}
rcon.print("detail ready")`)

		out := filepath.Join(t.TempDir(), "snapshot")
		dir, err := exportGraph(graphExportOptions{Force: "player", Surfaces: []string{integrationSurface}, Out: out,
			LeafMax: 2000, Concurrency: 4, ChunkPage: 20000}, io.Discard, h.addr, h.password)
		if err != nil {
			t.Fatalf("graph export: %v", err)
		}
		byType := map[string][]graphNodeOut{}
		if err := eachGraphLine(filepath.Join(dir, graphSnapshotNodes), func(line []byte) error {
			var n graphNodeOut
			if err := json.Unmarshal(line, &n); err != nil {
				return err
			}
			if typ, _ := n.Props["type"].(string); typ != "" {
				byType[typ] = append(byType[typ], n)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if drill := byType["mining-drill"]; len(drill) != 1 {
			t.Fatalf("drills=%d, want 1", len(drill))
		} else {
			res, _ := drill[0].Props["areaResources"].([]any)
			if len(res) != 2 {
				t.Fatalf("areaResources=%v, want iron and copper tiles", drill[0].Props["areaResources"])
			}
			// The test surface is generated terrain, so natural ore may sit in the
			// area too; the drill's own 5x5 is the floor.
			if tiles, _ := drill[0].Props["areaTiles"].(float64); tiles < 25 {
				t.Fatalf("areaTiles=%v, want at least the 5x5 mining area (25)", drill[0].Props["areaTiles"])
			}
			for _, field := range []string{"miningLeft", "miningTop", "miningRight", "miningBottom"} {
				if _, ok := drill[0].Props[field]; !ok {
					t.Errorf("drill has no %s", field)
				}
			}
		}
		if furnaces := byType["furnace"]; len(furnaces) != 1 {
			t.Fatalf("furnaces=%d, want 1", len(furnaces))
		} else {
			if _, ok := furnaces[0].Props["productsFinished"]; !ok {
				t.Errorf("furnace has no productsFinished: %v", furnaces[0].Props)
			}
			if _, ok := furnaces[0].Props["craftingProgress"]; !ok {
				t.Errorf("furnace has no craftingProgress: %v", furnaces[0].Props)
			}
		}
		// Every belt-family node samples its engine lines, not just transport belts.
		// Every belt-family node reports per-side counts (an empty half still has
		// them); only non-empty lines are listed, so the loaded half carries lanes.
		sampled := false
		for _, typ := range []string{"underground-belt"} {
			for _, belt := range byType[typ] {
				if _, ok := belt.Props["leftCount"]; !ok {
					t.Errorf("%s %v has no leftCount: %v", typ, belt.Props["unit"], belt.Props)
				}
				if _, ok := belt.Props["lanes"]; ok {
					sampled = true
				}
			}
		}
		if !sampled {
			t.Errorf("no underground belt carried a lanes sample: %v", byType["underground-belt"])
		}
		for _, belt := range byType["underground-belt"] {
			if v, _ := belt.Props["unit"].(float64); v == 0 {
				continue
			}
			if mixed, ok := belt.Props["mixedLeft"].(bool); ok && mixed {
				return // the tunnel-side sample proved a mixed left side
			}
		}
		t.Errorf("no underground belt reported a mixed left side: %v", byType["underground-belt"])
	})

	t.Run("MineAreaAndGatherItems", func(t *testing.T) {
		h.createTestSurface(t)
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		t.Cleanup(func() {
			if h.isStopped() {
				return
			}
			h.lua(t, `local surface=game.surfaces["`+integrationSurface+`"]
if surface then
  for _,e in pairs(surface.find_entities()) do if e.valid and e.type~="character" then e.destroy() end end
end
rcon.print("ok")`)
		})
		holds := func(item string) float64 {
			inventory := h.query(t, fmt.Sprintf(`{ inventory(input:{character:%q}) { items { name count } } }`, character.id))
			total := 0.0
			for _, raw := range jsonArray(t, jsonObject(t, jsonObject(t, inventory, "data"), "inventory"), "items") {
				entry := raw.(map[string]any)
				if jsonString(t, entry, "name") == item {
					total += jsonNumber(t, entry, "count")
				}
			}
			return total
		}
		sweep := func(args string) map[string]any {
			result := h.mutation(t, fmt.Sprintf(`mutation {
			  mineArea(input:{actor:{character:%q},%s}) {
			    requested gained stopReason tilesFound tilesTouched tilesDepleted failed
			    errors { index code message } items { name count }
			  }
			}`, character.id, args))
			return jsonObject(t, jsonObject(t, result, "data"), "mineArea")
		}
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
for x=10,13 do assert(s.create_entity{name="iron-ore",position={x+0.5,10.5},amount=50}) end
assert(s.create_entity{name="coal",position={10.5,12.5},amount=30})
rcon.print("ok")`)
		const area = `area:{leftTopX:8,leftTopY:8,rightBottomX:16,rightBottomY:16}`

		partial := sweep(area + `,resource:"iron-ore",count:7`)
		if jsonString(t, partial, "stopReason") != "COUNT_REACHED" || jsonNumber(t, partial, "gained") < 7 || jsonNumber(t, partial, "tilesFound") != 4 || jsonNumber(t, partial, "tilesTouched") != 1 {
			t.Fatalf("stop at count: %v", partial)
		}
		if got := holds("iron-ore"); got != jsonNumber(t, partial, "gained") || holds("coal") != 0 {
			t.Fatalf("iron-ore=%v coal=%v after the partial sweep: %v", got, holds("coal"), partial)
		}
		rest := sweep(area + `,resource:"iron-ore",count:1000`)
		if jsonString(t, rest, "stopReason") != "AREA_EXHAUSTED" || jsonNumber(t, rest, "tilesDepleted") != 4 || holds("iron-ore") != 200 || jsonNumber(t, rest, "failed") != 0 {
			t.Fatalf("exhaust the ore: %v (inventory ore %v)", rest, holds("iron-ore"))
		}
		everything := sweep(area + `,count:1000`)
		if jsonString(t, everything, "stopReason") != "AREA_EXHAUSTED" || jsonNumber(t, everything, "gained") != 30 || holds("coal") != 30 {
			t.Fatalf("all resources: %v", everything)
		}
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
assert(s.create_entity{name="stone",position={12.5,12.5},amount=20})
local c=game.get_entity_by_unit_number(`+character.id+`)
local inv=c.get_inventory(defines.inventory.character_main)
for i=1,#inv do if not inv[i].valid_for_read then inv[i].set_stack{name="wooden-chest",count=50} end end
rcon.print("ok")`)
		full := sweep(area + `,count:5`)
		if jsonString(t, full, "stopReason") != "INVENTORY_FULL" || jsonNumber(t, full, "gained") != 0 || jsonNumber(t, full, "failed") != 0 {
			t.Fatalf("full inventory: %v", full)
		}

		// gatherItems: a chest, a furnace output, a belt, a ground stack and an
		// infinity chest all hold iron plates inside the search area.
		h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local c=game.get_entity_by_unit_number(`+character.id+`)
local inv=c.get_inventory(defines.inventory.character_main); inv.clear()
local chest=assert(s.create_entity{name="wooden-chest",position={-18.5,10.5},force="player"}); assert(chest.insert{name="iron-plate",count=20}==20)
local furnace=assert(s.create_entity{name="stone-furnace",position={-15,10},force="player"}); assert(furnace.get_output_inventory().insert{name="iron-plate",count=10}==10)
for _,x in ipairs({-10.5,-8.5}) do
  local belt=assert(s.create_entity{name="transport-belt",position={x,12.5},direction=defines.direction.east,force="player"})
  for line=1,2 do assert(belt.get_transport_line(line).insert_at_back{name="iron-plate",count=1}) end
end
assert(s.create_entity{name="item-on-ground",position={-6.5,14.5},stack={name="iron-plate",count=5}})
local infinity=assert(s.create_entity{name="infinity-chest",position={-12.5,8.5},force="player"})
infinity.set_infinity_container_filter(1,{name="iron-plate",count=50,mode="exactly"})
rcon.print("ok")`)
		gather := func(args string) map[string]any {
			result := h.mutation(t, fmt.Sprintf(`mutation {
			  gatherItems(input:{actor:{character:%q},area:{leftTopX:-20,leftTopY:6,rightBottomX:-4,rightBottomY:16},item:"iron-plate",%s}) {
			    requested gathered stopReason sourcesTruncated failed
			    tiers { tier sources moved } sources { tier entity moved } errors { code message }
			  }
			}`, character.id, args))
			return jsonObject(t, jsonObject(t, result, "data"), "gatherItems")
		}
		tierMoved := func(result map[string]any, tier string) float64 {
			for _, raw := range jsonArray(t, result, "tiers") {
				entry := raw.(map[string]any)
				if jsonString(t, entry, "tier") == tier {
					return jsonNumber(t, entry, "moved")
				}
			}
			t.Fatalf("tier %s missing: %v", tier, result["tiers"])
			return 0
		}
		first := gather(`count:25`)
		if jsonString(t, first, "stopReason") != "COUNT_REACHED" || jsonNumber(t, first, "gathered") != 25 || tierMoved(first, "CONTAINER") != 20 || tierMoved(first, "MACHINE_OUTPUT") != 5 || tierMoved(first, "BELT") != 0 {
			t.Fatalf("tier order: %v", first)
		}
		second := gather(`count:100`)
		if jsonString(t, second, "stopReason") != "AREA_EXHAUSTED" || jsonNumber(t, second, "gathered") != 14 || tierMoved(second, "MACHINE_OUTPUT") != 5 || tierMoved(second, "BELT") != 4 || tierMoved(second, "LOOSE") != 5 || jsonNumber(t, second, "failed") != 0 {
			t.Fatalf("drain the rest without the infinity chest: %v", second)
		}
		if got := holds("iron-plate"); got != 39 {
			t.Fatalf("actor plates=%v, want 39", got)
		}
		third := gather(`count:10,includeInfinityContainers:true`)
		if jsonString(t, third, "stopReason") != "COUNT_REACHED" || tierMoved(third, "CONTAINER") != 10 {
			t.Fatalf("infinity opt-in: %v", third)
		}
		if got := holds("iron-plate"); got != 49 {
			t.Fatalf("actor plates=%v, want 49", got)
		}
	})

	t.Run("SaveShutdown", func(t *testing.T) {
		h.mutation(t, `mutation { save { response } }`)
		save := filepath.Join(h.dir, "data", "saves", "sandbox.zip")
		waitFor(t, 30*time.Second, func() bool {
			info, err := os.Stat(save)
			return err == nil && info.Size() > 0
		}, func() string { return "save not created at " + save + "\n" + h.logTail() })
		h.shutdown(t)
		if connection, err := net.DialTimeout("tcp", h.addr, 200*time.Millisecond); err == nil {
			connection.Close()
			t.Fatalf("RCON still accepts connections after shutdown: %s", h.addr)
		}
	})
}

type integrationCharacter struct {
	id   string
	x, y float64
}

func startIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	binary := os.Getenv("FACTORIO_BIN")
	if binary == "" {
		binary = "/Applications/factorio.app/Contents/MacOS/factorio"
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("real Factorio integration test requires FACTORIO_BIN: %v", err)
	}

	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := factorioSandboxScenario(binary)
	if err != nil {
		t.Skip(err)
	}
	mod := filepath.Join(repo, "mods", "factorio_ai_navigation")
	if _, err := os.Stat(filepath.Join(mod, "info.json")); err != nil {
		t.Skipf("navigation mod is absent: %v", err)
	}

	dir := t.TempDir()
	scenarioDest := filepath.Join(dir, "data", "scenarios", "sandbox")
	if err := os.MkdirAll(filepath.Dir(scenarioDest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(scenarioDest, os.DirFS(scenario)); err != nil {
		t.Fatalf("copy sandbox scenario: %v", err)
	}
	mods := filepath.Join(dir, "mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	// The mod directory name must carry the info.json version, so read it
	// instead of hard-coding a version that drifts with mod releases.
	infoBody, err := os.ReadFile(filepath.Join(mod, "info.json"))
	if err != nil {
		t.Fatalf("read navigation mod info.json: %v", err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(infoBody, &info); err != nil || info.Version == "" {
		t.Fatalf("decode navigation mod info.json: %v", err)
	}
	if err := os.Symlink(mod, filepath.Join(mods, "factorio_ai_navigation_"+info.Version)); err != nil {
		t.Fatalf("link navigation mod: %v", err)
	}

	for name, extra := range integrationExtraMods {
		if err := os.Symlink(extra, filepath.Join(mods, name)); err != nil {
			t.Fatalf("link extra mod %s: %v", name, err)
		}
	}

	config := filepath.Join(dir, "config.ini")
	configBody := fmt.Sprintf("[path]\nread-data=__PATH__system-read-data__\nwrite-data=%s\n", filepath.Join(dir, "data"))
	if err := os.WriteFile(config, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(passwordBytes)
	gamePort := freeUDPPort(t)
	rconPort := freeTCPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", rconPort)
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	versionOutput, _ := exec.Command(binary, "--version").CombinedOutput()
	version := strings.TrimSpace(strings.SplitN(string(versionOutput), "\n", 2)[0])

	cmd := exec.Command(binary,
		"-c", config,
		"--mod-directory", mods,
		"--start-server-load-scenario", "sandbox",
		"--map-gen-settings", filepath.Join(repo, "map-gen-settings-sandbox.json"),
		"--map-settings", filepath.Join(repo, "map-settings.json"),
		"--bind", fmt.Sprintf("127.0.0.1:%d", gamePort),
		"--rcon-bind", addr,
		"--rcon-password", password,
		"--server-settings", filepath.Join(repo, "server-settings.json"),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start Factorio: %v", err)
	}
	h := &integrationHarness{
		binary: binary, version: version, dir: dir, addr: addr, password: password,
		cmd: cmd, logFile: logFile, wait: make(chan error, 1),
	}
	go func() { h.wait <- cmd.Wait() }()

	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-h.wait:
			h.stopped = true
			logFile.Close()
			t.Fatalf("Factorio exited before RCON readiness: %v\n%s", err, h.logTail())
		default:
		}
		body, _ := os.ReadFile(logPath)
		if bytes.Contains(body, []byte("Starting RCON interface")) {
			break
		}
		if time.Now().After(deadline) {
			h.forceStop()
			t.Fatalf("Factorio RCON did not become ready within 60s\n%s", h.logTail())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return h
}

func factorioSandboxScenario(binary string) (string, error) {
	root := filepath.Dir(binary)
	candidates := []string{
		filepath.Join(root, "..", "data", "base", "scenarios", "sandbox"),
		filepath.Join(root, "..", "..", "data", "base", "scenarios", "sandbox"),
		filepath.Join(root, "..", "..", "..", "data", "base", "scenarios", "sandbox"),
	}
	if data := os.Getenv("FACTORIO_DATA"); data != "" {
		candidates = append([]string{filepath.Join(data, "base", "scenarios", "sandbox")}, candidates...)
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(filepath.Join(candidate, "control.lua")); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("sandbox scenario not found beside %s; set FACTORIO_DATA to Factorio's data directory", binary)
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.LocalAddr().(*net.UDPAddr).Port
}

// firstCellErrorMessage returns the first step error message a buildCell result
// carries, so a test can pin which preflight rule refused the cell.
func firstCellErrorMessage(t *testing.T, result map[string]any) string {
	t.Helper()
	steps := jsonArray(t, result, "steps")
	if len(steps) == 0 {
		t.Fatalf("buildCell result has no steps: %v", result)
	}
	for _, raw := range steps {
		step := raw.(map[string]any)
		for _, rawError := range jsonArray(t, step, "errors") {
			if message := jsonString(t, rawError.(map[string]any), "message"); message != "" {
				return message
			}
		}
	}
	t.Fatalf("buildCell result carries no step errors: %v", result)
	return ""
}

func (h *integrationHarness) query(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "query", document)
}

func (h *integrationHarness) mutation(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "mutation", document)
}

// graphqlExpectingError runs a document that is supposed to fail and returns the
// command error instead of failing the test, so a negative integration case can
// assert on the message.
func (h *integrationHarness) graphqlExpectingError(t *testing.T, operation, document string) error {
	t.Helper()
	var out bytes.Buffer
	return runGraphQLInProcess([]string{"--addr", h.addr, operation, "--format", "json", document}, &out, h.password)
}

func (h *integrationHarness) graphql(t *testing.T, operation, document string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"--addr", h.addr, operation, "--format", "json", document}, &out, h.password)
	if err != nil {
		t.Fatalf("real Factorio GraphQL failed\noperation: %s\ndocument:\n%s\nendpoint: %s\ntick: %v\noutput: %s\nerror: %v", operation, document, h.addr, h.bestEffortTick(), out.String(), err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid GraphQL JSON for document\n%s\noutput: %q\nerror: %v", document, out.String(), err)
	}
	return result
}

func (h *integrationHarness) bestEffortTick() any {
	if h.isStopped() {
		return "server-stopped"
	}
	var out bytes.Buffer
	if err := runGraphQLInProcess([]string{"--addr", h.addr, "query", "--format", "json", `{ status { tick } }`}, &out, h.password); err != nil {
		return "unavailable: " + err.Error()
	}
	var result map[string]any
	if json.Unmarshal(out.Bytes(), &result) != nil {
		return "unavailable"
	}
	data, _ := result["data"].(map[string]any)
	status, _ := data["status"].(map[string]any)
	return status["tick"]
}

func (h *integrationHarness) status(t *testing.T) float64 {
	t.Helper()
	result := h.query(t, `{ status { tick } }`)
	return jsonNumber(t, jsonObject(t, result, "data", "status"), "tick")
}

func (h *integrationHarness) lua(t *testing.T, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := executeContext(ctx, h.addr, h.password, "/silent-command "+script, false)
	if err != nil {
		t.Fatalf("real Factorio fixture Lua failed\nscript: %s\nendpoint: %s\ntick: %v\nerror: %v", script, h.addr, h.bestEffortTick(), err)
	}
	return result
}

func (h *integrationHarness) createTestSurface(t *testing.T) {
	t.Helper()
	h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
if not s then s=game.create_surface("`+integrationSurface+`",{width=64,height=64}) end
s.request_to_generate_chunks({0,0},2)
s.force_generate_chunk_requests()
for _,e in pairs(s.find_entities()) do if e.valid then e.destroy() end end
local tiles={}
for x=-32,31 do for y=-32,31 do tiles[#tiles+1]={name="lab-dark-1",position={x,y}} end end
s.set_tiles(tiles,true)
rcon.print("ok")`)
}

func (h *integrationHarness) spawnCharacter(t *testing.T) integrationCharacter {
	t.Helper()
	result := h.mutation(t, `mutation {
	  characterSpawn(input:{surface:"`+integrationSurface+`",position:{x:0,y:0},force:"player"}) {
	    character position { x y }
	  }
	}`)
	spawn := jsonObject(t, jsonObject(t, result, "data"), "characterSpawn")
	position := jsonObject(t, spawn, "position")
	return integrationCharacter{id: jsonString(t, spawn, "character"), x: jsonNumber(t, position, "x"), y: jsonNumber(t, position, "y")}
}

func (h *integrationHarness) destroyCharacter(t *testing.T, id string) {
	t.Helper()
	if h.isStopped() {
		return
	}
	h.lua(t, `local e=game.get_entity_by_unit_number(`+id+`)
if e and e.valid then e.destroy() end
rcon.print("ok")`)
}

func (h *integrationHarness) grantItems(t *testing.T, character string, items map[string]int) {
	t.Helper()
	var inserts strings.Builder
	for name, count := range items {
		fmt.Fprintf(&inserts, "\nassert(inv.insert{name=%q,count=%d}==%d)", name, count, count)
	}
	h.lua(t, `local e=game.get_entity_by_unit_number(`+character+`)
assert(e and e.valid and e.type=="character")
local inv=e.get_inventory(defines.inventory.character_main)`+inserts.String()+`
rcon.print("ok")`)
}

func (h *integrationHarness) placeLamp(t *testing.T, character string, x, y float64) string {
	t.Helper()
	result := h.mutation(t, fmt.Sprintf(`mutation {
	  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:%g},item:"small-lamp"}) { unitNumber }
	}`, character, integrationSurface, x, y))
	return jsonString(t, jsonObject(t, jsonObject(t, result, "data"), "place"), "unitNumber")
}

// place puts one entity through the place mutation and returns its unit number.
func (h *integrationHarness) place(t *testing.T, character, item string, x, y float64) string {
	t.Helper()
	result := h.mutation(t, fmt.Sprintf(`mutation {
	  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:%g},item:%q}) { unitNumber }
	}`, character, integrationSurface, x, y, item))
	return jsonString(t, jsonObject(t, jsonObject(t, result, "data"), "place"), "unitNumber")
}

func (h *integrationHarness) removeEntity(t *testing.T, character, entity, unit string) {
	t.Helper()
	if h.isStopped() {
		return
	}
	h.mutation(t, fmt.Sprintf(`mutation {
	  remove(input:{actor:{character:%q},target:{surface:%q,entity:%q,unitNumber:%q}}) { entity }
	}`, character, integrationSurface, entity, unit))
}

func (h *integrationHarness) entityCount(t *testing.T, name string) float64 {
	t.Helper()
	result := h.query(t, fmt.Sprintf(`{
	  entities(input:{surface:%q,area:{leftTopX:-16,leftTopY:-16,rightBottomX:16,rightBottomY:16},name:%q,limit:1000}) { pageInfo { total } }
	}`, integrationSurface, name))
	return jsonNumber(t, jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "entities"), "pageInfo"), "total")
}

func (h *integrationHarness) inspectSettings(t *testing.T, character, unit string) map[string]any {
	t.Helper()
	result := h.query(t, fmt.Sprintf(`{
	  inspect(input:{actor:{character:%q},target:{surface:%q,entity:"small-lamp",unitNumber:%q}}) {
	    settings {
	      lamp { color { r g b a } alwaysOn useColors colorMode }
	      circuit { enabled useColors colorMode }
	    }
	  }
	}`, character, integrationSurface, unit))
	return jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "inspect"), "settings")
}

func (h *integrationHarness) shutdown(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()
	h.mutation(t, `mutation { shutdown { response } }`)
	select {
	case err := <-h.wait:
		h.mu.Lock()
		h.stopped = true
		h.mu.Unlock()
		if h.logFile != nil {
			_ = h.logFile.Close()
		}
		if err != nil && !strings.Contains(err.Error(), "exit status 255") {
			t.Fatalf("Factorio shutdown exit: %v\n%s", err, h.logTail())
		}
	case <-time.After(30 * time.Second):
		h.forceStop()
		t.Fatalf("Factorio did not exit within 30s after shutdown\n%s", h.logTail())
	}
}

func (h *integrationHarness) close(t *testing.T) {
	t.Helper()
	if !h.isStopped() {
		h.shutdown(t)
	}
}

func (h *integrationHarness) isStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

func (h *integrationHarness) forceStop() {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	h.mu.Unlock()
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(os.Interrupt)
		select {
		case <-h.wait:
		case <-time.After(5 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.wait
		}
	}
	if h.logFile != nil {
		_ = h.logFile.Close()
	}
}

func (h *integrationHarness) logTail() string {
	body, err := os.ReadFile(filepath.Join(h.dir, "server.log"))
	if err != nil {
		return err.Error()
	}
	if len(body) > 8000 {
		body = body[len(body)-8000:]
	}
	return string(body)
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool, failure func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(failure())
}

func jsonObject(t *testing.T, object map[string]any, path ...string) map[string]any {
	t.Helper()
	var current any = object
	for _, key := range path {
		parent, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL path %v has non-object parent %T: %v", path, current, current)
		}
		current, ok = parent[key]
		if !ok {
			t.Fatalf("GraphQL path %v missing key %q in %v", path, key, parent)
		}
	}
	result, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL path %v is %T, want object: %v", path, current, current)
	}
	return result
}

func jsonArray(t *testing.T, object map[string]any, path ...string) []any {
	t.Helper()
	var current any = object
	for _, key := range path {
		parent, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL path %v has non-object parent %T: %v", path, current, current)
		}
		current, ok = parent[key]
		if !ok {
			t.Fatalf("GraphQL path %v missing key %q in %v", path, key, parent)
		}
	}
	result, ok := current.([]any)
	if !ok {
		t.Fatalf("GraphQL path %v is %T, want array: %v", path, current, current)
	}
	return result
}

func jsonString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want string: %v", key, object[key], object)
	}
	return value
}

func jsonNumber(t *testing.T, object map[string]any, key string) float64 {
	t.Helper()
	value, ok := object[key].(float64)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want number: %v", key, object[key], object)
	}
	return value
}

// luaBounds converts the GraphQL area literal the tests share into the Lua
// bounding-box table find_entities_filtered expects. Interpolating the GraphQL
// form into Lua is a syntax error that RCON reports as ordinary text, which
// silently turned every cleanup into a no-op.
func luaBounds(area string) string {
	numbers := regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`).FindAllString(area, -1)
	if len(numbers) != 4 {
		panic("area literal needs four numbers: " + area)
	}
	return fmt.Sprintf("{{%s,%s},{%s,%s}}", numbers[0], numbers[1], numbers[2], numbers[3])
}

func legendHasName(legend []any, name string) bool {
	for _, raw := range legend {
		if entry, ok := raw.(map[string]any); ok {
			if value, ok := entry["name"].(string); ok && value == name {
				return true
			}
		}
	}
	return false
}

func jsonBool(t *testing.T, object map[string]any, key string) bool {
	t.Helper()
	value, ok := object[key].(bool)
	if !ok {
		t.Fatalf("GraphQL field %q is %T, want bool: %v", key, object[key], object)
	}
	return value
}

func hasItem(items []any, name string, count float64) bool {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["name"] == name && item["count"] == count {
			return true
		}
	}
	return false
}
