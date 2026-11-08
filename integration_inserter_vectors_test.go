//go:build integration

package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startVectorHarness starts a disposable engine with a fixture mod that sets
// allow_custom_vectors on the fast-inserter prototype (no vanilla or Space Age
// inserter allows it).
func startVectorHarness(t *testing.T) *integrationHarness {
	t.Helper()
	mod := t.TempDir()
	files := map[string]string{
		"info.json":            `{"name":"inserter_vectors_fixture","version":"0.0.1","title":"fixture","author":"test","factorio_version":"2.0","dependencies":["base"]}`,
		"data-final-fixes.lua": `data.raw.inserter["fast-inserter"].allow_custom_vectors = true`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(mod, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	integrationExtraMods = map[string]string{"inserter_vectors_fixture_0.0.1": mod}
	defer func() { integrationExtraMods = nil }()
	h := startIntegrationHarness(t)
	t.Cleanup(func() { h.close(t) })
	h.createTestSurface(t)
	return h
}

// TestIntegrationInserterVectors verifies setInserterSettings pickupVector and
// dropVector against a real engine: write/read-back, refusal on prototypes
// without allow_custom_vectors, and rotation of the vectors with the entity.
func TestIntegrationInserterVectors(t *testing.T) {
	h := startVectorHarness(t)
	character := h.spawnCharacter(t)
	out := h.lua(t, `local s=game.surfaces["`+integrationSurface+`"]
local f=game.forces.player
local i=s.create_entity{name="fast-inserter",position={0.5,0.5},direction=defines.direction.east,force=f}
local b=s.create_entity{name="burner-inserter",position={5.5,0.5},direction=defines.direction.east,force=f}
rcon.print(i.unit_number.." "..b.unit_number)`)
	units := strings.Fields(out)
	if len(units) != 2 {
		t.Fatalf("fixture output %q", out)
	}
	fast, burner := units[0], units[1]
	const fields = `inserter { customVectorsAllowed pickupVector{x y} dropVector{x y} }`
	read := func(unit, name string) map[string]any {
		res := h.query(t, fmt.Sprintf(`{ inspect(input:{actor:{character:%q},target:{surface:%q,entity:%q,unitNumber:%q}}) { settings { %s } } }`,
			character.id, integrationSurface, name, unit, fields))
		return jsonObject(t, jsonObject(t, jsonObject(t, jsonObject(t, res, "data"), "inspect"), "settings"), "inserter")
	}
	input := func(unit, name, args string) string {
		return fmt.Sprintf(`mutation { setInserterSettings(input:{actor:{character:%q},target:{surface:%q,entity:%q,unitNumber:%q},%s}) { %s } }`,
			character.id, integrationSurface, name, unit, args, fields)
	}
	vec := func(m map[string]any, key string) (float64, float64) {
		v := jsonObject(t, m, key)
		return jsonNumber(t, v, "x"), jsonNumber(t, v, "y")
	}

	def := read(fast, "fast-inserter")
	t.Logf("default fast-inserter: %v", def)
	if !jsonBool(t, def, "customVectorsAllowed") {
		t.Fatalf("fixture mod did not enable custom vectors: %v", def)
	}
	px, py := vec(def, "pickupVector")
	dx, dy := vec(def, "dropVector")
	if px != 1 || py != 0 || math.Abs(dx+1.2) > 0.01 || dy != 0 {
		t.Fatalf("default east vectors pickup=(%v,%v) drop=(%v,%v)", px, py, dx, dy)
	}

	res := h.mutation(t, input(fast, "fast-inserter", `pickupVector:{x:1,y:0.25},dropVector:{x:-1.2,y:-0.25}`))
	set := jsonObject(t, jsonObject(t, jsonObject(t, res, "data"), "setInserterSettings"), "inserter")
	t.Logf("after write: %v", set)
	back := read(fast, "fast-inserter")
	for key, want := range map[string][2]float64{"pickupVector": {1, 0.25}, "dropVector": {-1.2, -0.25}} {
		x, y := vec(back, key)
		if math.Abs(x-want[0]) > 0.01 || math.Abs(y-want[1]) > 0.01 {
			t.Fatalf("%s read back (%v,%v), want %v", key, x, y, want)
		}
	}

	err := h.graphqlExpectingError(t, "mutation", input(burner, "burner-inserter", `pickupVector:{x:1,y:0.25}`))
	if !strings.Contains(err.Error(), "allow_custom_vectors=false") {
		t.Fatalf("burner-inserter refusal = %v", err)
	}
	if x, y := vec(read(burner, "burner-inserter"), "pickupVector"); x != 1 || y != 0 {
		t.Fatalf("refused write changed burner vector to (%v,%v)", x, y)
	}

	h.mutation(t, fmt.Sprintf(`mutation { setDirection(input:{actor:{character:%q},target:{surface:%q,entity:"fast-inserter",unitNumber:%q},direction:8}) { __typename } }`,
		character.id, integrationSurface, fast))
	// Custom vectors rotate with the entity (east to south maps (x,y) to (-y,x)).
	rot := read(fast, "fast-inserter")
	for key, want := range map[string][2]float64{"pickupVector": {-0.25, 1}, "dropVector": {0.25, -1.2}} {
		x, y := vec(rot, key)
		if math.Abs(x-want[0]) > 0.01 || math.Abs(y-want[1]) > 0.01 {
			t.Fatalf("after rotation %s=(%v,%v), want %v", key, x, y, want)
		}
	}
}
