package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

const ffProtoCanned = `{"entities":{
"assembling-machine-2":{"type":"assembling-machine","tile_width":3,"tile_height":3,"supports_direction":false,"place_item":"assembling-machine-2","module_inventory_size":2,"crafting_categories":{"crafting":true}},
"fast-inserter":{"type":"inserter","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"fast-inserter","filter_count":5,"crafting_categories":{}},
"burner-inserter":{"type":"inserter","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"burner-inserter","filter_count":0,"crafting_categories":{}},
"splitter":{"type":"splitter","tile_width":2,"tile_height":1,"supports_direction":true,"place_item":"splitter","crafting_categories":{}},
"transport-belt":{"type":"transport-belt","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"transport-belt","crafting_categories":{}},
"underground-belt":{"type":"underground-belt","tile_width":1,"tile_height":1,"supports_direction":true,"place_item":"underground-belt","crafting_categories":{}},
"wooden-chest":{"type":"container","tile_width":1,"tile_height":1,"place_item":"wooden-chest","crafting_categories":{}},
"stone-furnace":{"type":"furnace","tile_width":2,"tile_height":2,"place_item":"stone-furnace","module_inventory_size":0,"crafting_categories":{"smelting":true}}},
"recipes":{"electronic-circuit":true},
"items":{"speed-module":true,"copper-cable":true,"iron-ore":true},
"qualities":{"normal":true,"rare":true}}`

func ffTestNormalize(t *testing.T, src string) (*ffNormalized, hcl.Diagnostics) {
	t.Helper()
	m, d := ParseFactorioform(map[string][]byte{"t.ftf": []byte("factorioform {\n version = 1\n name = \"t\"\n}\n" + src)}, nil)
	if d.HasErrors() {
		t.Fatalf("parse: %v", d)
	}
	insts, d := ffStaticInstances(m)
	if d.HasErrors() {
		t.Fatalf("instances: %v", d)
	}
	var p ffPrototypes
	if err := jsonUnmarshalString(ffProtoCanned, &p); err != nil {
		t.Fatal(err)
	}
	return ffNormalize(insts, m.Wires, &p)
}

func ffExpectErr(t *testing.T, src string, parts ...string) {
	t.Helper()
	out, d := ffTestNormalize(t, src)
	if out != nil || !d.HasErrors() {
		t.Fatalf("expected error containing %v, got out=%v diags=%v", parts, out, d)
	}
	for _, p := range parts {
		if !strings.Contains(d.Error(), p) {
			t.Fatalf("error %q lacks %q", d.Error(), p)
		}
	}
}

func TestFFNormalizeAssemblerSplitterBelts(t *testing.T) {
	out, d := ffTestNormalize(t, `
resource "assembler" "green" {
  entity = "assembling-machine-2"
  position = { x = 10.5, y = 10.5 }
  quality = "rare"
  recipe = "electronic-circuit"
  modules = ["speed-module", "speed-module@rare"]
}
resource "splitter" "sort" {
  entity = "splitter"
  position = { x = 20.5, y = 5 }
  direction = direction.east
  splitter {
    filter = "iron-ore"
    output_priority = "right"
  }
}
resource "inserter" "feed" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  inserter { filters = ["copper-cable"] }
  circuit { set_filters = true }
}
resource "belt" "bus" {
  belt {
    entity = "transport-belt"
    path = [{ x = 0.5, y = 3.5, direction = direction.north }, { x = 0.5, y = 2.5, direction = direction.north }]
  }
}
wire {
  color = "red"
  from = resource.inserter.feed
  to = resource.assembler.green
}
`)
	if d.HasErrors() {
		t.Fatal(d)
	}
	var addrs []string
	by := map[string]ffResource{}
	for _, r := range out.Resources {
		addrs = append(addrs, r.Address)
		by[r.Address] = r
	}
	if strings.Join(addrs, ",") != "assembler.green,belt.bus.path[0],belt.bus.path[1],inserter.feed,splitter.sort" {
		t.Fatalf("addresses %v", addrs)
	}
	a := by["assembler.green"]
	if a.Footprint != (ffBox{9, 9, 12, 12}) || a.Type != "assembling-machine" || a.PlaceItem != "assembling-machine-2" || a.Quality != "rare" || a.Config["recipe"] != "electronic-circuit" {
		t.Fatalf("assembler %+v", a)
	}
	mods := a.Config["modules"].([]string)
	if len(mods) != 2 || mods[0] != "speed-module:normal:1" || mods[1] != "speed-module:rare:1" {
		t.Fatalf("modules %v", mods)
	}
	s := by["splitter.sort"]
	if s.Direction == nil || *s.Direction != 4 || s.Footprint != (ffBox{20, 4, 21, 6}) || s.Config["splitter.filter"] != "iron-ore" || s.Config["splitter.output_priority"] != "right" || s.Quality != "" {
		t.Fatalf("splitter %+v", s)
	}
	in := by["inserter.feed"]
	if f := in.Config["inserter.filters"].([]string); len(f) != 1 || f[0] != "copper-cable" || in.Config["circuit.set_filters"] != true {
		t.Fatalf("inserter %+v", in)
	}
	if b := by["belt.bus.path[1]"]; b.Position != (ffPoint{0.5, 2.5}) || b.Footprint != (ffBox{0, 2, 1, 3}) || b.Entity != "transport-belt" {
		t.Fatalf("belt %+v", b)
	}
	if len(out.Wires) != 1 || out.Wires[0].Address != "wire:inserter.feed->assembler.green:red" {
		t.Fatalf("wires %+v", out.Wires)
	}
}

