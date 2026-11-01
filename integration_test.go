//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		    position:{x:7.5,y:-3.25},text:"`+markerText+`",icon:{type:"item",name:"iron-plate"}}) {
		    force surface tag { tagNumber text position { x y } icon { type name quality } }
		  }
		}`)
		tag := jsonObject(t, jsonObject(t, jsonObject(t, added, "data"), "addChartTag"), "tag")
		tagNumber := jsonString(t, tag, "tagNumber")
		if jsonString(t, tag, "text") != markerText {
			t.Fatalf("added chart tag=%v, want text %q", tag, markerText)
		}
		icon := jsonObject(t, tag, "icon")
		if jsonString(t, icon, "type") != "item" || jsonString(t, icon, "name") != "iron-plate" {
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
		  setLampSettings(input:{actor:{character:%q},target:{surface:%q,entity:"small-lamp",unitNumber:%q},color:{r:0.25,g:0.5,b:0.75,a:1},alwaysOn:true,useColors:false,colorMode:0}) {
		    tick lamp { color { r g b a } alwaysOn useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		settings := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setLampSettings"), "lamp")
		if !jsonBool(t, settings, "alwaysOn") || jsonBool(t, settings, "useColors") || jsonNumber(t, settings, "colorMode") != 0 {
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
		    color connected redConnections greenConnections
		  }
		}`, integrationSurface, unit))
		networkResult := jsonObject(t, jsonObject(t, network, "data"), "circuitNetwork")
		if !jsonBool(t, networkResult, "connected") || jsonString(t, networkResult, "color") != "RED" || jsonNumber(t, networkResult, "redConnections") < 1 {
			t.Fatalf("circuitNetwork = %v, want connected RED with >=1 connection", networkResult)
		}

		result := h.mutation(t, fmt.Sprintf(`mutation {
		  setCircuitBehavior(input:{actor:{character:%q},target:{surface:%q,entity:"small-lamp",unitNumber:%q},useColors:true,colorMode:1}) {
		    circuit { useColors colorMode }
		  }
		}`, character.id, integrationSurface, unit))
		circuit := jsonObject(t, jsonObject(t, jsonObject(t, result, "data"), "setCircuitBehavior"), "circuit")
		if !jsonBool(t, circuit, "useColors") || jsonNumber(t, circuit, "colorMode") != 1 {
			t.Fatalf("unexpected post-write circuit behavior: %v", circuit)
		}
		readback := jsonObject(t, h.inspectSettings(t, character.id, unit), "circuit")
		if !jsonBool(t, readback, "useColors") || jsonNumber(t, readback, "colorMode") != 1 {
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

	t.Run("DeconstructionInstantCountsAndItems", func(t *testing.T) {
		character := h.spawnCharacter(t)
		t.Cleanup(func() { h.destroyCharacter(t, character.id) })
		h.grantItems(t, character.id, map[string]int{"transport-belt": 3})
		const beltX, beltY = 6.0, 10.0
		for i := 0; i < 3; i++ {
			h.mutation(t, fmt.Sprintf(`mutation {
			  place(input:{actor:{character:%q},surface:%q,position:{x:%g,y:%g},item:"transport-belt"}) { unitNumber }
			}`, character.id, integrationSurface, beltX+float64(i), beltY))
		}
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
		  place(input:{actor:{character:%q},surface:%q,position:{x:16,y:4},direction:4,item:"underground-belt"}) { tick unitNumber direction }
		}`, character.id, integrationSurface))
		firstResult := jsonObject(t, jsonObject(t, first, "data"), "place")
		firstUnit := jsonString(t, firstResult, "unitNumber")
		if got := jsonNumber(t, firstResult, "direction"); got != 4 {
			t.Fatalf("place direction=%v, want 4", got)
		}
		second := h.mutation(t, fmt.Sprintf(`mutation {
		  place(input:{actor:{character:%q},surface:%q,position:{x:19,y:4},direction:4,item:"underground-belt"}) { tick unitNumber direction }
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
		  place(input:{actor:{character:%q},surface:%q,position:{x:-3.5,y:-14.5},item:"pipe"}) { unitNumber }
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

	t.Run("WaitUntilTick", func(t *testing.T) {
		current := h.status(t)
		target := current + 20
		started := time.Now()
		result := h.mutation(t, fmt.Sprintf(`mutation {
		  waitUntil(input:{timeoutSeconds:5,pollSeconds:0.05,condition:{tick:%g}}) {
		    tick condition satisfied timedOut polls waitedSeconds
		    last { tick count detail }
		  }
		}`, target))
		wait := jsonObject(t, jsonObject(t, result, "data"), "waitUntil")
		if !jsonBool(t, wait, "satisfied") || jsonBool(t, wait, "timedOut") {
			t.Fatalf("tick wait: %v", wait)
		}
		if condition := jsonString(t, wait, "condition"); condition != "TICK" {
			t.Fatalf("condition=%q, want TICK", condition)
		}
		if tick := jsonNumber(t, wait, "tick"); tick < target {
			t.Fatalf("satisfied at tick=%v before target=%v", tick, target)
		}
		if polls := jsonNumber(t, wait, "polls"); polls < 1 {
			t.Fatalf("polls=%v", polls)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("tick wait took %v", elapsed)
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
	if err := os.Symlink(mod, filepath.Join(mods, "factorio_ai_navigation_1.0.0")); err != nil {
		t.Fatalf("link navigation mod: %v", err)
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

func (h *integrationHarness) query(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "query", document)
}

func (h *integrationHarness) mutation(t *testing.T, document string) map[string]any {
	t.Helper()
	return h.graphql(t, "mutation", document)
}

func (h *integrationHarness) graphql(t *testing.T, operation, document string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	err := runWithCredentials([]string{"--addr", h.addr, operation, "--format", "json", document}, &out, h.password)
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
	if err := runWithCredentials([]string{"--addr", h.addr, "query", "--format", "json", `{ status { tick } }`}, &out, h.password); err != nil {
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
