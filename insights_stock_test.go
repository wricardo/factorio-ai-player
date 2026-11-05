package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Exercise the shared belt-line reader against engine-identity fixtures.
// get_contents reports one tile's items, so lines of different entities must all
// count while one entity exposing the same internal line twice counts once.
func TestStockBeltLines(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
local function line(items, identity)
  return {identity=identity, get_contents=function() return items end,
    line_equals=function(other) return other.identity==identity end}
end
local function entity(lines)
  local e={lines=lines}
  e.get_max_transport_line_index=function() return #e.lines end
  e.get_transport_line=function(index) return e.lines[index] end
  return e
end
local first=entity({line({{name="coal",count=4}},"a"),line({{name="iron-plate",count=2}},"b")})
local second=entity({line({{name="coal",count=4}},"c"),line({{name="iron-plate",count=2}},"d")})
local repeated=entity({line({{name="stone",count=3}},"dup"),line({{name="stone",count=3}},"dup")})
local three=entity({line({{name="coal",count=1}},"x"),line({{name="coal",count=1}},"y"),line({{name="coal",count=1}},"z")})
`
	assertions := `
local function never() return true end
local a=stock_belt_lines(first,never)
assert(#a==2, "two distinct lines kept, got "..#a)
assert(a[1].index==1 and a[2].index==2)
local b=stock_belt_lines(second,never)
assert(#b==2, "identical content on another entity is not a duplicate, got "..#b)
local r=stock_belt_lines(repeated,never)
assert(#r==1, "one entity exposing the same line twice counts once, got "..#r)
assert(r[1].line.get_contents()[1].count==3)
local calls=0
local limited=stock_belt_lines(three,function()
  calls=calls+1
  if calls>1 then return false end
  return true
end)
assert(#limited==2, "budget exhaustion keeps the lines read so far, got "..#limited)
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + stockBeltLineLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stock belt line regression: %v\n%s", err, output)
	}
}

func TestStockCategoryName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		category *StockCategory
		want     string
		wantErr  bool
	}{
		{"unfiltered", nil, "", false},
		{"stored", stockCategoryPointer(StockCategoryStored), "stored", false},
		{"in-transit", stockCategoryPointer(StockCategoryInTransit), "in-transit", false},
		{"ground", stockCategoryPointer(StockCategoryGround), "ground", false},
		{"player", stockCategoryPointer(StockCategoryPlayer), "player", false},
		{"worker", stockCategoryPointer(StockCategoryWorker), "worker", false},
		{"all", stockCategoryPointer(StockCategoryAll), "", false},
		{"unknown", stockCategoryPointer(StockCategory("BOGUS")), "", true},
	} {
		got, err := stockCategoryName(tc.category)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error", tc.name)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func stockCategoryPointer(value StockCategory) *StockCategory { return &value }

func TestStockCategoryFilterLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","location_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"locations":{},"total_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"totals":{},"item_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"items":{}}`)
	category := StockCategoryStored
	result, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player", Category: &category, Limit: 10, TotalsLimit: 10, ItemsLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tick != 1 {
		t.Fatalf("tick=%v", result.Tick)
	}
	lua := <-commands
	for _, want := range []string{
		"local surface_name, force_name, item, category_filter = " + luaString("nauvis") + ", " + luaString("player") + `, "", ` + luaString("stored"),
		`if category_filter ~= "" and category ~= category_filter then return end`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("stock Lua missing %q\n%s", want, lua)
		}
	}
}

// Detached characters are counted under WORKER so courier and builder workers
// stop being invisible to factory-wide stock totals.
func TestStockCountsDetachedWorkers(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player",
	  "locationPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "locations":[{"category":"worker","entityName":"character","entityType":"character","unitNumber":"70",
	    "position":{"x":42.7,"y":-4},"inventoryKind":"main","stack":{"name":"iron-plate","quality":"normal","count":68}}],
	  "totalPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "totals":[{"item":"iron-plate","quality":"normal","category":"worker","count":68}],
	  "itemPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "items":[{"item":"iron-plate","quality":"normal","count":68}]}`)
	page, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player", Category: new(StockCategoryWorker),
		Limit: 100, TotalsLimit: 100, ItemsLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Locations) != 1 || page.Locations[0].Category != "worker" ||
		page.Locations[0].UnitNumber == nil || *page.Locations[0].UnitNumber != "70" {
		t.Fatalf("worker locations: %+v", page.Locations)
	}
	if len(page.Totals) != 1 || page.Totals[0].Count != 68 {
		t.Fatalf("worker totals: %+v", page.Totals)
	}
	lua := <-commands
	for _, want := range []string{
		`local surface_name, force_name, item, category_filter = `,
		`record("worker"`,
		`if character.valid and not character.player then`,
		`unitNumber=unit and tostring(unit)`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("stock Lua missing %q\n%s", want, lua)
		}
	}
}

