package main

import (
	"os/exec"
	"strings"
	"testing"
)

// A batch reference without a unit number must resolve by prototype name and
// exact centre position. The old lookup required candidate.unit_number==ref.unit,
// so a position-only ref (unit 0) always fell through and failed with a LOOKUP
// error even though the entity sat on that tile.
func TestBatchEntityLookupAcceptsPositionOnlyTargets(t *testing.T) {
	for _, want := range []string{
		"ref.unit==0 or candidate.unit_number==ref.unit",
		`if ref.unit==0 then return nil,"entity not found at that position on that surface" end`,
	} {
		if !strings.Contains(luaBatchEntityIndexLua, want) {
			t.Fatalf("batch lookup missing %q:\n%s", want, luaBatchEntityIndexLua)
		}
	}
}

// Furnaces keep modules in furnace_modules, not crafter_modules, so the shared
// MODULES selector must fall back to that index instead of reporting a missing
// inventory.
func TestInventoryHelpersExposeFurnaceModules(t *testing.T) {
	for _, want := range []string{
		`if t=="furnace" then`,
		"defines.inventory.furnace_modules",
	} {
		if !strings.Contains(luaInventoryHelpers, want) {
			t.Fatalf("inventory helpers missing %q:\n%s", want, luaInventoryHelpers)
		}
	}
}

// Inspection must expose engine pipe connections so callers can route pipes
// without probing every tile.
func TestInspectSnapshotReportsFluidPorts(t *testing.T) {
	for _, want := range []string{
		"get_pipe_connections(box)",
		"result.fluid_ports=ports",
		"connection.target_position",
		"target_pipe_connection_index",
	} {
		if !strings.Contains(luaInspectSnapshot, want) {
			t.Fatalf("inspect snapshot missing %q:\n%s", want, luaInspectSnapshot)
		}
	}
}

// An infinity pipe is configured with a fluid filter, not item filters, and the
// write is read back so an engine rejection cannot pass silently.
func TestConfigureInfinityPipeWritesFilter(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","name":"infinity-pipe","position":{"x":1.5,"y":2.5},"infinity":{"filters":[],"fluidFilters":[{"fluid":"crude-oil","percentage":0.5,"mode":"at-least"}],"removeUnfilteredItems":false}}`
	fake := newFakeRCON(t, response)
	result, err := configureInfinityContainer(fake.resolver, ConfigureInfinityContainerInput{
		Actor:  &ActorInput{Player: new("wricardo")},
		Target: &EntityRefInput{Surface: new("nauvis"), UnitNumber: new("77")},
		FluidFilters: []*InfinityFluidFilterInput{{
			Fluid:      "crude-oil",
			Percentage: new(0.5),
			Mode:       InfinityPipeFilterModeAtLeast,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := fake.commands()[0]
	for _, want := range []string{
		`if e.type=="infinity-pipe" then`,
		"e.set_infinity_pipe_filter(filter)",
		"filter.percentage=0.5",
		"e.get_infinity_pipe_filter()",
		`if not prototypes.fluid[`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("Lua must contain %q:\n%s", want, lua)
		}
	}
	// Regression for 7u8: a bare `return` mid-block in the container branch made
	// the whole chunk fail to compile whenever a fluid filter was sent.
	if luac, err := exec.LookPath("luac"); err == nil {
		cmd := exec.Command(luac, "-p", "-")
		cmd.Stdin = strings.NewReader(strings.TrimPrefix(lua, "/silent-command "))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("infinity pipe Lua does not parse: %v\n%s\n%s", err, output, lua)
		}
	}
	if result.Infinity == nil || len(result.Infinity.FluidFilters) != 1 {
		t.Fatalf("infinity section = %+v", result.Infinity)
	}
	filter := result.Infinity.FluidFilters[0]
	if filter.Fluid != "crude-oil" || filter.Mode != "at-least" {
		t.Fatalf("fluid filter = %+v", filter)
	}
}

// The pipe and container forms are mutually exclusive, and a pipe takes exactly
// one fluid filter.
func TestConfigureInfinityPipeValidation(t *testing.T) {
	// Every case is rejected before any RCON call, so a plain resolver is enough
	// and no fake server has to be drained.
	resolver := &Resolver{addr: "127.0.0.1:1", password: "password"}
	actor := &ActorInput{Player: new("wricardo")}
	target := &EntityRefInput{Surface: new("nauvis"), UnitNumber: new("77")}
	if _, err := configureInfinityContainer(resolver, ConfigureInfinityContainerInput{
		Actor:  actor,
		Target: target,
		FluidFilters: []*InfinityFluidFilterInput{
			{Fluid: "crude-oil", Mode: InfinityPipeFilterModeAtLeast},
			{Fluid: "water", Mode: InfinityPipeFilterModeAtLeast},
		},
	}); err == nil || !strings.Contains(err.Error(), "exactly one fluid filter") {
		t.Fatalf("two fluid filters error = %v", err)
	}
	if _, err := configureInfinityContainer(resolver, ConfigureInfinityContainerInput{
		Actor:  actor,
		Target: target,
		Filters: []*InfinityContainerFilterInput{{
			Item: "electric-mining-drill", Count: 1, Mode: InfinityFilterModeExactly,
		}},
		FluidFilters: []*InfinityFluidFilterInput{{Fluid: "water", Mode: InfinityPipeFilterModeAtLeast}},
	}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("combined filters error = %v", err)
	}
	if _, err := configureInfinityContainer(resolver, ConfigureInfinityContainerInput{
		Actor:  actor,
		Target: target,
		FluidFilters: []*InfinityFluidFilterInput{{
			Fluid: "water", Percentage: new(1.5), Mode: InfinityPipeFilterModeAtLeast,
		}},
	}); err == nil || !strings.Contains(err.Error(), "percentage") {
		t.Fatalf("bad percentage error = %v", err)
	}
}
