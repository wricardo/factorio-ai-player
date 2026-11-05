package main

import (
	"strings"
	"testing"
)

// powerConsumers filters run in Lua before pagination, so the request must
// carry every filter as a local and the response must decode as before.
func TestPowerConsumersFilterLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"powerEntities":[{"name":"inserter","type":"inserter","position":{"x":1.5,"y":2.5},"unitNumber":9,"status":"no_power","networkId":"4","connectedToProducingNetwork":false,"powerFluids":{}}]}`)
	status := "no_power"
	connected := false
	network := "4"
	page, err := powerConsumersQuery(r, &PowerInsightsInput{
		Surface: "nauvis", Force: "player", Limit: 100,
		Status: &status, ConnectedToProducingNetwork: &connected, NetworkID: &network, OnlyIssues: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.PowerEntities) != 1 || page.PowerEntities[0].Status == nil || *page.PowerEntities[0].Status != "no_power" {
		t.Fatalf("decoded entities: %+v", page.PowerEntities)
	}
	if got := page.PowerEntities[0].ConnectedToProducingNetwork; got == nil || *got {
		t.Fatalf("connected predicate must decode false, not null: %v", got)
	}
	lua := <-commands
	for _, want := range []string{
		`local want_status, want_connected, want_network, only_issues = ` + luaString("no_power") + `, false, 4, true`,
		`unknown entity status`,
		`issue_statuses = {no_power=true, low_power=true, not_plugged_in_electric_network=true}`,
		`issue = fuel ~= nil and #fuel == 0`,
		`if keep and want_connected ~= nil and f.connected ~= want_connected then keep = false end`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("power Lua missing %q\n%s", want, lua)
		}
	}
}

// With no filter the query must not read per-entity facts for every match;
// that keeps the default cost and behavior unchanged.
func TestPowerConsumersDefaultsAreUnfiltered(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player","pageInfo":{"total":0,"limit":100,"offset":0,"hasMore":false},"powerEntities":{}}`)
	if _, err := powerConsumersQuery(r, nil); err != nil {
		t.Fatal(err)
	}
	lua := <-commands
	if !strings.Contains(lua, `local want_status, want_connected, want_network, only_issues = "", nil, 0, false`) {
		t.Fatalf("default locals missing:\n%s", lua)
	}
	if !strings.Contains(lua, "local filtering = want_status") {
		t.Fatalf("filtering guard missing:\n%s", lua)
	}
}

// electricNetworks accepts networkId only; the entity filters would be silently
// ignored, so they must be rejected before any RCON dial.
func TestElectricNetworksFilters(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	status := "no_power"
	connected := true
	for name, input := range map[string]*PowerInsightsInput{
		"status":     {Surface: "nauvis", Force: "player", Limit: 10, Status: &status},
		"connected":  {Surface: "nauvis", Force: "player", Limit: 10, ConnectedToProducingNetwork: &connected},
		"onlyIssues": {Surface: "nauvis", Force: "player", Limit: 10, OnlyIssues: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := electricNetworksQuery(r, input)
			if err == nil || !strings.Contains(err.Error(), "apply to powerConsumers only") {
				t.Fatalf("want powerConsumers-only error, got %v", err)
			}
		})
	}

	network := "7"
	rr, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},"networks":[{"id":"7","polesInArea":2,"productionWatts":0,"consumptionWatts":0}]}`)
	page, err := electricNetworksQuery(rr, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10, NetworkID: &network})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Networks) != 1 || page.Networks[0].ID != "7" {
		t.Fatalf("networks: %+v", page.Networks)
	}
	if lua := <-commands; !strings.Contains(lua, "id == want_network") || !strings.Contains(lua, "local want_status, want_connected, want_network, only_issues = \"\", nil, 7, false") {
		t.Fatalf("network filter missing:\n%s", lua)
	}
}

// Invalid filters must fail before any RCON dial.
func TestPowerConsumersFilterValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	empty := ""
	for name, tc := range map[string]struct {
		mutate func(*PowerInsightsInput)
		want   string
	}{
		"empty status":     {func(in *PowerInsightsInput) { in.Status = &empty }, "status must not be empty"},
		"zero network":     {func(in *PowerInsightsInput) { id := "0"; in.NetworkID = &id }, "positive electric network id"},
		"text network":     {func(in *PowerInsightsInput) { id := "main"; in.NetworkID = &id }, "positive electric network id"},
		"negative network": {func(in *PowerInsightsInput) { id := "-3"; in.NetworkID = &id }, "positive electric network id"},
	} {
		t.Run(name, func(t *testing.T) {
			input := &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10}
			tc.mutate(input)
			_, err := powerConsumersQuery(r, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

// electricNetworks must report capacity, accumulator state and a load ratio over
// the requested window, not just observed flow.
func TestElectricNetworksCapacityWindowAndAccumulators(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},
	  "networks":[{"id":"3","polesInArea":4,"productionWatts":238555.26,"consumptionWatts":238555.26,
	    "maxProductionWatts":900000,"accumulatorChargeJoules":2500000,"accumulatorCapacityJoules":5000000,
	    "lowPowerConsumers":2,"satisfaction":0.265}]}`
	r, commands := serveLuaOnce(t, response)
	page, err := electricNetworksQuery(r, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10,
		Window: new(ProductionWindowTenMinutes)})
	if err != nil {
		t.Fatal(err)
	}
	network := page.Networks[0]
	if network.MaxProductionWatts != 900000 || network.AccumulatorChargeJoules != 2500000 ||
		network.AccumulatorCapacityJoules != 5000000 || network.LowPowerConsumers != 2 ||
		network.Satisfaction == nil || *network.Satisfaction != 0.265 {
		t.Fatalf("network: %+v", network)
	}
	lua := <-commands
	for _, want := range []string{
		`local flow_precision = defines.flow_precision_index.ten_minutes`,
		`get_max_energy_production`,
		`["electric-energy-interface"]=true`,
		`electric_energy_source_prototype.buffer_capacity`,
		`if capacity.capacity > 0 then network.satisfaction = consumption / capacity.capacity end`,
		`if status_names[entity.status] == "low_power" then entry.lowPower = entry.lowPower + 1 end`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("electricNetworks Lua missing %q\n%s", want, lua)
		}
	}
}

