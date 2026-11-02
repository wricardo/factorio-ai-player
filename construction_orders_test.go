package main

import (
	"strings"
	"testing"
)

func TestConstructionOrdersValidateBeforeRCON(t *testing.T) {
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 1}
	badState := ConstructionOrderState("BOGUS")
	tests := []struct {
		name  string
		input *ConstructionOrdersInput
		want  string
	}{
		{"surface", &ConstructionOrdersInput{Surface: "", Limit: 10}, "surface"},
		{"limit low", &ConstructionOrdersInput{Surface: "nauvis", Limit: 0}, "limit"},
		{"limit high", &ConstructionOrdersInput{Surface: "nauvis", Limit: maxPageSize + 1}, "limit"},
		{"offset", &ConstructionOrdersInput{Surface: "nauvis", Limit: 10, Offset: -1}, "offset"},
		{"state", &ConstructionOrdersInput{Surface: "nauvis", Limit: 10, State: badState}, "state"},
		{"area", &ConstructionOrdersInput{Surface: "nauvis", Limit: 10, Area: area}, "area"},
	}
	for _, test := range tests {
		if _, err := constructionOrders(&Resolver{addr: "127.0.0.1:1"}, test.input); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
}

func TestConstructionOrdersQueriesMarkedEntities(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":42,"surface":"nauvis","state":"DECONSTRUCTION","pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"orders":[{"name":"wooden-chest","type":"container","position":{"x":1.5,"y":2.5},"unitNumber":"42","force":"player"}]}`)
	result, err := constructionOrders(fake.resolver, &ConstructionOrdersInput{Surface: "nauvis", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.PageInfo == nil || result.PageInfo.Total != 1 || len(result.Orders) != 1 || result.Orders[0].Name != "wooden-chest" {
		t.Fatalf("result = %+v", result)
	}
	if result.Orders[0].UnitNumber == nil || *result.Orders[0].UnitNumber != "42" {
		t.Fatalf("unit number = %v", result.Orders[0].UnitNumber)
	}
}

func TestConstructionOrdersUpgradeReadsTarget(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":43,"surface":"nauvis","state":"UPGRADE","pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},"orders":[{"name":"assembling-machine-1","position":{"x":0.5,"y":0.5},"upgradeTarget":"assembling-machine-2","upgradeQuality":"normal"}]}`)
	result, err := constructionOrders(fake.resolver, &ConstructionOrdersInput{
		Surface: "nauvis",
		State:   ConstructionOrderStateUpgrade,
		Limit:   10,
		Area:    &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Orders) != 1 || result.Orders[0].UpgradeTarget == nil || *result.Orders[0].UpgradeTarget != "assembling-machine-2" {
		t.Fatalf("result = %+v", result)
	}
}

func TestConstructionOrdersEmptyLuaTableDecodesAsList(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":44,"surface":"nauvis","state":"DECONSTRUCTION","pageInfo":{"total":0,"limit":10,"offset":0,"hasMore":false},"orders":{}}`)
	result, err := constructionOrders(fake.resolver, &ConstructionOrdersInput{Surface: "nauvis", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.PageInfo == nil || result.PageInfo.Total != 0 || len(result.Orders) != 0 {
		t.Fatalf("empty orders = %+v", result)
	}
}
