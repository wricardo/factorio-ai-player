package main

import (
	"encoding/json"
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
	if _, err := resolveActorCharacter(offline, nil, nil); err == nil || err.Error() != "FACTORIO_RCON_PASSWORD is required" {
		t.Fatalf("neither actor defaults to first connected player, offline = %v", err)
	}
	if unit, err := resolveActorCharacter(offline, nil, &character); err != nil || unit != "4" {
		t.Fatalf("character passthrough = %q err=%v", unit, err)
	}
	if _, err := resolveActorCharacter(offline, nil, new("0")); err == nil {
		t.Fatal("zero character must fail")
	}
}
func TestResolveActorCharacterEmptyDefaultsToFirstConnected(t *testing.T) {
	// status lists connected players, then the player query resolves the character.
	fake := newFakeRCON(t,
		`{"tick":1,"players":[{"name":"wricardo","surface":"nauvis","position":{"x":0,"y":0}}]}`,
		`{"tick":1,"player":"wricardo","character":4,"has_character":true}`)
	unit, err := resolveActorCharacter(fake.resolver, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unit != "4" {
		t.Fatalf("defaulted character = %q", unit)
	}
	commands := fake.commands()
	if len(commands) != 2 || !strings.Contains(commands[1], "game.get_player(") {
		t.Fatalf("status then player lookup = %q", commands)
	}
}

func TestResolveActorCharacterEmptyWithoutConnectedPlayer(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":1,"players":[]}`)
	if _, err := resolveActorCharacter(fake.resolver, nil, nil); err == nil ||
		err.Error() != "actor is empty and no player is connected" {
		t.Fatalf("no connected player error = %v", err)
	}
}

func TestDefaultActorPassthrough(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	if got, err := defaultActor(offline, nil); got != nil || err != nil {
		t.Fatalf("nil actor = %v err=%v", got, err)
	}
	player := "wricardo"
	actor := &ActorInput{Player: &player}
	if got, err := defaultActor(offline, actor); err != nil || got != actor {
		t.Fatalf("populated actor = %v err=%v", got, err)
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

func TestSetRecipeDrainValidation(t *testing.T) {
	input := SetRecipeInput{Recipe: "iron-gear-wheel", Actor: &ActorInput{Character: new("4")}, Target: &EntityRefInput{Entity: new("assembling-machine-2"), Position: &PositionInput{X: 1.5, Y: 2.5}}}
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

func TestSetRecipeGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":1,"player":"wricardo","character":4}`,
		`{"entity":"assembling-machine-2","recipe":"iron-gear-wheel","quality":"rare","returned":[{"name":"iron-plate","count":2,"quality":"normal"}]}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { setRecipe(input: {actor: {player: \"wricardo\"}, target: {entity: \"assembling-machine-2\", position: {x: 1.5, y: 1.5}}, recipe: \"iron-gear-wheel\", recipeQuality: \"rare\", drain: SPILL}) { entity recipe quality returned { name count } } }"}`
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
}

func TestClearRecipeGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":19,"entity":"assembling-machine-2","position":{"x":4.5,"y":6.5},"surface":"nauvis"}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { clearRecipe(input: {actor: {character: \"4\"}, target: {entity: \"assembling-machine-2\", position: {x: 4.5, y: 6.5}}}) { entity unitNumber } }"}`
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
}

func TestClearRecipeResolver(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":13,"entity":"assembling-machine-2","position":{"x":4.5,"y":6.5},"surface":"nauvis","returned":[{"name":"iron-plate","count":4,"quality":"normal"}]}`,
		`""`,
	)
	result, err := clearRecipe(fake.resolver, ClearRecipeInput{
		Actor:  &ActorInput{Character: new("4")},
		Target: &EntityRefInput{Entity: new("assembling-machine-2"), Position: &PositionInput{X: 4.5, Y: 6.5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || stringValue(result.Entity) != "assembling-machine-2" {
		t.Fatalf("clear recipe result = %+v", result)
	}
	if len(result.Returned) != 1 || result.Returned[0].Name != "iron-plate" {
		t.Fatalf("returned items = %+v", result.Returned)
	}
}

func TestClearRecipeRequiresOneActor(t *testing.T) {
	player := "wricardo"
	character := "4"
	if _, err := clearRecipe(&Resolver{addr: "127.0.0.1:1"}, ClearRecipeInput{Actor: &ActorInput{Player: &player, Character: &character}, Target: &EntityRefInput{Entity: new("assembling-machine-2"), Position: &PositionInput{X: 1, Y: 1}}}); err == nil || err.Error() != "exactly one of player or character is required" {
		t.Fatalf("both actor error = %v", err)
	}
}

func TestEntityTagsJSONValidation(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	for _, text := range []string{"", "  ", "{oops", "[1,2]", `"text"`, "null", "42", `{"a":1}{"b":2}`} {
		_, err := setEntityTags(offline, EntityTagsInput{TagsJSON: text, Actor: &ActorInput{Character: new("4")}, Target: &EntityRefInput{Entity: new("iron-chest"), Position: &PositionInput{X: 1.5, Y: 1.5}}})
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

func TestSetEntityTagsPersistenceFailure(t *testing.T) {
	fake := newFakeRCON(t, `{"error":"entity tags are not persisted by this prototype in Factorio 2.0.77; tags are blueprint and ghost metadata - use createBlueprintFromEntities or set the tag on the ghost"}`)
	_, err := setEntityTags(fake.resolver, EntityTagsInput{
		Actor:    &ActorInput{Character: new("4")},
		Target:   &EntityRefInput{Entity: new("iron-chest"), Position: &PositionInput{X: 1.5, Y: 1.5}},
		TagsJSON: `{"role":"buffer"}`,
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
	result, err := setNameTag(fake.resolver, EntityNameTagInput{
		Actor:   &ActorInput{Character: new("4")},
		Target:  &EntityRefInput{Entity: new("train-stop"), Position: &PositionInput{X: 2.5, Y: 3.5}},
		NameTag: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.NameTag != nil {
		t.Fatalf("cleared name tag must be absent, got %+v", result)
	}
}

func TestSetArithmeticCombinatorParameters(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":16,"surface":"nauvis","name":"arithmetic-combinator","position":{"x":2.5,"y":3.5},"combinator_parameters":{"arithmetic":{"first_signal":{"type":"ITEM","name":"iron-plate","quality":"normal"},"second_constant":2,"operation":"*","output_signal":{"type":"VIRTUAL","name":"signal-A"}}}}`,
		`""`,
	)
	item, virtual := SignalTypeItem, SignalTypeVirtual
	result, err := setCombinatorParameters(fake.resolver, SetCombinatorParametersInput{
		Actor:  &ActorInput{Character: new("4")},
		Target: &EntityRefInput{Entity: new("arithmetic-combinator"), Position: &PositionInput{X: 2.5, Y: 3.5}},
		Parameters: &CombinatorParametersInput{Arithmetic: &ArithmeticCombinatorParametersInput{
			FirstSignal:    &SignalInput{Type: &item, Name: "iron-plate", Quality: new("normal")},
			SecondConstant: new(2),
			Operation:      "*",
			OutputSignal:   &SignalInput{Type: &virtual, Name: "signal-A"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CombinatorParameters == nil || result.CombinatorParameters.Arithmetic == nil || result.CombinatorParameters.Arithmetic.Operation != "*" {
		t.Fatalf("typed parameters = %+v", result.CombinatorParameters)
	}
	lua := fake.commands()[0]
	for _, want := range []string{"behavior.parameters=", "first_signal={type=" + luaString("item") + ",name=" + luaString("iron-plate") + ",quality=" + luaString("normal") + "}", "second_constant=2", "operation=" + luaString("*"), "output_signal={type=" + luaString("virtual") + ",name=" + luaString("signal-A") + "}"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("combinator Lua missing %q:\n%s", want, lua)
		}
	}
}

func TestSetCombinatorParametersGraphQLSurface(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":16,"surface":"nauvis","force":"player","name":"arithmetic-combinator","position":{"x":2.5,"y":3.5},"combinator_parameters":{"arithmetic":{"first_signal":{"type":"ITEM","name":"iron-plate"},"second_constant":2,"operation":"*","output_signal":{"type":"VIRTUAL","name":"signal-A"}}}}`,
		`""`,
	)
	server := httptest.NewServer(newGraphQLHandler(newGraphQLSchema(fake.resolver.addr, "password")))
	defer server.Close()
	body := `{"query":"mutation { setCombinatorParameters(input: {actor: {character: \"4\"}, target: {entity: \"arithmetic-combinator\", position: {x: 2.5, y: 3.5}}, parameters: {arithmetic: {firstSignal: {name: \"iron-plate\"}, secondConstant: 2, operation: \"*\", outputSignal: {type: VIRTUAL, name: \"signal-A\"}}}}) { tick surface force actor { player character } name combinatorParameters { arithmetic { operation secondConstant firstSignal { type name } outputSignal { type name } } } } }"}`
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
		SetCombinatorParameters struct {
			Tick    float64 `json:"tick"`
			Surface string  `json:"surface"`
			Force   string  `json:"force"`
			Actor   struct {
				Player    *string `json:"player"`
				Character *string `json:"character"`
			} `json:"actor"`
			Name       string `json:"name"`
			Parameters struct {
				Arithmetic struct {
					Operation string `json:"operation"`
				} `json:"arithmetic"`
			} `json:"combinatorParameters"`
		} `json:"setCombinatorParameters"`
	}
	if err := json.Unmarshal(envelope.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.SetCombinatorParameters
	if got.Tick != 16 || got.Surface != "nauvis" || got.Force != "player" || got.Actor.Character == nil || *got.Actor.Character != "4" || got.Actor.Player != nil || got.Name != "arithmetic-combinator" || got.Parameters.Arithmetic.Operation != "*" {
		t.Fatalf("typed combinator payload = %+v", got)
	}
}

func TestCombinatorParametersValidationBeforeRCON(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	base := SetCombinatorParametersInput{
		Actor:  &ActorInput{Character: new("4")},
		Target: &EntityRefInput{Entity: new("arithmetic-combinator"), Position: &PositionInput{X: 2.5, Y: 3.5}},
	}
	tests := []struct {
		name       string
		parameters *CombinatorParametersInput
		want       string
	}{
		{name: "neither family", parameters: &CombinatorParametersInput{}, want: "exactly one"},
		{name: "both families", parameters: &CombinatorParametersInput{Arithmetic: &ArithmeticCombinatorParametersInput{}, Decider: &DeciderCombinatorParametersInput{}}, want: "exactly one"},
		{name: "bad operation", parameters: &CombinatorParametersInput{Arithmetic: &ArithmeticCombinatorParametersInput{Operation: "BAD", OutputSignal: &SignalInput{Name: "signal-A"}}}, want: "unsupported arithmetic operation"},
		{name: "empty decider", parameters: &CombinatorParametersInput{Decider: &DeciderCombinatorParametersInput{}}, want: "conditions must contain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.Parameters = tc.parameters
			_, err := setCombinatorParameters(offline, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// requireFailDefined proves a generated program defines the global fail helper
// before any fail(...) call, which is what every resolver-generated Lua needs.

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
		{InventoryKindRocket, "rocket_silo_rocket"},
		{InventoryKindAsteroidCollectorOutput, "asteroid_collector_output"},
	}
	for _, tc := range cases {
		fake := newFakeRCON(t, `{"entity":"entity","inventory":"`+tc.key+`","transfer_direction":"TO_ACTOR","item":"coal","moved":5,"requested":5,"available":5,"capacity":4000}`)
		kind := tc.kind
		result, err := command[ActionResult](fake.resolver, "transfer", TransferInput{
			Actor:     &ActorInput{Character: new("4")},
			Target:    &EntityRefInput{Entity: new("entity"), Position: &PositionInput{X: 1.5, Y: 1.5}},
			Inventory: kind,
			Direction: TransferDirectionToActor,
			Item:      "coal",
			Count:     5,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		if result == nil || stringValue(result.Inventory) != tc.key || numberValue(result.Moved) != "5" {
			t.Fatalf("%s result = %+v", tc.key, result)
		}
		if result.Actor == nil || result.Actor.Character == nil || *result.Actor.Character != "4" || result.Actor.Player != nil {
			t.Fatalf("%s actor = %+v", tc.key, result.Actor)
		}
	}
}

func TestActorQueueSnapshotMetadata(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":99,"surface":"nauvis","force":"player","player":"Alice","queue":[]}`)
	result, err := command[CraftQueue](fake.resolver, "queue", ActorInput{Player: new("Alice")})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tick != 99 || result.Surface != "nauvis" || result.Force != "player" || result.Actor == nil || result.Actor.Player == nil || *result.Actor.Player != "Alice" {
		t.Fatalf("queue snapshot = %+v", result)
	}
}
