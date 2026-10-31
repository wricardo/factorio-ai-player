package main

import (
	"io"
	"math"
	"net"
	"regexp"
	"strings"
	"testing"
)

// serveLuaAndNotify answers one executeLua exchange plus the follow-up
// notification command, returning the resolver and the captured Lua payload.
func serveLuaAndNotify(t *testing.T, response string) (*Resolver, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	commands := make(chan string, 1)
	serverErr := make(chan error, 1)
	go func() {
		fail := func(err error) { serverErr <- err }
		conn, err := listener.Accept()
		if err != nil {
			fail(err)
			return
		}
		if _, _, _, err = readPacket(conn); err != nil {
			conn.Close()
			fail(err)
			return
		}
		if err = writePacket(conn, 1, 2, ""); err != nil {
			conn.Close()
			fail(err)
			return
		}
		_, _, body, err := readPacket(conn)
		if err != nil {
			conn.Close()
			fail(err)
			return
		}
		commands <- body
		if err = writePacket(conn, 2, 0, response+"\n"); err != nil {
			conn.Close()
			fail(err)
			return
		}
		if err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n"); err != nil {
			conn.Close()
			fail(err)
			return
		}
		conn.Close()
		notify, err := listener.Accept()
		if err != nil {
			fail(err)
			return
		}
		defer notify.Close()
		if _, _, _, err = readPacket(notify); err != nil {
			fail(err)
			return
		}
		if err = writePacket(notify, 1, 2, ""); err != nil {
			fail(err)
			return
		}
		if _, _, _, err = readPacket(notify); err != nil {
			fail(err)
			return
		}
		fail(writePacket(notify, 2, 0, ""))
	}()
	t.Cleanup(func() {
		if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Errorf("fake server: %v", err)
		}
	})
	return &Resolver{addr: listener.Addr().String(), password: "password"}, commands
}

// Factorio runtime methods are dot calls taking a single table/argument, so a
// pcall whose first argument is a dotted method plus an explicit object raises
// "Arguments count error". Ban that shape in every generated write payload
// while allowing plain module functions such as helpers.table_to_json.
var dottedPcall = regexp.MustCompile(`pcall\(([A-Za-z_][A-Za-z0-9_]*)\.[A-Za-z_]`)

func bannedDottedPcall(lua string) string {
	for _, match := range dottedPcall.FindAllStringSubmatch(lua, -1) {
		switch match[1] {
		case "helpers", "defines", "string", "table", "math":
			continue
		}
		return match[0]
	}
	return ""
}

