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
		{"area too wide", BuildGhostsInput{Area: constructionArea(maxAreaTiles+1, 10), Limit: 10}, "area"},
		{"limit zero", BuildGhostsInput{Area: constructionArea(10, 10)}, "limit"},
		{"limit too high", BuildGhostsInput{Area: constructionArea(10, 10), Limit: maxPageSize + 1}, "limit"},
		{"invalid mode", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Mode: BuildGhostsMode("BUILD")}, "mode"},
		{"empty surface", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Surface: new("")}, "surface"},
		{"empty force", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Force: new("")}, "force"},
		{"both actors", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Actor: &ActorInput{Character: new("4"), Player: new("wricardo")}}, "exactly one"},
		{"bad character", BuildGhostsInput{Area: constructionArea(10, 10), Limit: 10, Actor: &ActorInput{Character: new("abc")}}, "character"},
	}
	for _, test := range tests {
		_, err := buildGhosts(&Resolver{}, test.input)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
}

func TestBuildGhostsTypedBatchErrorDecode(t *testing.T) {
	result, err := factoryDecode[BuildGhostsResult](`{"tick":42,"surface":"nauvis","force":"player","mode":"REVIVE","revived":0,"failed":1,"missing":[],"entities":[],"errors":[{"index":1,"code":"COLLISION","message":"blocked","target":{"surface":"nauvis","entity":"entity-ghost","position":{"x":2.5,"y":3.5}}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 1 || result.Errors[0].Index != 1 || result.Errors[0].Code != BatchErrorCodeCollision || result.Errors[0].Target == nil || result.Errors[0].Target.Entity == nil || *result.Errors[0].Target.Entity != "entity-ghost" {
		t.Fatalf("errors = %+v", result.Errors)
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
		{"limit too high", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: maxPageSize + 1}, "limit"},
		{"negative offset", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, Offset: -1}, "offset"},
		{"items limit too low", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, ItemsLimit: -1}, "itemsLimit"},
		{"items limit too high", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, ItemsLimit: maxPageSize + 1}, "itemsLimit"},
		{"negative items offset", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, ItemsOffset: -1}, "itemsOffset"},
		{"bad area", &LogisticNetworksInput{Surface: "nauvis", Force: "player", Limit: 10, Area: constructionArea(maxAreaTiles+1, 10)}, "area"},
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

func TestLogisticNetworkDecode(t *testing.T) {
	response := `{"tick":42,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"networks":[{"id":"7","force":"player","surface":"nauvis","cells":3,"custom_name":"main bus","robot_limit":4294967295,"logistic_robots":{"total":5,"available":2},"construction_robots":{"total":1,"available":1},"contents":[{"name":"iron-plate","count":12,"quality":"normal"}],"requester_points":2,"provider_points":4,"deployed_logistic_robots":3,"carrying_logistic_robots":2,"points":{"roboports":1,"requesterChests":2,"bufferChests":0,"passiveProviderChests":3,"activeProviderChests":0,"storageChests":1,"characterRequestPoints":1,"other":0},"items":[{"name":"electronic-circuit","quality":"normal","stored":7,"storedPassiveProviders":7,"storedActiveProviders":0,"storedBuffers":0,"storedStorage":0,"available":-3,"requested":50,"requestedMinimum":50,"unlimitedRequests":0,"delivered":44,"inFlight":2,"incoming":4,"targetedPickup":4,"missing":2},{"name":"iron-plate","quality":null,"stored":0,"available":-2,"requested":20,"requestedMinimum":0,"unlimitedRequests":1,"delivered":0,"inFlight":0,"incoming":0,"targetedPickup":0,"missing":20}],"item_page_info":{"total":2,"limit":1000,"offset":0,"hasMore":false}}]}`
	page, err := factoryDecode[LogisticNetworkPage](response)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.PageInfo == nil || page.PageInfo.Total != 1 || len(page.Networks) != 1 {
		t.Fatalf("page = %+v", page)
	}
	network := page.Networks[0]
	if network.ID != "7" || network.Cells != 3 {
		t.Fatalf("network = %+v", network)
	}
	if network.CustomName == nil || *network.CustomName != "main bus" || network.RobotLimit != 4294967295 {
		t.Fatalf("network identity = %+v", network)
	}
	if network.LogisticRobots == nil || network.LogisticRobots.Total != 5 || network.LogisticRobots.Available != 2 {
		t.Fatalf("logistic robots = %+v", network.LogisticRobots)
	}
	if network.ConstructionRobots == nil || network.ConstructionRobots.Total != 1 {
		t.Fatalf("construction robots = %+v", network.ConstructionRobots)
	}
	if len(network.Contents) != 1 || network.Contents[0].Name != "iron-plate" {
		t.Fatalf("contents = %+v", network.Contents)
	}
	if network.RequesterPoints != 2 || network.ProviderPoints != 4 {
		t.Fatalf("points = %d/%d", network.RequesterPoints, network.ProviderPoints)
	}
	if network.DeployedLogisticRobots != 3 || network.CarryingLogisticRobots != 2 {
		t.Fatalf("deployed/carrying = %d/%d", network.DeployedLogisticRobots, network.CarryingLogisticRobots)
	}
	if network.Points == nil || network.Points.Roboports != 1 || network.Points.RequesterChests != 2 || network.Points.CharacterRequestPoints != 1 {
		t.Fatalf("point breakdown = %+v", network.Points)
	}
	if network.ItemPageInfo == nil || network.ItemPageInfo.Total != 2 || network.ItemPageInfo.Limit != 1000 {
		t.Fatalf("item page = %+v", network.ItemPageInfo)
	}
	if len(network.Items) != 2 {
		t.Fatalf("items = %+v", network.Items)
	}
	circuit := network.Items[0]
	if circuit.Name != "electronic-circuit" || circuit.Quality == nil || *circuit.Quality != "normal" {
		t.Fatalf("circuit = %+v", circuit)
	}
	if circuit.Stored != 7 || circuit.Available != -3 || circuit.Requested != 50 || circuit.Delivered != 44 || circuit.InFlight != 2 || circuit.Incoming != 4 || circuit.TargetedPickup != 4 || circuit.Missing != 2 {
		t.Fatalf("circuit stats = %+v", circuit)
	}
	plate := network.Items[1]
	if plate.Quality != nil || plate.UnlimitedRequests != 1 || plate.Stored != 0 || plate.Missing != 20 {
		t.Fatalf("plate stats = %+v", plate)
	}
}

func TestDeconstructionAreaFilterValidation(t *testing.T) {
	valid := func() DeconstructAreaInput {
		return DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10)}
	}
	tooManyNames := make([]string, maxBatchTargets+1)
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
		{"limit too high", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Limit: new(maxPageSize + 1)}, false, "limit"},
		{"empty name", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Names: []string{"transport-belt", " "}}, false, "names"},
		{"too many names", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Names: tooManyNames}, false, "names"},
		{"empty type", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Types: []string{""}}, false, "types"},
		{"both actors", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Actor: &ActorInput{Character: new("4"), Player: new("wricardo")}}, false, "exactly one"},
		{"bad character", DeconstructAreaInput{Surface: "nauvis", Area: constructionArea(10, 10), Actor: &ActorInput{Character: new("four")}}, false, "character"},
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

// TestLogisticMissingUsesRequestMinimum proves a vanilla 2.0 requester chest,
// which compiles to a minimum count with no maximum, still reports a shortfall,
// and that carried robot cargo is subtracted.
func TestLogisticMissingUsesRequestMinimum(t *testing.T) {
	if _, err := logisticNetworks(nil, &LogisticNetworksInput{Surface: "", Force: "player", Limit: 10}); err == nil {
		t.Fatal("an empty surface must fail before RCON")
	}
	stubs := `
defines={inventory={chest=1},logistic_mode={storage=1,buffer=2,active_provider=3,passive_provider=4,requester=5}}
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=100,surfaces={nauvis=surface},forces={player=force}}
local chest={type="logistic-container",valid=true,unit_number=90,position={x=1.5,y=2.5}}
chest.get_inventory=function(kind) return {get_contents=function() return {{name="iron-plate",count=30,quality="normal"}} end} end
local requester_point={owner=chest,filters={{name="iron-plate",quality="normal",count=100}},targeted_items_deliver={}}
local net={network_id=7,robot_limit=100,cells={},
  get_contents=function() return {} end,
  requester_points={requester_point},provider_points={},storage_points={},
  logistic_robots={},all_logistic_robots=0,available_logistic_robots=0,
  all_construction_robots=0,available_construction_robots=0}
local roboport={logistic_network=net}
surface.find_entities_filtered=function(filter)
  if filter.type=="roboport" then return {roboport} end
  return {}
end
`
	assertions := `
local network=captured.networks[1]
assert(network~=nil,"one network expected")
local row=network.items[1]
assert(row.name=="iron-plate")
assert(row.requestedMinimum==100,"the request minimum must be summed")
assert(row.unlimitedRequests==1,"a slot without a maximum is unlimited")
assert(row.requested==0,"a vanilla requester has no maximum request")
assert(row.delivered==30,"chest contents count as delivered")
assert(row.missing==70,"a vanilla requester shortage must be visible, got "..tostring(row.missing))
`
	lua := logisticNetworksLua("nauvis", "player", "nil", "", 10, 0, true, 10, 0)
	runLuaFixture(t, lua, stubs, assertions)
}

// TestBuildGhostsRaiseReviveFlagsMatchSchema proves REVIVE raises revive events
// and SILENT does not, that collided items are reported separately from missing
// materials, and that an actor's main inventory is passed as overflow.
func TestBuildGhostsRaiseReviveFlagsMatchSchema(t *testing.T) {
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
local main={kind="main"}
local actor={type="character",unit_number=42,get_main_inventory=function() return main end}
local revived_entity={valid=true,name="transport-belt",type="transport-belt",position={x=1.5,y=1.5},unit_number=77}
local ghost={valid=true,name="entity-ghost",position={x=1.5,y=1.5},
  ghost_prototype={items_to_place_this={{name="transport-belt",count=1}}}}
local recorded={}
ghost.revive=function(params) recorded.revive=params; return {{name="iron-plate",count=3,quality="normal"}},revived_entity end
ghost.silent_revive=function(params) recorded.silent=params; return {{name="iron-plate",count=3,quality="normal"}},revived_entity end
game={tick=55,surfaces={nauvis=surface},forces={player=force},get_entity_by_unit_number=function(unit) return actor end}
surface.find_entities_filtered=function(filter) return {ghost} end
`
	assertions := `
assert(recorded.revive~=nil,"REVIVE mode must call revive")
assert(recorded.revive.raise_revive==true,"REVIVE must raise script_raised_revive")
assert(recorded.revive.overflow==main,"an actor must supply the overflow inventory")
assert(recorded.silent==nil)
assert(captured.revived==1 and captured.failed==0)
assert(#captured.missing==0,"collided items must not be reported as missing materials")
assert(#captured.collidedItems==1 and captured.collidedItems[1].name=="iron-plate" and captured.collidedItems[1].count==3)
`
	bounds := areaBounds{LeftTopX: 0, LeftTopY: 0, RightBottomX: 5, RightBottomY: 5}
	runLuaFixture(t, buildGhostsLua("nauvis", "player", bounds, "REVIVE", 10, 42, ""), stubs, assertions)

	silentAssertions := `
assert(recorded.silent~=nil,"SILENT mode must call silent_revive")
assert(recorded.silent.raise_revive==false,"SILENT must not raise revive events")
assert(recorded.revive==nil)
`
	runLuaFixture(t, buildGhostsLua("nauvis", "player", bounds, "SILENT", 10, 42, ""), stubs, silentAssertions)
}

