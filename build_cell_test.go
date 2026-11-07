package main

import (
	"strings"
	"testing"
)

// A cell entry must name exactly one action: silently ignoring a second action
// would build something other than what the caller described.
func TestValidateCellEntriesRejectsAmbiguousAction(t *testing.T) {
	_, err := validateCellEntries([]*BuildCellEntryInput{{
		Ref:   "belt",
		Place: &CellPlaceInput{Item: "transport-belt", Position: &PositionInput{X: 1.5, Y: 2.5}},
		Recipe: &CellRecipeInput{
			Recipe: "iron-gear-wheel",
			Target: &EntityRefInput{},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "exactly one of place, connection or recipe") {
		t.Fatalf("err = %v, want single-action refusal", err)
	}
}

// Refs are the only way a later entry reaches an earlier entity, so a duplicate
// ref would silently retarget every consumer.
func TestValidateCellEntriesRejectsDuplicateRef(t *testing.T) {
	_, err := validateCellEntries([]*BuildCellEntryInput{
		{Ref: "a", Place: &CellPlaceInput{Item: "transport-belt", Position: &PositionInput{X: 1.5, Y: 2.5}}},
		{Ref: "a", Place: &CellPlaceInput{Item: "transport-belt", Position: &PositionInput{X: 2.5, Y: 2.5}}},
	})
	if err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("err = %v, want duplicate-ref refusal", err)
	}
}

// A recipe ref must name an earlier entry; a forward or unknown ref has no
// entity to configure at apply time.
func TestValidateCellEntriesRejectsUnknownRecipeRef(t *testing.T) {
	ref := "later"
	_, err := validateCellEntries([]*BuildCellEntryInput{
		{Ref: "first", Recipe: &CellRecipeInput{Recipe: "iron-gear-wheel", Ref: &ref}},
		{Ref: "later", Place: &CellPlaceInput{Item: "assembling-machine-1", Position: &PositionInput{X: 3.5, Y: 3.5}}},
	})
	if err == nil || !strings.Contains(err.Error(), "must name an earlier entry") {
		t.Fatalf("err = %v, want earlier-ref refusal", err)
	}
}

// A recipe target is either a ref or an explicit entity, never both and never
// neither.
func TestValidateCellEntriesRejectsRecipeWithoutTarget(t *testing.T) {
	_, err := validateCellEntries([]*BuildCellEntryInput{{
		Ref:    "recipe",
		Recipe: &CellRecipeInput{Recipe: "iron-gear-wheel"},
	}})
	if err == nil || !strings.Contains(err.Error(), "exactly one of ref or target") {
		t.Fatalf("err = %v, want target exclusivity refusal", err)
	}
}

func TestValidateCellEntriesAcceptsOrderedLayout(t *testing.T) {
	ref := "gear"
	entries, err := validateCellEntries([]*BuildCellEntryInput{
		{Ref: "gear", Place: &CellPlaceInput{Item: "assembling-machine-1", Position: &PositionInput{X: 10.5, Y: 10.5}}},
		{Ref: "feed", Connection: &CellConnectionInput{
			From: &PositionInput{X: 10.5, Y: 13.5}, To: &PositionInput{X: 14.5, Y: 13.5},
		}},
		{Ref: "recipe", Recipe: &CellRecipeInput{Recipe: "iron-gear-wheel", Ref: &ref}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 || entries[0].kind != "PLACE" || entries[1].kind != "CONNECTION" || entries[2].kind != "RECIPE" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[2].recipe.Ref == nil || *entries[2].recipe.Ref != "gear" {
		t.Fatalf("recipe ref = %+v", entries[2].recipe.Ref)
	}
}

// buildConnection maps the mutation input onto the planner input field by field:
// a dropped field would plan a different route than the one that gets built.
func TestConnectionPlanInputFromPreservesRouteOptions(t *testing.T) {
	item := "underground-belt"
	from := &PositionInput{X: 1.5, Y: 2.5}
	to := &PositionInput{X: 9.5, Y: 2.5}
	in := ConnectEntitiesInput{
		Surface: "nauvis", Force: "player", From: from, To: to,
		Kind: ConnectionKindBelt, Item: &item, Bend: ConnectionBendAuto, Routing: ConnectionRoutingAstar,
		AllowUnderground: true, UndergroundItem: &item, MaxUndergroundDistance: new(3),
		PreferExisting: true, MaxExpandedTiles: new(1234), SearchMargin: new(7),
		AllowBeltAdjacency: true, AllowFluidAdjacency: true,
	}
	plan := connectionPlanInputFrom(in)
	if plan.From != from || plan.To != to || plan.Surface != "nauvis" || plan.Force != "player" {
		t.Fatalf("plan endpoints = %+v", plan)
	}
	if plan.Kind != ConnectionKindBelt || plan.Routing != ConnectionRoutingAstar || !plan.AllowUnderground {
		t.Fatalf("plan route options = %+v", plan)
	}
	if plan.Item == nil || *plan.Item != item || plan.UndergroundItem == nil || *plan.UndergroundItem != item {
		t.Fatalf("plan items = %+v", plan)
	}
	if plan.MaxUndergroundDistance == nil || *plan.MaxUndergroundDistance != 3 {
		t.Fatalf("maxUndergroundDistance = %v", plan.MaxUndergroundDistance)
	}
	if plan.SearchMargin == nil || *plan.SearchMargin != 7 {
		t.Fatalf("searchMargin = %v", plan.SearchMargin)
	}
	if !plan.PreferExisting || !plan.AllowBeltAdjacency || !plan.AllowFluidAdjacency {
		t.Fatalf("plan flags = %+v", plan)
	}
}
