package main

import (
	"strings"
	"testing"
)

func constructionArea(width, height float64) *FactoryAreaInput {
	return &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: width, RightBottomY: height}
}

func TestBuildGhostsValidation(t *testing.T) {
	tests := []struct {
		name  string
		input BuildGhostsInput
		want  string
	}{
		{"missing area", BuildGhostsInput{Limit: 10}, "area is required"},
		{"area too wide", BuildGhostsInput{Area: constructionArea(257, 10), Limit: 10}, "area"},
		{"limit zero", BuildGhostsInput{Area: constructionArea(10, 10)}, "limit"},
		{"limit too high", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 1001}, "limit"},
		{"invalid mode", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Mode: BuildGhostsMode("BUILD")}, "mode"},
		{"empty surface", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Surface: new("")}, "surface"},
		{"empty force", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Force: new("")}, "force"},
		{"both actors", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Character: new("4"), Player: new("wricardo")}, "exactly one"},
		{"bad character", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Character: new("abc")}, "character"},
	}
	for _, test := range tests {
		_, err := buildGhosts(&Resolver{}, test.input)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
}

func TestBuildGhostsLuaInvariants(t *testing.T) {
	bounds := areaBounds{0, 0, 10, 10}
	revive := buildGhostsLua("nauvis", "player", bounds, string(BuildGhostsModeRevive), 25)
	for _, want := range []string{`type="entity-ghost"`, "g.revive{raise_revive=false}", "ghost_prototype", "items_to_place_this", "limit=25", "local silent=false", "local ok,r1,r2=pcall", "collided=r1", "built=r2", "add_collided(collided)", "if ok and built and built.valid then"} {
		if !strings.Contains(revive, want) {
			t.Errorf("revive Lua missing %q", want)
		}
	}
	if strings.Contains(revive, "g.silent_revive") {
		t.Error("revive mode must not call silent_revive")
	}
	snapshot := strings.Index(revive, "local ghost_position=g.position")
	reviveCall := strings.Index(revive, "g.revive{")
	if snapshot < 0 || reviveCall < 0 || snapshot > reviveCall {
		t.Fatalf("ghost state must be snapshotted before revive (snapshot=%d revive=%d)", snapshot, reviveCall)
	}
	for _, banned := range []string{"g.position", "g.name", "g.valid", "#collided"} {
		if strings.Contains(revive[reviveCall:], banned) {
			t.Errorf("revive path must not touch the ghost object after revival: %q", banned)
		}
	}
	if !strings.Contains(revive, "if g.valid then") {
		t.Error("invalid ghosts must be skipped before acting")
	}
	silent := buildGhostsLua("nauvis", "player", bounds, string(BuildGhostsModeSilent), 10)
	for _, want := range []string{"g.silent_revive{raise_revive=true}", "local silent=true", "if silent then built=r1"} {
		if !strings.Contains(silent, want) {
			t.Errorf("silent mode Lua missing %q", want)
		}
	}
	if strings.Contains(silent, "g.revive{raise_revive=false}") {
		t.Error("silent mode must not call revive")
	}
}

func TestLogisticNetworksValidation(t *testing.T) {
	tests := []struct {
		name  string
		input *LogisticNetworksInput
		want  string
	}{
		{"empty surface", &LogisticNetworksInput{Surface: "", Force: "player", Limit: 10}, "surface"},
		{"empty force", &LogisticNetworksInput{Surface: "nauvis", Force: "", Limit: 10}, "force"},
		{"limit zero", &LogisticNetworksInput{Surface: "nauvis", Force: "player"}, "limit"},
		{"limit too high", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 1001}, "limit"},
		{"negative offset", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, Offset: -1}, "offset"},
		{"bad area", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, Area: constructionArea(300, 10)}, "area"},
		{"empty network id", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, NetworkID: new("")}, "networkId"},
		{"non numeric network id", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, NetworkID: new("cell-7")}, "networkId"},
	}
	for _, test := range tests {
		_, err := logisticNetworks(&Resolver{}, test.input)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
}

func TestLogisticNetworksLuaInvariants(t *testing.T) {
	lua := logisticNetworksLua("nauvis", "player", "nil", "7", 10, 5)
	for _, want := range []string{`type="roboport"`, "rp.logistic_network", "net.network_id", "all_logistic_robots", "available_logistic_robots", "all_construction_robots", "available_construction_robots", `type(all)=="number"`, `type(av)=="number"`, "get_contents", "contents[1]~=nil", "item.quality", "requester_points", "provider_points", "limit,offset=10,5", luaString("7")} {
		if !strings.Contains(lua, want) {
			t.Errorf("logistic networks Lua missing %q", want)
		}
	}
	for _, banned := range []string{"#all", "#av", "#net.all_", "#net.available_"} {
		if strings.Contains(lua, banned) {
			t.Errorf("angular robot counts must not be length-tested: %q", banned)
		}
	}
	bounded := logisticNetworksLua("nauvis", "player", areaBounds{1, 2, 11, 12}.lua(), "", 10, 0)
	if !strings.Contains(bounded, "area={") {
		t.Error("bounded area must be passed to the roboport scan")
	}
}