// A burner mining drill keeps no output buffer, so Factorio returns its fuel
// inventory from get_output_inventory(). stock() must report that coal stack
// once as fuel, matching inspect(), instead of as an output stack; a furnace
// with distinct result and fuel inventories must keep both labels.
func TestStockBurnerDrillFuelIsNotOutput(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":674522,"surface":"nauvis","force":"player",
	  "locationPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"locations":[],
	  "totalPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"totals":[],
	  "itemPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"items":[]}`)
	if _, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player",
		Limit: 100, TotalsLimit: 100, ItemsLimit: 100}); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	stubs := `
local captured
rcon = {print=function(value) captured = value end}
helpers = {table_to_json=function(value) return value end}
defines = {inventory={chest=1,crafter_input=4,crafter_output=5,crafter_modules=6,
  crafter_trash=7,assembling_machine_dump=8,mining_drill_modules=9,lab_input=10,
  lab_modules=11,lab_trash=12,rocket_silo_rocket=13,rocket_silo_trash=14,
  agricultural_tower_input=15,agricultural_tower_output=16,
  asteroid_collector_output=17,logistic_container_trash=18}}
local function inv(index, contents)
  return {index=index, get_contents=function() return contents end}
end
-- A burner drill exposes the same inventory from both accessors.
local drill_fuel = inv(2, {{name="coal",quality="normal",count=5}})
local drill = {name="burner-mining-drill", type="mining-drill", unit_number=11,
  position={x=-96,y=-4},
  get_inventory=function() return nil end,
  get_output_inventory=function() return drill_fuel end,
  get_fuel_inventory=function() return drill_fuel end,
  get_burnt_result_inventory=function() return nil end}
local furnace_out = inv(5, {{name="iron-plate",quality="normal",count=7}})
local furnace_fuel = inv(2, {{name="coal",quality="normal",count=3}})
local furnace = {name="stone-furnace", type="furnace", unit_number=12,
  position={x=0,y=0},
  get_inventory=function(id) if id==defines.inventory.crafter_output then return furnace_out end return nil end,
  get_output_inventory=function() return furnace_out end,
  get_fuel_inventory=function() return furnace_fuel end,
  get_burnt_result_inventory=function() return nil end}
local entities = {drill, furnace}
local surface = {name="nauvis", find_entities_filtered=function(filter)
  if filter and filter.type=="item-entity" then return {} end
  return entities
end}
game = {tick=674522, players={}, forces={player={name="player"}}, surfaces={nauvis=surface}}
`
	assertions := `
assert(captured, "stock Lua printed nothing")
assert(not captured.error, "stock Lua failed: "..tostring(captured.error))
local seen, coal = {}, 0
for _, row in ipairs(captured.locations) do
  local key = row.entityName.."/"..row.inventoryKind.."/"..row.stack.name
  seen[key] = (seen[key] or 0) + row.stack.count
  if row.stack.name=="coal" then coal = coal + row.stack.count end
end
assert(seen["burner-mining-drill/fuel/coal"]==5, "drill coal must be fuel 5, got "..tostring(seen["burner-mining-drill/fuel/coal"]))
assert(seen["burner-mining-drill/output/coal"]==nil, "drill coal must not be labelled output")
assert(seen["stone-furnace/output/iron-plate"]==7, "furnace result must stay output, got "..tostring(seen["stone-furnace/output/iron-plate"]))
assert(seen["stone-furnace/fuel/coal"]==3, "furnace fuel must stay fuel, got "..tostring(seen["stone-furnace/fuel/coal"]))
assert(coal==8, "each coal stack counts once, got "..coal)
for _, total in ipairs(captured.totals) do
  if total.item=="coal" then assert(total.count==8, "coal total must count each stack once, got "..total.count) end
end
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(stubs + strings.TrimPrefix(source, "/silent-command ") + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stock burner drill fixture: %v\n%s\n%s", err, output, source)
	}
}

// Regression for a rigged-bug report: an item filter returned chests that hold a
// different item, and container rows carried no unitNumber, so a row could only
// be attributed to a chest by position. The filter compares each stack name with
// the requested item, and every entity row must now carry its unit number.
func TestStockItemFilterKeepsOnlyHoldersAndNamesContainers(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":9,"surface":"nauvis","force":"player",
	  "locationPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"locations":[],
	  "totalPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"totals":[],
	  "itemPageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"items":[]}`)
	item := "iron-plate"
	if _, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player", Item: &item,
		Limit: 100, TotalsLimit: 100, ItemsLimit: 100}); err != nil {
		t.Fatal(err)
	}
	source := <-commands
	stubs := `
local captured
rcon = {print=function(value) captured = value end}
helpers = {table_to_json=function(value) return value end}
defines = {inventory={chest=1,logistic_container_trash=2,crafter_input=3,crafter_output=4,
  crafter_modules=5,crafter_trash=6,assembling_machine_dump=7,mining_drill_modules=8,
  lab_input=9,lab_modules=10,lab_trash=11,rocket_silo_rocket=12,rocket_silo_trash=13,
  agricultural_tower_input=14,agricultural_tower_output=15,asteroid_collector_output=16,
  furnace_source=17,furnace_result=18}}
local function inv(index, contents)
  return {index=index, get_contents=function() return contents end}
end
local copper_only = inv(1, {{name="copper-plate",quality="normal",count=256}})
local mixed = inv(1, {{name="copper-plate",quality="normal",count=1150},
  {name="iron-plate",quality="normal",count=12}})
local furnace_out = inv(4, {{name="iron-plate",quality="normal",count=7}})
local furnace_fuel = inv(18, {{name="coal",quality="normal",count=3}})
local entities = {
  {name="iron-chest", type="container", unit_number=1447, position={x=15.5,y=19.5},
    get_inventory=function(id) if id==defines.inventory.chest then return copper_only end end,
    get_output_inventory=function() return nil end,
    get_fuel_inventory=function() return nil end,
    get_burnt_result_inventory=function() return nil end},
  {name="iron-chest", type="container", unit_number=1244, position={x=-54.5,y=88.5},
    get_inventory=function(id) if id==defines.inventory.chest then return mixed end end,
    get_output_inventory=function() return nil end,
    get_fuel_inventory=function() return nil end,
    get_burnt_result_inventory=function() return nil end},
  {name="stone-furnace", type="furnace", unit_number=5, position={x=-54,y=72},
    get_inventory=function(id)
      if id==defines.inventory.crafter_input then return nil end
      if id==defines.inventory.crafter_output then return furnace_out end
      return nil end,
    get_output_inventory=function() return furnace_out end,
    get_fuel_inventory=function() return furnace_fuel end,
    get_burnt_result_inventory=function() return nil end},
}
local surface = {name="nauvis", find_entities_filtered=function(filter)
  if filter and filter.type=="item-entity" then return {} end
  return entities
end}
game = {tick=9, players={}, forces={player={name="player"}}, surfaces={nauvis=surface}}
`
	assertions := `
assert(captured, "stock Lua printed nothing")
assert(not captured.error, "stock Lua failed: "..tostring(captured.error))
local rows = {}
for _, row in ipairs(captured.locations) do
  rows[#rows+1] = row.entityName.."/"..tostring(row.unitNumber).."/"..row.inventoryKind.."/"..row.stack.name.."/"..row.stack.count
end
table.sort(rows)
local want = {"iron-chest/1244/chest/iron-plate/12", "stone-furnace/5/output/iron-plate/7"}
assert(#rows==#want, "only iron-plate holders stay, got "..table.concat(rows, ", "))
for index, line in ipairs(want) do
  assert(rows[index]==line, "row "..index.." is "..rows[index]..", want "..line)
end
local sum = 0
for _, total in ipairs(captured.totals) do
  assert(total.item=="iron-plate", "totals must stay filtered, got "..total.item)
  sum = sum + total.count
end
assert(sum==19, "iron-plate total is 19, got "..sum)
for _, item_total in ipairs(captured.items) do
  assert(item_total.item=="iron-plate", "items must stay filtered, got "..item_total.item)
end
`
	// The copper side of the same snapshot: the mixed chest must keep only its
	// copper stack and the copper-only chest must appear with its unit number.
	copperSource := strings.Replace(source, luaString("iron-plate"), luaString("copper-plate"), 1)
	copperAssertions := `
assert(captured, "stock Lua printed nothing")
assert(not captured.error, "stock Lua failed: "..tostring(captured.error))
local rows = {}
for _, row in ipairs(captured.locations) do
  rows[#rows+1] = row.entityName.."/"..tostring(row.unitNumber).."/"..row.inventoryKind.."/"..row.stack.name.."/"..row.stack.count
end
table.sort(rows)
assert(#rows==2, "two copper plate holders, got "..table.concat(rows, ", "))
assert(rows[1]=="iron-chest/1244/chest/copper-plate/1150", "mixed chest row is "..rows[1])
assert(rows[2]=="iron-chest/1447/chest/copper-plate/256", "copper chest row is "..rows[2])
local sum = 0
for _, total in ipairs(captured.totals) do
  assert(total.item=="copper-plate", "totals must stay filtered, got "..total.item)
  sum = sum + total.count
end
assert(sum==1406, "copper-plate total is 1406, got "..sum)
`
	runLuaFixture(t, source, stubs, assertions)
	runLuaFixture(t, copperSource, stubs, copperAssertions)
}