// TestUpgradeAreaValidation rejects mappings and areas before RCON.
func TestUpgradeAreaValidation(t *testing.T) {
	area := constructionArea(10, 10)
	cases := []struct {
		name  string
		input UpgradeAreaInput
		want  string
	}{
		{"missing area", UpgradeAreaInput{Mappings: []*UpgradeMappingInput{{From: "transport-belt", To: "fast-transport-belt"}}, Limit: 10}, "area is required"},
		{"empty surface", UpgradeAreaInput{Surface: "", Area: area, Mappings: []*UpgradeMappingInput{{From: "a", To: "b"}}, Limit: 10}, "surface"},
		{"empty force", UpgradeAreaInput{Surface: "nauvis", Force: " ", Area: area, Mappings: []*UpgradeMappingInput{{From: "a", To: "b"}}, Limit: 10}, "force"},
		{"no mappings", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10}, "mappings"},
		{"empty from", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10, Mappings: []*UpgradeMappingInput{{From: " ", To: "b"}}}, "from"},
		{"empty to", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10, Mappings: []*UpgradeMappingInput{{From: "a", To: ""}}}, "to"},
		{"empty quality", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10, Mappings: []*UpgradeMappingInput{{From: "a", To: "b", Quality: new(" ")}}}, "quality"},
		{"duplicate from", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 10, Mappings: []*UpgradeMappingInput{{From: "a", To: "b"}, {From: "a", To: "c"}}}, "repeats from"},
		{"limit zero", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Mappings: []*UpgradeMappingInput{{From: "a", To: "b"}}}, "limit"},
		{"oversized area", UpgradeAreaInput{Surface: "nauvis", Force: "player", Area: constructionArea(maxAreaTiles+1, 10), Mappings: []*UpgradeMappingInput{{From: "a", To: "b"}}, Limit: 10}, "area"},
	}
	for _, tc := range cases {
		if _, err := upgradeArea(nil, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q error, got %v", tc.name, tc.want, err)
		}
	}
	if _, err := cancelUpgradeArea(nil, CancelUpgradeAreaInput{Surface: "nauvis", Force: "player", Area: area, Limit: 0}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("cancel with zero limit: got %v", err)
	}
}