func TestFFNormalizeUndergroundType(t *testing.T) {
	out, d := ffTestNormalize(t, `
resource "ug" "in" {
  entity = "underground-belt"
  position = { x = 0.5, y = 0.5 }
  underground_type = "input"
}`)
	if d.HasErrors() || out.Resources[0].Config["underground_type"] != "input" {
		t.Fatalf("%v %+v", d, out)
	}
}

func TestFFNormalizeDirectionUnmanagedWhenOmitted(t *testing.T) {
	out, d := ffTestNormalize(t, `
resource "chest" "c" {
  entity = "wooden-chest"
  position = { x = 5.5, y = 0.5 }
}
resource "arm" "i" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  direction = direction.east
}`)
	if d.HasErrors() {
		t.Fatal(d)
	}
	by := map[string]ffResource{}
	for _, r := range out.Resources {
		by[r.Address] = r
	}
	chest := by["chest.c"]
	if chest.Direction != nil {
		t.Fatalf("omitted direction must be unmanaged, got %d", *chest.Direction)
	}
	arm := by["arm.i"]
	if arm.Direction == nil || *arm.Direction != 4 {
		t.Fatalf("explicit direction lost: %+v", arm.Direction)
	}
}

func TestFFNormalizeContainerInventory(t *testing.T) {
	out, d := ffTestNormalize(t, `
resource "chest" "c" {
  entity = "wooden-chest"
  position = { x = 5.5, y = 0.5 }
  container {
    inventory = "fuel"
    allowed_slots = 4
  }
}`)
	if d.HasErrors() {
		t.Fatal(d)
	}
	if got := out.Resources[0].Config["container.fuel.bar"]; got != int64(4) {
		t.Fatalf("container.fuel.bar = %#v", got)
	}
	if _, stray := out.Resources[0].Config["container.inventory"]; stray {
		t.Fatalf("inventory must not be diffed on its own: %#v", out.Resources[0].Config)
	}
}

func TestFFNormalizeContainerInventoryWithoutBarIsRejected(t *testing.T) {
	ffExpectErr(t, `resource "chest" "c" {
  entity = "wooden-chest"
  position = { x = 5.5, y = 0.5 }
  container { inventory = "fuel" }
}`, "Inert container setting", "add allowed_slots")
}

func TestFFNormalizeDirectionRequiresPrototypeSupport(t *testing.T) {
	ffExpectErr(t, `resource "a" "x" {
  entity = "assembling-machine-2"
  position = { x = 10.5, y = 10.5 }
  direction = direction.east
}`, "Direction unsupported", "does not support direction")
}

func TestFFNormalizeSplitterFilterMustBeString(t *testing.T) {
	ffExpectParseErr(t, `resource "splitter" "sort" {
  entity = "splitter"
  position = { x = 20.5, y = 5 }
  direction = direction.east
  splitter { filter = 5 }
}`, "Invalid splitter.filter", "must be a string")
}

// ffExpectParseErr asserts the manifest fails while parsing config attributes,
// which is where typed config attribute checks run.
func ffExpectParseErr(t *testing.T, src string, parts ...string) {
	t.Helper()
	_, d := ParseFactorioform(map[string][]byte{"t.ftf": []byte("factorioform {\n version = 1\n name = \"t\"\n}\n" + src)}, nil)
	if !d.HasErrors() {
		t.Fatalf("expected parse error containing %v", parts)
	}
	for _, p := range parts {
		if !strings.Contains(d.Error(), p) {
			t.Fatalf("error %q lacks %q", d.Error(), p)
		}
	}
}