// Every row the engine can address by unit number must carry it: a chest row
// without one cannot be attributed to a chest except by position, which is how
// a row got blamed on the wrong chest.
func TestStockRowsPassUnitNumbers(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":2,"surface":"nauvis","force":"player",
	  "locationPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "locations":[{"category":"stored","entityName":"iron-chest","entityType":"container","unitNumber":"1447",
	    "position":{"x":15.5,"y":19.5},"inventoryKind":"chest","stack":{"name":"copper-plate","quality":"normal","count":256}}],
	  "totalPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "totals":[{"item":"copper-plate","quality":"normal","category":"stored","count":256}],
	  "itemPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "items":[{"item":"copper-plate","quality":"normal","count":256}]}`)
	item := "copper-plate"
	page, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player", Item: &item,
		Limit: 100, TotalsLimit: 100, ItemsLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Locations) != 1 || page.Locations[0].UnitNumber == nil || *page.Locations[0].UnitNumber != "1447" {
		t.Fatalf("container row unit number: %+v", page.Locations)
	}
	lua := <-commands
	for _, want := range []string{
		`record("stored", entity.name, entity.type, nil, entity.position, kind, inv.get_contents(), entity.unit_number)`,
		`"held", {{name=stack.name, quality=stack.quality.name, count=stack.count}}, entity.unit_number)`,
		`"line-"..entry.index, entry.line.get_contents(), entity.unit_number)`,
		`"ground", {{name=stack.name, quality=stack.quality.name, count=stack.count}}, entity.unit_number)`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("stock Lua missing %q\n%s", want, lua)
		}
	}
}
