package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Draining a machine must not mark the spilled items for deconstruction: the
// engine reads spill_item_stack{force=...} as "deconstruct every created item",
// which leaves orders the bots can never finish. The stub records the real
// parameters machine_store passes to the engine.
func TestSetRecipeDrainSpillDoesNotMarkForDeconstruction(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	stubs := `
defines = {inventory={crafter_input=1,crafter_output=2}}
local recorded = {}
local actor = {
  force = {name="player"},
  position = {x=0,y=0},
  surface = {spill_item_stack=function(params) recorded[#recorded+1]=params end},
  get_main_inventory = function() return nil end,
}
local function inventory(items)
  return {
    get_contents = function() return items end,
    remove = function(stack) return stack.count end,
    is_empty = function() return #items==0 end,
  }
end
local machine = {
  get_inventory = function(index)
    if index == defines.inventory.crafter_input then
      return inventory({{name="iron-plate",quality="normal",count=5}})
    end
    return inventory({})
  end,
}
`
	assertions := `
local target = {}
machine_drain(machine, target, actor, {x=1.5,y=1.5}, false)
assert(#recorded==1, "expected one spill, got "..#recorded)
local params = recorded[1]
assert(params.force==nil, "spill_item_stack must not pass force: it marks items for deconstruction")
assert(params.allow_belts==false, "spill must not offer items to a belt tile")
assert(params.enable_looted==true, "spill must stay lootable")
assert(params.stack.name=="iron-plate" and params.stack.count==5, "spilled stack")
assert(#target==1 and target[1].count==5, "reported drained stack")
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(stubs + machineDrainLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set-recipe drain spill regression: %v\n%s", err, output)
	}
}

// A furnace cannot use LuaEntity::set_recipe (it is assembling-machine only in
// Factorio 2.0) and chooses its recipe from the item in its input, so setRecipe
// primes the input with one unit of the recipe's item ingredient. The stub
// models a furnace whose input selects the recipe from the inserted item.
func TestFurnaceRecipePrime(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	stubs := `
defines = {inventory={crafter_input=1,crafter_output=2}}
local function stack_index(items, stack)
  for index,item in ipairs(items) do
    if item.name==stack.name and item.quality==stack.quality then return index,item end
  end
end
local function inventory(items)
  return {
    insert=function(stack)
      local index,item=stack_index(items,stack)
      if index then item.count=item.count+stack.count else items[#items+1]={name=stack.name,quality=stack.quality,count=stack.count} end
      return stack.count
    end,
    remove=function(stack)
      local index,item=stack_index(items,stack)
      if not index then return 0 end
      local removed=math.min(item.count,stack.count)
      item.count=item.count-removed
      if item.count==0 then table.remove(items,index) end
      return removed
    end,
    get_contents=function() return items end,
    is_empty=function() return #items==0 end,
  }
end
local function furnace_with(selected,items)
  local furnace={input_items=items,selected=selected}
  furnace.get_inventory=function(index)
    if index==defines.inventory.crafter_input then return inventory(items) end
    return inventory({})
  end
  furnace.get_recipe=function() return furnace.selected end
  return furnace
end
local function actor_with(items)
  return {get_main_inventory=function() return inventory(items) end}
end
local steel={name="steel-plate",enabled=true,category="smelting",ingredients={{type="item",name="iron-plate",amount=5}}}
local iron={name="iron-plate",enabled=true,category="smelting",ingredients={{type="item",name="iron-ore",amount=1}}}
local plate_recipe={name="iron-plate",enabled=true,category="smelting",ingredients={{type="item",name="iron-plate",amount=1}}}
local oil={name="oil-processing",enabled=true,category="oil-processing",ingredients={{type="fluid",name="crude-oil",amount=100}}}
`
	assertions := `
-- the furnace selects steel-plate when iron plate enters its input
local actor_items={{name="iron-plate",quality="normal",count=10}}
local furnace=furnace_with(steel,{})
local prime,why=machine_furnace_recipe(furnace,steel,"normal",actor_with(actor_items))
assert(prime,why)
assert(prime.name=="iron-plate" and prime.count==1 and prime.quality=="normal","prime stack")
assert(#actor_items==1 and actor_items[1].count==9,"actor inventory must lose exactly one ingredient")
assert(#furnace.input_items==1 and furnace.input_items[1].count==1,"input keeps the priming item")

-- no ingredient in the actor inventory
local empty=furnace_with(steel,{})
local missing,reason=machine_furnace_recipe(empty,steel,"normal",actor_with({}))
assert(missing==nil,"missing ingredient must fail")
assert(reason:find("has no iron-plate",1,true),tostring(reason))
assert(#empty.input_items==0,"nothing may be inserted")

-- the furnace keeps a recipe that can consume the priming item: undo the prime
local wrong_items={{name="iron-plate",quality="normal",count=3}}
local wrong=furnace_with(plate_recipe,{})
local refused,message=machine_furnace_recipe(wrong,steel,"normal",actor_with(wrong_items))
assert(refused==nil,"wrong recipe must fail")
assert(message:find("chose iron-plate",1,true),tostring(message))
assert(wrong_items[1].count==3,"the priming item must go back to the actor")
assert(#wrong.input_items==0,"the furnace input must be emptied again")

-- a stale recipe that cannot consume the priming item will be replaced
local stale_items={{name="iron-plate",quality="normal",count=2}}
local stale=furnace_with(iron,{})
local replaced,replaced_why=machine_furnace_recipe(stale,steel,"normal",actor_with(stale_items))
assert(replaced,replaced_why)
assert(replaced.name=="iron-plate" and stale_items[1].count==1,"stale recipe must accept the prime")

-- a fluid ingredient cannot be primed with an item
local fluid,fluid_reason=machine_furnace_recipe(furnace_with(oil,{}),oil,"normal",actor_with({{name="crude-oil",quality="normal",count=1}}))
assert(fluid==nil and fluid_reason:find("fluid ingredient"),tostring(fluid_reason))
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(stubs + machineFurnaceLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("furnace recipe prime: %v\n%s", err, output)
	}
}

// The generated set-recipe Lua must accept furnaces and route them through the
// priming helper instead of calling set_recipe, which the engine refuses for
// every type except assembling-machine.
func TestSetRecipeLuaAcceptsFurnaces(t *testing.T) {
	var out bytes.Buffer
	lua, err := machineRecipeLua([]string{"--character=267", "--entity=stone-furnace", "--x=1.5", "--y=2.5", "--recipe=steel-plate"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if e.type~="assembling-machine" and e.type~="furnace"`,
		`if e.type=="furnace" then`,
		"machine_furnace_recipe(e,recipe,quality,c)",
		"e.set_recipe(recipe.name,quality)",
		"result.item=prime.name",
	} {
		if !strings.Contains(lua, want) {
			t.Errorf("setRecipe Lua missing %q:\n%s", want, lua)
		}
	}
	if strings.Contains(lua, "assembling machine not found") {
		t.Errorf("setRecipe Lua still rejects furnaces:\n%s", lua)
	}
}

// Every generated set-recipe script must parse; the furnace branch is new Lua
// that no other test executes end to end.
func TestSetRecipeLuaCompiles(t *testing.T) {
	interpreter, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"assembling machine", []string{"--character=267", "--unitNumber=500", "--entity=assembling-machine-1", "--recipe=iron-gear-wheel"}},
		{"furnace", []string{"--character=267", "--entity=stone-furnace", "--x=1.5", "--y=1.5", "--recipe=steel-plate", "--drain=to-actor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			source, err := machineRecipeLua(tc.args, &out)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "set-recipe.lua")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(interpreter, "-e", "assert(loadfile("+strconv.Quote(path)+"))").CombinedOutput(); err != nil {
				t.Fatalf("set-recipe Lua does not parse: %v\n%s", err, output)
			}
		})
	}
}

// A furnace setRecipe call must reach the priming path and surface the priming
// ingredient as item/count, so the caller knows an inventory item was used.
func TestSetRecipeFurnaceGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":1,"player":"wricardo","character":4}`,
		`{"entity":"stone-furnace","recipe":"steel-plate","quality":"normal","drain":"abort","item":"iron-plate","count":1,"returned":[]}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { setRecipe(input: {actor: {player: \"wricardo\"}, target: {entity: \"stone-furnace\", unitNumber: \"9261\"}, recipe: \"steel-plate\"}) { entity recipe item count } }"}`
	reply, err := http.Post(server.URL+graphqlPath, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(reply.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Errors) > 0 {
		t.Fatalf("GraphQL errors = %v", envelope.Errors)
	}
	var decoded struct {
		SetRecipe struct {
			Entity string  `json:"entity"`
			Recipe string  `json:"recipe"`
			Item   string  `json:"item"`
			Count  float64 `json:"count"`
		} `json:"setRecipe"`
	}
	if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SetRecipe.Entity != "stone-furnace" || decoded.SetRecipe.Recipe != "steel-plate" {
		t.Fatalf("setRecipe payload = %+v", decoded.SetRecipe)
	}
	if decoded.SetRecipe.Item != "iron-plate" || decoded.SetRecipe.Count != 1 {
		t.Fatalf("furnace priming ingredient = %q count=%v", decoded.SetRecipe.Item, decoded.SetRecipe.Count)
	}
	found := false
	for _, command := range fake.commands() {
		if strings.Contains(command, "machine_furnace_recipe") && strings.Contains(command, `if e.type=="furnace" then`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("set-recipe Lua did not carry the furnace path: %q", fake.commands())
	}
}