func TestPcallShapesAcrossCommands(t *testing.T) {
	transferLua, err := productionLua("transfer", []string{
		"--character=4", "--entity=cargo-wagon", "--x=1", "--y=2",
		"--inventoryKind=CARGO_WAGON", "--direction=to-entity",
		"--item=iron-plate", "--count=5",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	equipText, err := equipLua([]string{"--character=4", "--item=firearm-magazine", "--inventory=ammo", "--direction=to-main"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	beltText, err := beltTakeLua([]string{"--character=4", "--x=1", "--y=2", "--item=coal"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	placeText, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt", "--fastReplace=true"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fuelText, err := actionLua("fuel", []string{"--character=4", "--entity=stone-furnace", "--x=1", "--y=2", "--item=coal"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	inspectText, err := productionLua("inspect", []string{"--character=4", "--entity=wooden-chest", "--x=1", "--y=2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for name, lua := range map[string]string{
		"place":    placeText,
		"fuel":     fuelText,
		"transfer": transferLua,
		"inspect":  inspectText,
		"equip":    equipText,
		"beltTake": beltText,
	} {
		if match := bannedDottedPcall(lua); match != "" {
			t.Fatalf("%s Lua pcalls a dotted method with an explicit object: %q", name, match)
		}
	}

	for name, run := range map[string]func() string{
		"dropItem": func() string {
			r, commands := serveLuaAndNotify(t, `{"tick":1,"surface":"nauvis","item":"iron-plate","quality":"normal","requested":1,"moved":1,"position":{"x":1,"y":2},"line":0}`)
			if _, err := dropItem(r, DropItemInput{
				Character: new("4"), Item: "iron-plate", Count: 1, X: 1, Y: 2, Target: DropTargetGround,
			}); err != nil {
				t.Fatal(err)
			}
			return <-commands
		},
		"placeMany": func() string {
			r, commands := serveLuaAndNotify(t, `{"tick":1,"surface":"nauvis","item":"pipe","placed":1,"failed":0,"errors":[],"entities":[]}`)
			if _, err := placeMany(r, PlaceManyInput{
				Character:  new("4"),
				Item:       "pipe",
				Placements: []*PlacementInput{{X: 1, Y: 2, FastReplace: new(true)}},
			}); err != nil {
				t.Fatal(err)
			}
			return <-commands
		},
	} {
		lua := run()
		if match := bannedDottedPcall(lua); match != "" {
			t.Fatalf("%s Lua pcalls a dotted method with an explicit object: %q", name, match)
		}
		if !strings.Contains(lua, "pcall(function() return ") {
			t.Fatalf("%s Lua must pcall through a closure", name)
		}
	}
}

func TestQualityResolution(t *testing.T) {
	if quality, err := resolveQuality(nil); err != nil || quality != "normal" {
		t.Fatalf("default quality = %q err=%v", quality, err)
	}
	if quality, err := resolveQuality(new("rare")); err != nil || quality != "rare" {
		t.Fatalf("explicit quality = %q err=%v", quality, err)
	}
	if _, err := resolveQuality(new("")); err == nil {
		t.Fatal("empty quality must fail")
	}
}

func TestQualityLuaAcrossCommands(t *testing.T) {
	placeArgs := []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt", "--quality=rare"}
	lua, err := actionLua("place", placeArgs, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local quality=" + luaString("rare"),
		"if not prototypes.quality[quality] then fail(\"quality not found\")",
		"{name=item,quality=quality}",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("place Lua missing %q", want)
		}
	}
	if strings.Contains(lua, `quality="normal"`) {
		t.Fatal("place Lua must not hardcode normal quality")
	}

	fuelLua, err := actionLua("fuel", []string{"--character=4", "--entity=stone-furnace", "--x=1", "--y=2", "--item=coal", "--quality=rare", "--count=2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fuelLua, "local quality="+luaString("rare")) || strings.Contains(fuelLua, `quality="normal"`) {
		t.Fatal("fuel Lua must pass the requested quality")
	}

	beltLua, err := beltTakeLua([]string{"--character=4", "--x=1", "--y=2", "--item=coal", "--quality=rare"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(beltLua, "local quality="+luaString("rare")) || strings.Contains(beltLua, `quality="normal"`) {
		t.Fatal("belt-take Lua must pass the requested quality")
	}

	equipLuaText, err := equipLua([]string{"--character=4", "--item=firearm-magazine", "--quality=rare", "--inventory=ammo", "--direction=to-main"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(equipLuaText, "local quality="+luaString("rare")) || strings.Contains(equipLuaText, `quality="normal"`) {
		t.Fatal("equip Lua must pass the requested quality")
	}

	if _, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt", "--quality="}, io.Discard); err == nil {
		t.Fatal("empty quality flag must fail")
	}
}

func TestDropItemValidation(t *testing.T) {
	r := &Resolver{}
	base := func() DropItemInput {
		return DropItemInput{
			Character: new("4"),
			Item:      "iron-plate",
			Count:     10,
			X:         1,
			Y:         2,
			Target:    DropTargetGround,
		}
	}
	for name, mutate := range map[string]func(*DropItemInput){
		"no actor":         func(in *DropItemInput) { in.Character = nil },
		"both actors":      func(in *DropItemInput) { in.Player = new("wricardo") },
		"zero character":   func(in *DropItemInput) { in.Character = new("0") },
		"bad character":    func(in *DropItemInput) { in.Character = new("x") },
		"empty item":       func(in *DropItemInput) { in.Item = "" },
		"empty quality":    func(in *DropItemInput) { in.Quality = new("") },
		"zero count":       func(in *DropItemInput) { in.Count = 0 },
		"fractional count": func(in *DropItemInput) { in.Count = 1.5 },
		"count too large":  func(in *DropItemInput) { in.Count = math.MaxUint32 + 1 },
		"nan count":        func(in *DropItemInput) { in.Count = math.NaN() },
		"infinite count":   func(in *DropItemInput) { in.Count = math.Inf(1) },
		"nan x":            func(in *DropItemInput) { in.X = math.NaN() },
		"infinite y":       func(in *DropItemInput) { in.Y = math.Inf(-1) },
		"empty surface":    func(in *DropItemInput) { in.Surface = new("") },
		"unknown target":   func(in *DropItemInput) { in.Target = "" },
	} {
		input := base()
		mutate(&input)
		if _, err := dropItem(r, input); err == nil {
			t.Fatalf("%s must fail", name)
		}
	}
	if _, err := dropItem(r, base()); err == nil {
		t.Fatal("valid input still requires a live server")
	}
}

func TestDropItemLuaAndDecode(t *testing.T) {
	r, commands := serveLuaAndNotify(t, `{"tick":5,"surface":"nauvis","item":"iron-plate","quality":"rare","requested":10,"moved":10,"position":{"x":1,"y":2},"line":2}`)
	result, err := dropItem(r, DropItemInput{
		Character: new("4"),
		Item:      "iron-plate",
		Quality:   new("rare"),
		Count:     10,
		X:         1,
		Y:         2,
		Target:    DropTargetBelt,
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		"s.find_entities_filtered",
		"e.type==\"transport-belt\" or e.type==\"linked-belt\"",
		"get_item_insert_specification",
		"transport.can_insert_at(spec_position)",
		"transport.insert_at",
		"s.spill_item_stack",
		"local quality=" + luaString("rare"),
		"local requested=10",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("dropItem Lua missing %q", want)
		}
	}
	for _, bad := range []string{"pcall(belt.", "pcall(transport.", "pcall(s.spill_item_stack,s,"} {
		if strings.Contains(lua, bad) {
			t.Fatalf("dropItem must pcall through closures, found %q", bad)
		}
	}
	if !strings.Contains(lua, "pcall(function() return belt.get_item_insert_specification(") ||
		!strings.Contains(lua, "pcall(function() return transport.insert_at(") ||
		!strings.Contains(lua, "pcall(function() return s.spill_item_stack{") {
		t.Fatal("dropItem belt and spill calls must be pcalled through closures")
	}
	if result.Moved != 10 || result.Line != 2 || result.Quality != "rare" || result.Position == nil || result.Position.X != 1 {
		t.Fatalf("unexpected drop result: %+v", result)
	}
}

func TestDirectionCodeBounds(t *testing.T) {
	if _, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=rail", "--directionCode=16"}, io.Discard); err == nil {
		t.Fatal("place directionCode 16 must fail")
	}
	lua, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=rail", "--directionCode=6"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lua, "direction=6") {
		t.Fatalf("directionCode must reach create_entity, got %q", lua)
	}
	if _, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=rail", "--directionCode=-5"}, io.Discard); err == nil {
		t.Fatal("negative place directionCode must fail")
	}
	if _, err := placeMany(&Resolver{}, PlaceManyInput{
		Character: new("4"),
		Item:      "rail",
		Placements: []*PlacementInput{
			{X: 1, Y: 2, DirectionCode: new(16)},
		},
	}); err == nil {
		t.Fatal("placeMany directionCode 16 must fail")
	}
	if _, err := placeBlueprint(&Resolver{}, PlaceBlueprintInput{Blueprint: "0eNq", X: 0, Y: 0, DirectionCode: new(16)}); err == nil {
		t.Fatal("placeBlueprint directionCode 16 must fail")
	}
	if _, err := createBlueprintFromEntities(&Resolver{}, CreateBlueprintFromEntitiesInput{
		Entities: []*BlueprintEntityInput{{Name: "rail", DirectionCode: new(16)}},
	}); err == nil {
		t.Fatal("blueprint entity directionCode 16 must fail")
	}
}

func TestPlaceCreationFailureShape(t *testing.T) {
	lua, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt", "--fastReplace=true"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// Factorio surface methods take a single table argument, so pcall must wrap
	// a call instead of passing the surface again.
	for _, bad := range []string{"pcall(s.create_entity,s,", "pcall(s.create_entity, s,"} {
		if strings.Contains(lua, bad) {
			t.Fatalf("create_entity must not be pcalled with an explicit self argument: %q", bad)
		}
	}
	if !strings.Contains(lua, "pcall(function() return s.create_entity") {
		t.Fatal("create_entity must be pcalled through a closure")
	}
	// fail() only prints; every failure branch must return before the success print.
	for _, branch := range []string{
		`fail("entity creation failed: "..tostring(e)); return end`,
		`fail("entity creation failed; item returned"); return end`,
	} {
		if !strings.Contains(lua, branch) {
			t.Fatalf("place failure branch must return before printing: %q", branch)
		}
	}
	if count := strings.Count(lua, "helpers.table_to_json({name=e.name"); count != 1 {
		t.Fatalf("place must print exactly one success document, got %d", count)
	}
}

func TestFastReplaceLua(t *testing.T) {
	plain, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "fast_replace") || !strings.Contains(plain, "s.can_place_entity") {
		t.Fatal("place must check can_place_entity and not fast-replace by default")
	}
	if !strings.Contains(plain, "direction=0") {
		t.Fatal("place direction must default to north")
	}
	replace, err := actionLua("place", []string{"--character=4", "--x=1", "--y=2", "--item=transport-belt", "--fastReplace=true"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replace, "fast_replace=true,spill=true") {
		t.Fatal("fastReplace must set fast_replace and spill")
	}
	if strings.Contains(replace, "s.can_place_entity") {
		t.Fatal("fastReplace must not reject collisions up front")
	}
}

func TestInventoryKindMapping(t *testing.T) {
	args := []string{
		"--character=4", "--entity=roboport", "--x=1", "--y=2",
		"--inventoryKind=ROBO_PORT_ROBOT", "--direction=to-entity",
		"--item=iron-plate", "--quality=rare", "--count=5",
	}
	lua, err := productionLua("transfer", args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"local target_name=" + luaString("roboport_robot"),
		"local quality=" + luaString("rare"),
		"if not prototypes.quality[quality] then fail(\"quality not found\")",
		"result.roboport_robot=get_inv(entity,\"roboport_robot\")",
		"result.cargo_wagon=get_inv(entity,\"cargo_wagon\")",
		"result.car_trunk=get_inv(entity,\"car_trunk\")",
		"result.spider_trunk=get_inv(entity,\"spider_trunk\")",
		"result.artillery_wagon_ammo=get_inv(entity,\"artillery_wagon_ammo\")",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("transfer Lua missing %q", want)
		}
	}

	legacy, err := productionLua("transfer", []string{
		"--character=4", "--entity=gun-turret", "--x=1", "--y=2",
		"--inventory=turret-ammo", "--direction=to-entity",
		"--item=firearm-magazine", "--count=5",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(legacy, "local target_name="+luaString("turret-ammo")) {
		t.Fatal("string inventory must pass through unchanged")
	}
	if !strings.Contains(legacy, "local quality="+luaString("normal")) {
		t.Fatal("quality must default to normal")
	}

	if _, err := productionLua("transfer", []string{
		"--character=4", "--entity=wooden-chest", "--x=1", "--y=2",
		"--inventory=chest", "--inventoryKind=CHEST", "--direction=to-entity",
		"--item=iron-plate", "--count=5",
	}, io.Discard); err == nil {
		t.Fatal("inventory plus inventoryKind must fail")
	}
	if _, err := productionLua("transfer", []string{
		"--character=4", "--entity=wooden-chest", "--x=1", "--y=2",
		"--inventoryKind=BOGUS", "--direction=to-entity",
		"--item=iron-plate", "--count=5",
	}, io.Discard); err == nil || !strings.Contains(err.Error(), "inventoryKind must be one of") {
		t.Fatalf("unknown inventoryKind must list the enum, got %v", err)
	}
	if _, err := productionLua("transfer", []string{
		"--character=4", "--entity=wooden-chest", "--x=1", "--y=2",
		"--direction=to-entity", "--item=iron-plate", "--count=5",
	}, io.Discard); err == nil {
		t.Fatal("transfer without inventory selector must fail")
	}
}

func TestPlaceManyLuaAndEntities(t *testing.T) {
	response := `{"tick":7,"surface":"nauvis","item":"transport-belt","placed":2,"failed":0,"errors":[],"entities":[{"name":"transport-belt","type":"transport-belt","position":{"x":1,"y":2},"unit_number":41}]}`
	r, commands := serveLuaAndNotify(t, response)
	result, err := placeMany(r, PlaceManyInput{
		Character: new("4"),
		Item:      "transport-belt",
		Quality:   new("rare"),
		Placements: []*PlacementInput{
			{X: 1, Y: 2, DirectionCode: new(15), FastReplace: new(true)},
			{X: 3, Y: 4},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	for _, want := range []string{
		"{x=1,y=2,direction=15,fast_replace=true}",
		"{x=3,y=4,direction=0,fast_replace=false}",
		"local quality=" + luaString("rare"),
		"if not prototypes.quality[quality] then fail(\"quality not found\")",
		"entities=entities",
		"inv.remove{name=item,quality=quality,count=1}",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("placeMany Lua missing %q", want)
		}
	}
	if !strings.Contains(lua, "pcall(function() return s.create_entity") {
		t.Fatal("placeMany must pcall create_entity through a closure")
	}
	if strings.Contains(lua, "pcall(s.create_entity,s,") {
		t.Fatal("placeMany must not pass an explicit surface argument to create_entity")
	}
	if result.Placed != 2 || result.Failed != 0 {
		t.Fatalf("unexpected placeMany result: %+v", result)
	}
	if len(result.Entities) != 1 {
		t.Fatalf("expected one placed entity, got %+v", result.Entities)
	}
	entity := result.Entities[0]
	if entity.Name != "transport-belt" || entity.UnitNumber == nil || *entity.UnitNumber != "41" {
		t.Fatalf("unexpected placed entity: %+v", entity)
	}
	if entity.Position == nil || entity.Position.X != 1 || entity.Position.Y != 2 {
		t.Fatalf("unexpected placed entity position: %+v", entity.Position)
	}
}

func TestPlaceManyDecodeEntities(t *testing.T) {
	decoded, err := factoryDecode[PlaceManyResult](`{"tick":1,"surface":"nauvis","item":"rail","placed":1,"failed":0,"errors":[],"entities":[{"name":"straight-rail","type":"straight-rail","position":{"x":-2.5,"y":3.5},"unit_number":99}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Entities) != 1 {
		t.Fatalf("expected decoded entity, got %+v", decoded.Entities)
	}
	entity := decoded.Entities[0]
	if entity.Name != "straight-rail" || entity.Type == nil || *entity.Type != "straight-rail" {
		t.Fatalf("unexpected entity: %+v", entity)
	}
	if entity.UnitNumber == nil || *entity.UnitNumber != "99" {
		t.Fatalf("unit number must decode as a string: %+v", entity.UnitNumber)
	}
}