// Power rows must expose the burner fuel inventory and its remaining runtime, so
// a boiler one minute from collapse is visible.
func TestPowerConsumersReportFuelInventoryAndRemainingSeconds(t *testing.T) {
	response := `{"tick":9,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},
	  "powerEntities":[{"name":"boiler","type":"boiler","position":{"x":1,"y":1},"unitNumber":"31","status":"working",
	    "burningFuelJoules":190103,"fuel":[{"name":"coal","quality":"normal","count":7}],
	    "estimatedSecondsOfFuel":95.5,"powerFluids":[{"box":1,"name":"water","amount":100,"temperatureCelsius":15}]}]}`
	r, commands := serveLuaOnce(t, response)
	page, err := powerConsumersQuery(r, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	entity := page.PowerEntities[0]
	if len(entity.Fuel) != 1 || entity.Fuel[0].Count != 7 || entity.EstimatedSecondsOfFuel == nil || *entity.EstimatedSecondsOfFuel != 95.5 {
		t.Fatalf("fuel state: %+v", entity)
	}
	lua := <-commands
	for _, want := range []string{
		`fuel=fuel or {}`,
		`estimatedSecondsOfFuel=seconds`,
		`issue = fuel ~= nil and #fuel == 0`,
		`prototype.fuel_value`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("powerConsumers Lua missing %q\n%s", want, lua)
		}
	}
}

// An omitted window must mean ONE_MINUTE instead of being rejected, and
// powerConsumers treats the network window as a documented no-op rather than
// erroring, because the field is nullable and entity rows are instantaneous.
func TestPowerWindowIsOptional(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player","pageInfo":{"total":0,"limit":10,"offset":0,"hasMore":false},"powerEntities":[]}`)
	if _, err := powerConsumersQuery(r, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10}); err != nil {
		t.Fatalf("omitted window rejected: %v", err)
	}
	if lua := <-commands; !strings.Contains(lua, "local flow_precision = defines.flow_precision_index.one_minute") {
		t.Fatalf("default window missing:\n%s", lua)
	}
	r2, commands2 := serveLuaOnce(t, `{"tick":5,"surface":"nauvis","force":"player","pageInfo":{"total":0,"limit":10,"offset":0,"hasMore":false},"powerEntities":[]}`)
	if _, err := powerConsumersQuery(r2, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10, Window: new(ProductionWindowOneHour)}); err != nil {
		t.Fatalf("powerConsumers must ignore the network window: %v", err)
	}
	<-commands2
	r3 := &Resolver{addr: "127.0.0.1:1", password: "password"}
	if _, err := electricNetworksQuery(r3, &PowerInsightsInput{Surface: "nauvis", Force: "player", Limit: 10, Window: new(ProductionWindow("ONE_DAY"))}); err == nil ||
		!strings.Contains(err.Error(), "invalid production window") {
		t.Fatalf("invalid window accepted: %v", err)
	}
}