func TestLogisticNetworkDecode(t *testing.T) {
	response := `{"tick":42,"surface":"nauvis","force":"player","total":1,"limit":100,"offset":0,"networks":[{"id":"7","force":"player","surface":"nauvis","cells":3,"logistic_robots":{"total":5,"available":2},"construction_robots":{"total":1,"available":1},"contents":[{"name":"iron-plate","count":12}],"requester_points":2,"provider_points":4}]}`
	page, err := factoryDecode[LogisticNetworkPage](response)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 || len(page.Networks) != 1 {
		t.Fatalf("page = %+v", page)
	}
	network := page.Networks[0]
	if network.ID != "7" || network.Cells != 3 {
		t.Fatalf("network = %+v", network)
	}
	if network.LogisticRobots == nil || network.LogisticRobots.Total != 5 || network.LogisticRobots.Available != 2 {
		t.Fatalf("logistic robots = %+v", network.LogisticRobots)
	}
	if network.ConstructionRobots == nil || network.ConstructionRobots.Total != 1 {
		t.Fatalf("construction robots = %+v", network.ConstructionRobots)
	}
	if len(network.Contents) != 1 || network.Contents[0].Name == nil || *network.Contents[0].Name != "iron-plate" {
		t.Fatalf("contents = %+v", network.Contents)
	}
	if network.RequesterPoints != 2 || network.ProviderPoints != 4 {
		t.Fatalf("points = %d/%d", network.RequesterPoints, network.ProviderPoints)
	}
}

func TestDeconstructionAreaFilterValidation(t *testing.T) {
	valid := func() DeconstructAreaInput {
		return DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10)}
	}
	tooManyNames := make([]string, 201)
	for i := range tooManyNames {
		tooManyNames[i] = "transport-belt"
	}
	tests := []struct {
		name   string
		input  DeconstructAreaInput
		cancel bool
		want   string
	}{
		{"instant with cancel", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Instant: new(true)}, true, "instant"},
		{"limit zero", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Limit: new(0)}, false, "limit"},
		{"limit too high", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Limit: new(1001)}, false, "limit"},
		{"empty name", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Names: []string{"transport-belt", " "}}, false, "names"},
		{"too many names", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Names: tooManyNames}, false, "names"},
		{"empty type", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Types: []string{""}}, false, "types"},
		{"both actors", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Character: new("4"), Player: new("wricardo")}, false, "exactly one"},
		{"bad character", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Character: new("four")}, false, "character"},
	}
	for _, test := range tests {
		_, err := deconstructionArea(&Resolver{}, test.input, test.cancel)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
	if _, err := deconstructionArea(&Resolver{}, valid(), false); err == nil {
		t.Error("expected a transport error for a valid request without a server")
	} else if strings.Contains(err.Error(), "area") || strings.Contains(err.Error(), "limit") {
		t.Errorf("valid request must fail at the transport, got %v", err)
	}
}

func TestDeconstructionAreaLuaInvariants(t *testing.T) {
	options := deconstructionAreaOptions{
		Surface: "nauvis",
		Force:   "player",
		Bounds:  areaBounds{0, 0, 10, 10},
		Names:   []string{"transport-belt"},
		Types:   []string{"inserter"},
		Limit:   25,
	}
	lua := deconstructionAreaLua(options)
	for _, want := range []string{`find_entities_filtered`, `order_deconstruction`, `removed=0`, `items={}`, `cancel=false`, `limit=25`, luaString("transport-belt"), luaString("inserter")} {
		if !strings.Contains(lua, want) {
			t.Errorf("non-instant Lua missing %q", want)
		}
	}
	cancel := deconstructionAreaLua(deconstructionAreaOptions{Surface: "nauvis", Force: "player", Bounds: areaBounds{0, 0, 10, 10}, Cancel: true, Limit: 25})
	if !strings.Contains(cancel, "cancel_deconstruction") || !strings.Contains(cancel, "cancel=true") {
		t.Error("cancel Lua must cancel deconstruction orders")
	}
	instant := deconstructionAreaLua(deconstructionAreaOptions{Surface: "nauvis", Force: "player", Bounds: areaBounds{0, 0, 10, 10}, Instant: true, Limit: 25, Character: 4})
	for _, want := range []string{"local control=actor or player_object", "control.mine_entity(e)", "destroy{raise_destroy=true}", "deconstruction_inventory_difference", "deconstruction_inventory_counts", "contents[1]~=nil", "item.quality", "removed=removed", `find_entities_filtered`} {
		if !strings.Contains(instant, want) {
			t.Errorf("instant Lua missing %q", want)
		}
	}
	if strings.Contains(instant, "player_object.mine_entity") {
		t.Error("detached characters must not be forced onto the destroy path by player-only mining")
	}
}
