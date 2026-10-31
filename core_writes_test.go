package main

import (
	"math"
	"net"
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

func TestDropItemValidation(t *testing.T) {
	r := &Resolver{}
	base := func() DropItemInput {
		return DropItemInput{
			Actor:    &ActorInput{Character: new("4")},
			Item:     "iron-plate",
			Count:    10,
			Position: &PositionInput{X: 1, Y: 2},
			Target:   DropTargetGround,
		}
	}
	for name, mutate := range map[string]func(*DropItemInput){
		"no actor":         func(in *DropItemInput) { in.Actor = nil },
		"both actors":      func(in *DropItemInput) { in.Actor.Player = new("wricardo") },
		"zero character":   func(in *DropItemInput) { in.Actor.Character = new("0") },
		"bad character":    func(in *DropItemInput) { in.Actor.Character = new("x") },
		"empty item":       func(in *DropItemInput) { in.Item = "" },
		"empty quality":    func(in *DropItemInput) { in.Quality = new("") },
		"zero count":       func(in *DropItemInput) { in.Count = 0 },
		"fractional count": func(in *DropItemInput) { in.Count = 1.5 },
		"count too large":  func(in *DropItemInput) { in.Count = math.MaxUint32 + 1 },
		"nan count":        func(in *DropItemInput) { in.Count = math.NaN() },
		"infinite count":   func(in *DropItemInput) { in.Count = math.Inf(1) },
		"nan x":            func(in *DropItemInput) { in.Position.X = math.NaN() },
		"infinite y":       func(in *DropItemInput) { in.Position.Y = math.Inf(-1) },
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
	r, _ := serveLuaAndNotify(t, `{"tick":5,"surface":"nauvis","item":"iron-plate","quality":"rare","requested":10,"moved":10,"position":{"x":1,"y":2},"line":2}`)
	result, err := dropItem(r, DropItemInput{
		Actor:    &ActorInput{Character: new("4")},
		Item:     "iron-plate",
		Quality:  new("rare"),
		Count:    10,
		Position: &PositionInput{X: 1, Y: 2},
		Target:   DropTargetBelt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Moved != 10 || result.Line != 2 || result.Quality != "rare" || result.Position == nil || result.Position.X != 1 {
		t.Fatalf("unexpected drop result: %+v", result)
	}
}

func TestDirectionBounds(t *testing.T) {
	if _, err := placeMany(&Resolver{}, PlaceManyInput{
		Actor: &ActorInput{Character: new("4")},
		Item:  "rail",
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 1, Y: 2}, Direction: 16},
		},
	}); err == nil {
		t.Fatal("placeMany direction 16 must fail")
	}
	if _, err := placeBlueprint(&Resolver{}, PlaceBlueprintInput{Blueprint: "0eNq", Position: &PositionInput{X: 0, Y: 0}, Direction: 16}); err == nil {
		t.Fatal("placeBlueprint direction 16 must fail")
	}
	if _, err := createBlueprintFromEntities(&Resolver{}, CreateBlueprintFromEntitiesInput{
		Entities: []*BlueprintEntityInput{{Name: "rail", Position: &PositionInput{X: 0, Y: 0}, Direction: 16}},
	}); err == nil {
		t.Fatal("blueprint entity direction 16 must fail")
	}
}

func TestPlaceManyLuaAndEntities(t *testing.T) {
	response := `{"tick":7,"surface":"nauvis","item":"transport-belt","placed":2,"failed":0,"errors":[],"entities":[{"name":"transport-belt","type":"transport-belt","position":{"x":1,"y":2},"unit_number":41}]}`
	r, _ := serveLuaAndNotify(t, response)
	result, err := placeMany(r, PlaceManyInput{
		Actor:   &ActorInput{Character: new("4")},
		Item:    "transport-belt",
		Quality: new("rare"),
		Placements: []*PlacementInput{
			{Position: &PositionInput{X: 1, Y: 2}, Direction: 15, FastReplace: new(true)},
			{Position: &PositionInput{X: 3, Y: 4}},
		},
	})
	if err != nil {
		t.Fatal(err)
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