func TestFFConfigAttrTypesStrict(t *testing.T) {
	// A string is never a number: no cross-type coercion.
	ffExpectParseErr(t, `resource "arm" "i" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  inserter { stack_size_override = "4" }
}`, "Invalid inserter.stack_size_override", "must be a number")
	// A bool attribute rejects a string in the same way.
	ffExpectParseErr(t, `resource "arm" "i" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  inserter { pickup_from_left_lane = "yes" }
}`, "must be true or false")
}

func TestFFConfigNestedObjectKeysMapToCamelCase(t *testing.T) {
	out, d := ffTestNormalize(t, `
resource "arm" "i" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  circuit {
    circuit_condition = { comparator = ">", constant = 5, first_signal = { name = "iron-plate" } }
  }
}`)
	if d.HasErrors() {
		t.Fatal(d)
	}
	cond, ok := out.Resources[0].Config["circuit.circuit_condition"].(map[string]any)
	if !ok {
		t.Fatalf("circuit.circuit_condition = %#v", out.Resources[0].Config)
	}
	if _, bad := cond["first_signal"]; bad {
		t.Fatalf("nested key must be camelCase for the write input: %#v", cond)
	}
	sig, ok := cond["firstSignal"].(map[string]any)
	if !ok || sig["name"] != "iron-plate" {
		t.Fatalf("firstSignal = %#v", cond["firstSignal"])
	}
	if cond["comparator"] != ">" {
		t.Fatalf("comparator = %#v", cond["comparator"])
	}
}

func TestFFConfigNestedObjectRejectsUnknownKey(t *testing.T) {
	ffExpectParseErr(t, `resource "arm" "i" {
  entity = "fast-inserter"
  position = { x = 8.5, y = 10.5 }
  circuit {
    circuit_condition = { comparator = ">", first_signl = { name = "iron-plate" } }
  }
}`, "Unsupported attribute", "first_signl")
}

func TestFFNormalizeRejections(t *testing.T) {
	ffExpectErr(t, `resource "a" "x" { 
 entity = "assembling-machine-2"
 position = { x = 10, y = 10.5 }
}`, "off the grid", "use (10.5,10.5)")
	ffExpectErr(t, `resource "s" "x" {
 entity = "splitter"
 position = { x = 20.5, y = 5 }
}`, "off the grid", "use (21,5.5)")
	ffExpectErr(t, `resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 10.5, y = 10.5 }
}
resource "a" "y" {
 entity = "assembling-machine-2"
 position = { x = 12.5, y = 10.5 }
}`, "a.x and a.y")
	ffExpectErr(t, `resource "a" "x" {
 entity = "wooden-chest"
 position = { x = 0.5, y = 0.5 }
 recipe = "electronic-circuit"
}`, "cannot have a recipe")
	ffExpectErr(t, `resource "a" "x" {
 entity = "stone-furnace"
 position = { x = 1, y = 1 }
 recipe = "electronic-circuit"
}`, "cannot have a recipe")
	ffExpectErr(t, `resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 1.5, y = 1.5 }
 recipe = "nope"
}`, `recipe "nope" does not exist`)
	ffExpectErr(t, `resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 1.5, y = 1.5 }
 modules = ["speed-module", "speed-module", "speed-module"]
}`, "3 modules requested", "2 module slots")
	ffExpectErr(t, `resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 1.5, y = 1.5 }
 modules = ["ghost-module"]
}`, `module "ghost-module" does not exist`)
	ffExpectErr(t, `resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 1.5, y = 1.5 }
 quality = "mythic"
}`, `quality "mythic" does not exist`)
	ffExpectErr(t, `resource "a" "x" {
 entity = "nope"
 position = { x = 1.5, y = 1.5 }
}`, `"nope" does not exist`)
	ffExpectErr(t, `resource "i" "x" {
 entity = "burner-inserter"
 position = { x = 1.5, y = 1.5 }
 inserter { filters = ["copper-cable"] }
}`, "no filter slots")
	ffExpectErr(t, `resource "i" "x" {
 entity = "fast-inserter"
 position = { x = 1.5, y = 1.5 }
 inserter { filters = ["a-1", "a-2", "a-3", "a-4", "a-5", "a-6"] }
}`, "6 filters", "5 filter slots")
	ffExpectErr(t, `resource "i" "x" {
 entity = "fast-inserter"
 position = { x = 1.5, y = 1.5 }
 inserter { filters = ["ghost-item"] }
}`, `filter "ghost-item" does not exist`)
	ffExpectErr(t, `resource "i" "x" {
 entity = "wooden-chest"
 position = { x = 1.5, y = 1.5 }
 inserter { filters = ["copper-cable"] }
}`, `block "inserter" requires`)
	ffExpectErr(t, `resource "i" "x" {
 entity = "fast-inserter"
 position = { x = 1.5, y = 1.5 }
 splitter { filter = "iron-ore" }
}`, `block "splitter" requires`)
	ffExpectErr(t, `resource "i" "x" {
 entity = "transport-belt"
 position = { x = 1.5, y = 1.5 }
 underground_type = "input"
}`, "underground-belt only")
	ffExpectErr(t, `resource "belt" "b" {
 belt {
  entity = "fast-inserter"
  path = [{ x = 0.5, y = 0.5, direction = 0 }]
 }
}`, "transport-belt prototype")
	ffExpectErr(t, `resource "belt" "b" {
 belt {
  entity = "transport-belt"
  path = [{ x = 0.5, y = 0.5, direction = 0 }]
 }
}
resource "a" "x" {
 entity = "wooden-chest"
 position = { x = 0.5, y = 0.5 }
}`, "belt.b.path[0]", "a.x")
}

