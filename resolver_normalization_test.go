package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestEveryGraphQLResponseListIsNormalized(t *testing.T) {
	schema := newGraphQLSchema("", "").Schema()
	missing := map[string][]string{}
	for typeName, definition := range schema.Types {
		if len(typeName) >= 2 && typeName[:2] == "__" {
			continue
		}
		if definition.Kind != ast.Object {
			continue
		}
		for _, field := range definition.Fields {
			if field.Type == nil || field.Type.Elem == nil || isListField(field.Name) {
				continue
			}
			missing[field.Name] = append(missing[field.Name], typeName+"."+field.Name)
		}
	}
	if len(missing) == 0 {
		return
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Errorf("list field %q missing from isListField: %v", name, missing[name])
	}
}

func TestListNormalizationKeepsObjectValuedEmptyTables(t *testing.T) {
	raw := map[string]any{
		"inventories": map[string]any{},
		"items":       map[string]any{},
	}
	normalized := normalizeGraphQL(raw, "", "inspect").(map[string]any)
	if _, ok := normalized["inventories"].(map[string]any); !ok {
		t.Fatalf("inventories changed from object: %#v", normalized["inventories"])
	}
	if items, ok := normalized["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("items did not normalize to empty list: %#v", normalized["items"])
	}
}

func TestDirectionStringsNormalizeToEngineInts(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"north", "north", "0"},
		{"east", "east", "4"},
		{"half direction", "north_north_east", "1"},
		{"hyphenated", "north-east", "2"},
		{"numeric string", "12", "12"},
		{"numeric value", json.Number("8"), "8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized := normalizeGraphQL(map[string]any{"direction": tc.value}, "", "place").(map[string]any)
			if got := fmt.Sprint(normalized["direction"]); got != tc.want {
				t.Fatalf("direction = %#v, want %s", got, tc.want)
			}
		})
	}

	normalized := normalizeGraphQL(map[string]any{"direction": "to-player"}, "", "transfer").(map[string]any)
	if got, ok := normalized["direction"].(string); !ok || got != "to-player" {
		t.Fatalf("non-direction enum changed: %#v", normalized["direction"])
	}
}

func TestPlaceDecodesDirectionString(t *testing.T) {
	r, _ := serveLuaOnce(t, `{"tick":10,"surface":"nauvis","force":"player","name":"wooden-chest","position":{"x":1.5,"y":2.5},"direction":"north","unit_number":42,"item":"wooden-chest"}`)
	player, surface := "wricardo", "nauvis"
	result, _, err := runCommand[ActionResult](r, "place", &PlaceInput{
		Actor:    &ActorInput{Player: &player},
		Surface:  &surface,
		Position: &PositionInput{X: 1.5, Y: 2.5},
		Item:     "wooden-chest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Direction == nil || *result.Direction != 0 {
		t.Fatalf("direction = %#v, want 0", result.Direction)
	}
	if result.UnitNumber == nil || *result.UnitNumber != "42" {
		t.Fatalf("unitNumber = %#v, want 42", result.UnitNumber)
	}
}

func TestWireColorAndInserterVectorDecode(t *testing.T) {
	signals, err := factoryDecode[CircuitNetworkSignals](`{"tick":1,"surface":"nauvis","name":"lamp","color":"red","connected":true,"redConnections":1,"greenConnections":0,"signals":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if signals.Color != WireColorRed {
		t.Fatalf("wire color = %q, want RED", signals.Color)
	}

	prototype, err := factoryDecode[Prototype](`{"name":"inserter","entity":{"name":"inserter","type":"inserter","tileWidth":1,"tileHeight":1,"inserterPickup":[0,-1],"inserterDrop":[0,1]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if prototype.Entity == nil || prototype.Entity.InserterPickup == nil || prototype.Entity.InserterDrop == nil {
		t.Fatalf("inserter vectors did not decode: %+v", prototype.Entity)
	}
	if prototype.Entity.InserterPickup.X != 0 || prototype.Entity.InserterPickup.Y != -1 {
		t.Fatalf("inserterPickup = %+v", prototype.Entity.InserterPickup)
	}
	if prototype.Entity.InserterDrop.X != 0 || prototype.Entity.InserterDrop.Y != 1 {
		t.Fatalf("inserterDrop = %+v", prototype.Entity.InserterDrop)
	}
}
