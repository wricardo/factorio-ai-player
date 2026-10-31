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

func TestLogisticNetworkDecode(t *testing.T) {
	response := `{"tick":42,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"networks":[{"id":"7","force":"player","surface":"nauvis","cells":3,"logistic_robots":{"total":5,"available":2},"construction_robots":{"total":1,"available":1},"contents":[{"name":"iron-plate","count":12,"quality":"normal"}],"requester_points":2,"provider_points":4}]}`
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