func TestFFValidateSingleRCONCall(t *testing.T) {
	fake := newFakeRCON(t, ffProtoCanned)
	m, d := ParseFactorioform(map[string][]byte{"t.ftf": []byte(`factorioform {
 version = 1
 name = "t"
}
resource "a" "x" {
 entity = "assembling-machine-2"
 position = { x = 10.5, y = 10.5 }
 recipe = "electronic-circuit"
}
resource "i" "y" {
 entity = "fast-inserter"
 position = { x = 8.5, y = 10.5 }
 inserter { filters = ["copper-cable"] }
}
resource "belt" "b" {
 belt {
  entity = "transport-belt"
  path = [{ x = 0.5, y = 0.5, direction = 0 }, { x = 0.5, y = -0.5, direction = 0 }, { x = 0.5, y = -1.5, direction = 0 }]
 }
}
`)}, nil)
	if d.HasErrors() {
		t.Fatal(d)
	}
	insts, _ := ffStaticInstances(m)
	out, diags, err := ffValidate(context.Background(), fake.resolver, insts, m.Wires)
	if err != nil || diags.HasErrors() || len(out.Resources) != 5 {
		t.Fatalf("out=%v diags=%v err=%v", out, diags, err)
	}
	cmds := fake.commands()
	if len(cmds) != 1 {
		t.Fatalf("expected one RCON call, got %d", len(cmds))
	}
	for _, name := range []string{"assembling-machine-2", "fast-inserter", "transport-belt", "electronic-circuit", "copper-cable"} {
		if !strings.Contains(cmds[0], luaString(name)) {
			t.Fatalf("command misses %s", name)
		}
	}
}

func TestFFPrototypeLuaFixture(t *testing.T) {
	names := ffProtoNames{Entities: []string{"splitter", "ghost"}, Recipes: []string{"r", "nor"}, Items: []string{"i"}, Qualities: []string{"normal"}}
	stubs := `
local function box(a,b,c,d) return {left_top={x=a,y=b},right_bottom={x=c,y=d}} end
prototypes = {
  entity={splitter={type="splitter",tile_width=2,tile_height=1,collision_box=box(-0.9,-0.4,0.9,0.4),supports_direction=true,items_to_place_this={{name="splitter",count=1}},crafting_categories=nil,filter_count=nil}},
  recipe={r={}}, item={i={}}, quality={normal={}}}
`
	assertions := `
assert(captured.entities.splitter.type=="splitter")
assert(captured.entities.splitter.tile_width==2 and captured.entities.splitter.tile_height==1)
assert(captured.entities.splitter.place_item=="splitter")
assert(captured.entities.splitter.supports_direction==true)
assert(captured.entities.splitter.collision_box[3]==0.9)
assert(captured.entities.ghost==nil)
assert(captured.recipes.r==true and captured.recipes.nor==nil)
assert(captured.items.i==true and captured.qualities.normal==true)
`
	runLuaFixture(t, ffPrototypeLua(names), stubs, assertions)
}

func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }
