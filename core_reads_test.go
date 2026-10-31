package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRCON answers a scripted list of RCON connections in order. Each entry is
// the body of one server response; the client's optional end-of-response marker
// command is answered automatically.
type fakeRCON struct {
	resolver *Resolver
	done     chan error
	mu       sync.Mutex
	captured []string
}

func newFakeRCON(t *testing.T, responses ...string) *fakeRCON {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRCON{resolver: &Resolver{addr: listener.Addr().String(), password: "password"}, done: make(chan error, 1)}
	go func() {
		defer listener.Close()
		for _, response := range responses {
			conn, err := listener.Accept()
			if err != nil {
				fake.done <- err
				return
			}
			if _, _, _, err = readPacket(conn); err != nil {
				conn.Close()
				fake.done <- err
				return
			}
			if err = writePacket(conn, 1, 2, ""); err != nil {
				conn.Close()
				fake.done <- err
				return
			}
			_, _, body, err := readPacket(conn)
			if err != nil {
				conn.Close()
				fake.done <- err
				return
			}
			fake.mu.Lock()
			fake.captured = append(fake.captured, body)
			fake.mu.Unlock()
			if err = writePacket(conn, 2, 0, response+"\n"); err != nil {
				conn.Close()
				fake.done <- err
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			if _, _, _, err := readPacket(conn); err == nil {
				_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			}
			conn.Close()
		}
		fake.done <- nil
	}()
	t.Cleanup(func() {
		select {
		case err := <-fake.done:
			if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Errorf("fake server: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("fake server did not finish")
		}
	})
	return fake
}

func (f *fakeRCON) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.captured...)
}

func TestResolveActorCharacterValidation(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	player := "wricardo"
	character := "4"
	if _, err := resolveActorCharacter(offline, &player, &character); err == nil || err.Error() != "exactly one of player or character is required" {
		t.Fatalf("both actors error = %v", err)
	}
	if _, err := resolveActorCharacter(offline, nil, nil); err == nil || err.Error() != "exactly one of player or character is required" {
		t.Fatalf("neither actor error = %v", err)
	}
	if unit, err := resolveActorCharacter(offline, nil, &character); err != nil || unit != "4" {
		t.Fatalf("character passthrough = %q err=%v", unit, err)
	}
	if _, err := resolveActorCharacter(offline, nil, new("0")); err == nil {
		t.Fatal("zero character must fail")
	}
}

func TestResolveActorCharacterFromPlayer(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":1,"player":"wricardo","character":4,"has_character":true}`)
	player := "wricardo"
	unit, err := resolveActorCharacter(fake.resolver, &player, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unit != "4" {
		t.Fatalf("resolved character = %q", unit)
	}
	commands := fake.commands()
	if len(commands) != 1 || !strings.Contains(commands[0], "game.get_player("+luaString("wricardo")) {
		t.Fatalf("player lookup command = %q", commands)
	}
}

func TestResolveActorCharacterWithoutCharacter(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":1,"player":"wricardo","has_character":false}`)
	player := "wricardo"
	_, err := resolveActorCharacter(fake.resolver, &player, nil)
	if err == nil || !strings.Contains(err.Error(), "has no character") {
		t.Fatalf("missing character error = %v", err)
	}
}

func TestRotateResolvesPlayerToCharacter(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":10,"player":"wricardo","character":4}`,
		`{"name":"transport-belt","position":{"x":8.5,"y":9.5},"surface":"nauvis","turns":1,"direction":"east"}`,
	)
	player := "wricardo"
	result, err := rotateMutation(fake.resolver, RotateInput{Player: &player, Entity: new("transport-belt"), X: new(8.5), Y: new(9.5)})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || stringValue(result.Name) != "transport-belt" {
		t.Fatalf("rotate result = %+v", result)
	}
	commands := fake.commands()
	if len(commands) < 2 {
		t.Fatalf("captured commands = %q", commands)
	}
	if !strings.Contains(commands[1], "game.get_entity_by_unit_number(4)") {
		t.Fatalf("rotate command lost the resolved character: %q", commands[1])
	}
}

func TestPickupAndPickupAreaResolvePlayer(t *testing.T) {
	player := "wricardo"
	fake := newFakeRCON(t,
		`{"tick":11,"player":"wricardo","character":4}`,
		`{"item":"iron-plate","moved":5,"remaining":0}`,
	)
	if _, err := pickupMutation(fake.resolver, PickupInput{Player: &player, X: 1.5, Y: 2.5}); err != nil {
		t.Fatal(err)
	}
	commands := fake.commands()
	if len(commands) < 2 || !strings.Contains(commands[1], "game.get_entity_by_unit_number(4)") {
		t.Fatalf("pickup command = %q", commands)
	}

	area := newFakeRCON(t,
		`{"tick":12,"player":"wricardo","character":4}`,
		`{"stacksFound":0,"stacksOutOfReach":0,"itemsPickedUp":[],"itemsRemaining":[]}`,
	)
	_, err := pickupArea(area.resolver, PickupAreaInput{Player: &player, Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 4, RightBottomY: 4}})
	if err != nil {
		t.Fatal(err)
	}
	commands = area.commands()
	if len(commands) < 2 || !strings.Contains(commands[1], "game.get_entity_by_unit_number(4)") {
		t.Fatalf("pickup-area command = %q", commands)
	}
}

func TestSetRecipeDrainValidation(t *testing.T) {
	input := SetRecipeInput{Recipe: "iron-gear-wheel", Character: new("4")}
	bogus := RecipeDrainMode("BOGUS")
	input.Drain = &bogus
	if _, err := setRecipe(&Resolver{addr: "127.0.0.1:1"}, input); err == nil || !strings.Contains(err.Error(), "drain must be ABORT") {
		t.Fatalf("invalid drain error = %v", err)
	}
	input.Drain = nil
	input.Recipe = "  "
	if _, err := setRecipe(&Resolver{addr: "127.0.0.1:1"}, input); err == nil {
		t.Fatal("empty recipe must fail")
	}
	if _, err := normalizeRecipeDrain("to_actor"); err != nil {
		t.Fatalf("underscore drain rejected: %v", err)
	}
	if mode, err := normalizeRecipeDrain(""); err != nil || mode != "abort" {
		t.Fatalf("default drain = %q err=%v", mode, err)
	}
}

func TestSetRecipeLuaInvariants(t *testing.T) {
	base := []string{"--character=4", "--entity=assembling-machine-2", "--x=1.5", "--y=2.5", "--recipe=iron-gear-wheel"}
	lua, err := machineRecipeLua(append(base, "--drain=SPILL", "--recipeQuality=rare"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"e.clear_fluid_inside()",
		"e.set_recipe(recipe.name,quality)",
		"prototypes.quality[quality]",
		"spill_item_stack",
		"returned=machine_aggregate(drained)",
		"inventory.get_contents()",
		"inventory.remove{name=item.name,quality=quality,count=count}",
		"machine_drain(e,drained,c,spill_position,insert_items)",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("spill Lua missing %q", want)
		}
	}
	if !strings.Contains(lua, "local quality="+luaString("rare")) || !strings.Contains(lua, "local drain="+luaString("spill")) {
		t.Fatalf("spill Lua arguments missing: %q", lua)
	}
	toActor, err := machineRecipeLua(append(base, "--drain=to-actor"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toActor, "machine holds fluids; use drain spill to clear them") {
		t.Fatal("to-actor must refuse machines holding fluids")
	}
	if !strings.Contains(toActor, "else\n  local cleared=pcall(function() e.clear_fluid_inside() end)") {
		t.Fatal("fluid clearing must stay inside the spill branch")
	}
	if !strings.Contains(toActor, "inventory.insert{name=name,quality=quality,count=count}") || !strings.Contains(toActor, "spill_position=c.position") {
		t.Fatal("to-actor must insert into the actor inventory and spill near the actor")
	}
	drainIndex := strings.Index(toActor, "machine_drain(e,drained,c,spill_position,insert_items)")
	recipeIndex := strings.Index(toActor, "e.set_recipe(recipe.name,quality)")
	if drainIndex < 0 || recipeIndex < 0 || drainIndex > recipeIndex {
		t.Fatalf("to-actor must drain the machine inventories before changing the recipe (drain=%d recipe=%d)", drainIndex, recipeIndex)
	}
	if strings.Contains(toActor, "removed=result or {}") {
		t.Fatal("returned items must come from the explicit drain, not from the engine return alone")
	}
	if !strings.Contains(toActor, "machine_merge_engine_items(e,drained,c,spill_position,insert_items,engine_returned)") {
		t.Fatal("engine-returned items must be merged into the drained list")
	}
	if _, err := machineRecipeLua(append(base, "--drain=explode"), io.Discard); err == nil {
		t.Fatal("unknown backend drain must fail")
	}
	if _, err := machineRecipeLua(append(base, "--recipeQuality="), io.Discard); err == nil {
		t.Fatal("empty recipe quality must fail")
	}
}

func TestSetRecipeGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":1,"player":"wricardo","character":4}`,
		`{"entity":"assembling-machine-2","recipe":"iron-gear-wheel","quality":"rare","returned":[{"name":"iron-plate","count":2,"quality":"normal"}]}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { setRecipe(input: {player: \"wricardo\", entity: \"assembling-machine-2\", x: 1.5, y: 1.5, recipe: \"iron-gear-wheel\", recipeQuality: \"rare\", drain: SPILL}) { entity recipe quality returned { name count } } }"}`
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
			Entity   string `json:"entity"`
			Recipe   string `json:"recipe"`
			Quality  string `json:"quality"`
			Returned []struct {
				Name  string  `json:"name"`
				Count float64 `json:"count"`
			} `json:"returned"`
		} `json:"setRecipe"`
	}
	if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SetRecipe.Entity != "assembling-machine-2" || decoded.SetRecipe.Recipe != "iron-gear-wheel" || decoded.SetRecipe.Quality != "rare" {
		t.Fatalf("setRecipe payload = %+v", decoded.SetRecipe)
	}
	if len(decoded.SetRecipe.Returned) != 1 || decoded.SetRecipe.Returned[0].Count != 2 {
		t.Fatalf("setRecipe returned = %+v", decoded.SetRecipe.Returned)
	}
	commands := fake.commands()
	if len(commands) < 2 {
		t.Fatalf("captured commands = %q", commands)
	}
	for _, want := range []string{"local quality=" + luaString("rare"), "local drain=" + luaString("spill"), "game.get_entity_by_unit_number(4)"} {
		if !strings.Contains(commands[1], want) {
			t.Fatalf("setRecipe backend Lua missing %q", want)
		}
	}
}

func TestClearRecipeGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":19,"entity":"assembling-machine-2","position":{"x":4.5,"y":6.5},"surface":"nauvis"}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { clearRecipe(input: {character: \"4\", entity: \"assembling-machine-2\", x: 4.5, y: 6.5}) { entity unitNumber } }"}`
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
		t.Fatalf("clearRecipe GraphQL errors = %v", envelope.Errors)
	}
	var decoded struct {
		ClearRecipe struct {
			Entity string `json:"entity"`
		} `json:"clearRecipe"`
	}
	if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ClearRecipe.Entity != "assembling-machine-2" {
		t.Fatalf("clearRecipe payload = %+v", decoded.ClearRecipe)
	}
	if lua := fake.commands()[0]; !strings.Contains(lua, "e.set_recipe(nil)") {
		t.Fatalf("clearRecipe Lua = %q", lua)
	}
}

func TestClearRecipeResolver(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":13,"entity":"assembling-machine-2","position":{"x":4.5,"y":6.5},"surface":"nauvis","returned":[{"name":"iron-plate","count":4,"quality":"normal"}]}`,
		`""`,
	)
	character := "4"
	result, err := clearRecipe(fake.resolver, ClearRecipeInput{
		Character: &character,
		Entity:    new("assembling-machine-2"),
		X:         new(4.5),
		Y:         new(6.5),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || stringValue(result.Entity) != "assembling-machine-2" {
		t.Fatalf("clear recipe result = %+v", result)
	}
	if len(result.Returned) != 1 || stringValue(result.Returned[0].Name) != "iron-plate" {
		t.Fatalf("returned items = %+v", result.Returned)
	}
	commands := fake.commands()
	if !strings.Contains(commands[0], "e.set_recipe(nil)") {
		t.Fatalf("clear recipe Lua does not clear the recipe: %q", commands[0])
	}
	if !strings.Contains(commands[0], "get_main_inventory()") || !strings.Contains(commands[0], "spill_item_stack") {
		t.Fatalf("clear recipe Lua does not drain items: %q", commands[0])
	}
	if !strings.Contains(commands[0], `if e.is_crafting() then fail("machine has an active craft")`) {
		t.Fatalf("clear recipe Lua does not refuse active crafts: %q", commands[0])
	}
	drainIndex := strings.Index(commands[0], "machine_drain(e,drained,c,c.position,true)")
	clearIndex := strings.Index(commands[0], "e.set_recipe(nil)")
	if drainIndex < 0 || clearIndex < 0 || drainIndex > clearIndex {
		t.Fatalf("clear recipe must drain the machine inventories before clearing (drain=%d clear=%d)", drainIndex, clearIndex)
	}
}

func TestClearRecipeResolvesPlayer(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":16,"player":"wricardo","character":4}`,
		`{"tick":17,"entity":"assembling-machine-2","position":{"x":4.5,"y":6.5}}`,
		`""`,
	)
	player := "wricardo"
	if _, err := clearRecipe(fake.resolver, ClearRecipeInput{Player: &player, Entity: new("assembling-machine-2"), X: new(4.5), Y: new(6.5)}); err != nil {
		t.Fatal(err)
	}
	commands := fake.commands()
	if len(commands) != 3 {
		t.Fatalf("captured commands = %d", len(commands))
	}
	if !strings.Contains(commands[1], "game.get_entity_by_unit_number(4)") || !strings.Contains(commands[1], "e.set_recipe(nil)") {
		t.Fatalf("clear recipe Lua = %q", commands[1])
	}
}

func TestClearRecipeRequiresOneActor(t *testing.T) {
	player := "wricardo"
	character := "4"
	if _, err := clearRecipe(&Resolver{addr: "127.0.0.1:1"}, ClearRecipeInput{Player: &player, Character: &character, Entity: new("assembling-machine-2"), X: new(1.0), Y: new(1.0)}); err == nil || err.Error() != "exactly one of player or character is required" {
		t.Fatalf("both actor error = %v", err)
	}
}

func TestEntityTagsJSONValidation(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	for _, text := range []string{"", "  ", "{oops", "[1,2]", `"text"`, "null", "42", `{"a":1}{"b":2}`} {
		_, err := setEntityTags(offline, EntityTagsInput{TagsJSON: text, Character: new("4"), Entity: new("iron-chest"), X: new(1.5), Y: new(1.5)})
		if err == nil || !strings.Contains(err.Error(), "JSON object") {
			t.Fatalf("tags %q error = %v", text, err)
		}
	}
	if err := validateTagsJSON(`{"station":"iron","priority":2}`); err != nil {
		t.Fatalf("valid tags rejected: %v", err)
	}
	if err := validateTagsJSON(`{}`); err != nil {
		t.Fatalf("empty object rejected: %v", err)
	}
}

func TestSetEntityTagsLua(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":14,"surface":"nauvis","name":"iron-chest","position":{"x":1.5,"y":1.5},"unitNumber":77,"tagsJSON":"{\"station\":\"iron\"}"}`,
		`""`,
	)
	character := "4"
	tags := `{"station":"iron"}`
	result, err := setEntityTags(fake.resolver, EntityTagsInput{
		Character: &character,
		Entity:    new("iron-chest"),
		X:         new(1.5),
		Y:         new(1.5),
		TagsJSON:  tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || stringValue(result.UnitNumber) != "77" {
		t.Fatalf("tags result = %+v", result)
	}
	commands := fake.commands()
	lua := commands[0]
	if !strings.Contains(lua, "helpers.json_to_table,"+luaString(tags)) {
		t.Fatalf("tags Lua does not parse the JSON: %q", lua)
	}
	if !strings.Contains(lua, "e.tags=tags") || !strings.Contains(lua, "entity_meta_fields(e)") {
		t.Fatalf("tags Lua does not write tags or snapshot: %q", lua)
	}
	for _, want := range []string{"local ok_readback,persisted=pcall(function() return e.tags end)", "local function tags_equal(left,right)", "entity tags are not persisted by this prototype in Factorio 2.0.77"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("tags Lua does not verify persistence (%q): %q", want, lua)
		}
	}
	if !strings.Contains(lua, "can_reach_entity(e)") {
		t.Fatalf("tags Lua does not enforce reach: %q", lua)
	}
}

func TestSetEntityTagsPersistenceFailure(t *testing.T) {
	fake := newFakeRCON(t, `{"error":"entity tags are not persisted by this prototype in Factorio 2.0.77; tags are blueprint and ghost metadata - use createBlueprintFromEntities or set the tag on the ghost"}`)
	character := "4"
	_, err := setEntityTags(fake.resolver, EntityTagsInput{
		Character: &character,
		Entity:    new("iron-chest"),
		X:         new(1.5),
		Y:         new(1.5),
		TagsJSON:  `{"role":"buffer"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "not persisted") {
		t.Fatalf("non-persisted tags must fail: %v", err)
	}
}