// TestUpgradeAreaOrdersUpgrades proves the scripted order path records the
// mapped prototype and quality, reports already-marked entities, and classifies
// unknown target prototypes as LOOKUP.
func TestUpgradeAreaOrdersUpgrades(t *testing.T) {
	stubs := `
local surface={name="nauvis",index=1}
local force={name="player"}
game={tick=200,surfaces={nauvis=surface},forces={player=force}}
prototypes={entity={["fast-transport-belt"]={name="fast-transport-belt"},["transport-belt"]={name="transport-belt"}},quality={normal={name="normal"}}}
local marked=false
local belt={valid=true,name="transport-belt",type="transport-belt",unit_number=8,position={x=1.5,y=1.5},
  to_be_upgraded=function() return marked end}
local recorded={}
belt.order_upgrade=function(params) recorded.order=params; marked=true; return true end
local already={valid=true,name="transport-belt",type="transport-belt",unit_number=9,position={x=2.5,y=1.5},
  to_be_upgraded=function() return true end}
local mystery={valid=true,name="splitter",type="splitter",unit_number=10,position={x=3.5,y=1.5},
  to_be_upgraded=function() return false end}
surface.find_entities_filtered=function(filter) return {belt,already,mystery} end
`
	assertions := `
assert(captured.orders==1,"one new order expected, got "..tostring(captured.orders))
assert(recorded.order~=nil,"order_upgrade must be called")
assert(recorded.order.target.name=="fast-transport-belt" and recorded.order.target.quality=="normal")
assert(recorded.order.force==game.forces.player,"the order must belong to the force")
local statuses={}
for _,row in ipairs(captured.rows) do statuses[row.unitNumber]=row.status end
assert(statuses["8"]=="MARKED","a new order must be MARKED")
assert(statuses["9"]=="ALREADY_MARKED","an existing order must be left untouched")
assert(statuses["10"]=="LOOKUP","an unknown target prototype must be LOOKUP")
assert(#captured.errors==1 and captured.errors[1].code=="LOOKUP")
`
	lua := upgradeAreaLua(upgradeAreaOptions{
		Surface: "nauvis", Force: "player", Bounds: areaBounds{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
		Mappings: []upgradeMapping{{From: "transport-belt", To: "fast-transport-belt", Quality: "normal"}, {From: "splitter", To: "fast-splitter", Quality: "normal"}},
		Limit:    10,
	})
	runLuaFixture(t, lua, stubs, assertions)
}