func TestSetNameTagLua(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":15,"surface":"nauvis","name":"train-stop","position":{"x":2.5,"y":3.5}}`,
		`""`,
	)
	character := "4"
	result, err := setNameTag(fake.resolver, EntityNameTagInput{
		Character: &character,
		Entity:    new("train-stop"),
		X:         new(2.5),
		Y:         new(3.5),
		NameTag:   "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.NameTag != nil {
		t.Fatalf("cleared name tag must be absent, got %+v", result)
	}
	if lua := fake.commands()[0]; !strings.Contains(lua, "e.name_tag="+luaString("")) {
		t.Fatalf("empty name tag must clear the tag: %q", lua)
	}
}

// requireFailDefined proves a generated program defines the global fail helper
// before any fail(...) call, which is what every resolver-generated Lua needs.
func requireFailDefined(t *testing.T, name, lua string) {
	t.Helper()
	definition := "local function fail(message)"
	if trimmed, ok := strings.CutPrefix(lua, "/silent-command "); ok {
		lua = trimmed
	}
	if !strings.HasPrefix(lua, definition) {
		t.Fatalf("%s Lua does not start with the fail helper: %q", name, lua)
	}
	if count := strings.Count(lua, definition); count != 1 {
		t.Fatalf("%s Lua defines the fail helper %d times", name, count)
	}
}

func TestGeneratedLuaDefinesFail(t *testing.T) {
	character := "4"
	tagsFake := newFakeRCON(t, `{"tick":1,"surface":"nauvis","name":"iron-chest","position":{"x":1.5,"y":1.5}}`, `""`)
	if _, err := setEntityTags(tagsFake.resolver, EntityTagsInput{Character: &character, Entity: new("iron-chest"), X: new(1.5), Y: new(1.5), TagsJSON: `{"role":"buffer"}`}); err != nil {
		t.Fatal(err)
	}
	requireFailDefined(t, "setEntityTags", tagsFake.commands()[0])

	nameFake := newFakeRCON(t, `{"tick":1,"surface":"nauvis","name":"train-stop","position":{"x":1.5,"y":1.5}}`, `""`)
	if _, err := setNameTag(nameFake.resolver, EntityNameTagInput{Character: &character, Entity: new("train-stop"), X: new(1.5), Y: new(1.5), NameTag: "iron"}); err != nil {
		t.Fatal(err)
	}
	requireFailDefined(t, "setNameTag", nameFake.commands()[0])

	clearFake := newFakeRCON(t, `{"tick":1,"entity":"assembling-machine-2","position":{"x":1.5,"y":1.5}}`, `""`)
	if _, err := clearRecipe(clearFake.resolver, ClearRecipeInput{Character: &character, Entity: new("assembling-machine-2"), X: new(1.5), Y: new(1.5)}); err != nil {
		t.Fatal(err)
	}
	requireFailDefined(t, "clearRecipe", clearFake.commands()[0])

	lua, err := machineRecipeLua([]string{"--character=4", "--entity=assembling-machine-2", "--x=1.5", "--y=1.5", "--recipe=iron-gear-wheel"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	requireFailDefined(t, "set-recipe", lua)

	inspectLua, err := productionLua("inspect", []string{"--character=4", "--entity=iron-chest", "--x=1.5", "--y=1.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	requireFailDefined(t, "inspect", inspectLua)
}

func TestInspectEmitsEntityMeta(t *testing.T) {
	lua, err := productionLua("inspect", []string{"--character=4", "--entity=iron-chest", "--x=1.5", "--y=1.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lua, "underground_neighbour") {
		t.Fatal("underground neighbours do not exist in Factorio 2.0.77; the field must stay null")
	}
	for _, want := range []string{
		"unit_number",
		"name_tag",
		"tags_json",
		"parameters_json",
		"belt_neighbours",
		"fluidbox_neighbours",
		"fluidbox:get_connections(i)",
		"for name,value in pairs(entity_meta_fields(e)) do result[name]=value end",
		"pcall(function() return entity.name_tag end)",
		"pcall(function() return helpers.table_to_json(entity.tags) end)",
		"pcall(function() return entity.get_control_behavior() end)",
		"pcall(function() return entity.parameters end)",
		"for _,child in pairs(value) do entity_meta_collect_tree(list,seen,child,depth+1) end",
		"pcall(function() return entity.belt_neighbours end)",
		"pcall(function() return value.entity end)",
		"if entity_meta_neighbour(value)~=nil then entity_meta_collect(list,seen,value); return end",
		"pcall(function() return entity.fluidbox end)",
		"pcall(function() return box.owner end)",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("inspect Lua missing %q", want)
		}
	}
}

func TestTransferVehicleInventoriesEndToEnd(t *testing.T) {
	cases := []struct {
		kind InventoryKind
		key  string
	}{
		{InventoryKindRoboPortRobot, "roboport_robot"},
		{InventoryKindRoboPortMaterial, "roboport_material"},
		{InventoryKindCargoWagon, "cargo_wagon"},
		{InventoryKindCarTrunk, "car_trunk"},
		{InventoryKindSpiderTrunk, "spider_trunk"},
		{InventoryKindArtilleryWagonAmmo, "artillery_wagon_ammo"},
	}
	for _, tc := range cases {
		fake := newFakeRCON(t, `{"entity":"entity","inventory":"`+tc.key+`","direction":"to-player","item":"coal","moved":5,"requested":5,"available":5,"capacity":4000}`)
		character := "4"
		kind := tc.kind
		result, err := command[ActionResult](fake.resolver, "transfer", TransferInput{
			Character:     &character,
			Entity:        new("entity"),
			X:             new(1.5),
			Y:             new(1.5),
			InventoryKind: &kind,
			Direction:     "to-player",
			Item:          "coal",
			Count:         5,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if result == nil || stringValue(result.Inventory) != tc.key || numberValue(result.Moved) != "5" {
			t.Fatalf("%s result = %+v", tc.key, result)
		}
		lua := fake.commands()[0]
		if !strings.Contains(lua, "inventory="+luaString(tc.key)) {
			t.Fatalf("%s Lua does not target the resolved key: %q", tc.key, lua)
		}
	}
}

func TestBeltNeighbourEntityUnwrap(t *testing.T) {
	lua, err := productionLua("inspect", []string{"--character=4", "--entity=transport-belt", "--x=1.5", "--y=1.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local ok_unwrap,unwrapped=pcall(function() return value.entity end)",
		`if ok_unwrap and unwrapped~=nil and unwrapped~=value then return entity_meta_neighbour(unwrapped) end`,
		"if entity_meta_neighbour(value)~=nil then entity_meta_collect(list,seen,value); return end",
		"for _,child in pairs(value) do entity_meta_collect_tree(list,seen,child,depth+1) end",
		`local ok_belt,belt=pcall(function() return entity.belt_neighbours end)`,
		"if #list>0 then meta.belt_neighbours=list end",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("neighbour Lua missing %q", want)
		}
	}
	// The inputs/outputs dictionary must be walked, not treated as one entity.
	if !strings.Contains(lua, "if value==nil or depth>3 then return end") {
		t.Fatal("neighbour tree walk must keep its depth guard")
	}
}

func TestInventoryMappingKeys(t *testing.T) {
	inspect, err := productionLua("inspect", []string{"--character=4", "--entity=roboport", "--x=1.5", "--y=1.5"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"roboport_robot", "roboport_material", "cargo_wagon", "car_trunk", "spider_trunk", "artillery_wagon_ammo"} {
		if !strings.Contains(inspect, "result."+key+"=get_inv") {
			t.Fatalf("inspect Lua missing inventory %q", key)
		}
		if !inventoryKeys[key] || !inventoryKeys[strings.ReplaceAll(key, "_", "-")] {
			t.Fatalf("transfer does not accept inventory %q", key)
		}
	}
	for _, key := range []string{"roboport-robot", "roboport-material", "cargo-wagon", "car-trunk", "spider-trunk", "artillery-wagon-ammo"} {
		lua, err := productionLua("transfer", []string{"--character=4", "--entity=roboport", "--x=1.5", "--y=1.5", "--inventory=" + key, "--direction=to-entity", "--item=coal", "--count=1"}, io.Discard)
		if err != nil {
			t.Fatalf("transfer %s: %v", key, err)
		}
		if !strings.Contains(lua, "inventory="+luaString(key)) || !strings.Contains(lua, `string.gsub(target_name,"-","_")`) {
			t.Fatalf("transfer %s Lua missing inventory resolution", key)
		}
	}
}
