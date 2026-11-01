package main

import (
	"net"
	"strings"
	"sync"
	"testing"
)

// scriptedRCON answers each RCON command with the response of the first rule
// whose marker the Lua body contains, and records every command. It lets a
// composed query run its several independent reads against canned game state.
type scriptedRCON struct {
	mu       sync.Mutex
	commands []string
}

type scriptRule struct{ marker, response string }

func (s *scriptedRCON) seen(marker string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, command := range s.commands {
		if strings.Contains(command, marker) {
			n++
		}
	}
	return n
}

func serveScriptedRCON(t *testing.T, rules []scriptRule) (*Resolver, *scriptedRCON) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	recorder := &scriptedRCON{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if _, _, _, err := readPacket(conn); err != nil {
					return
				}
				if err := writePacket(conn, 1, 2, ""); err != nil {
					return
				}
				_, _, body, err := readPacket(conn)
				if err != nil {
					return
				}
				recorder.mu.Lock()
				recorder.commands = append(recorder.commands, body)
				recorder.mu.Unlock()
				response := `{"error":"unscripted command"}`
				for _, rule := range rules {
					if strings.Contains(body, rule.marker) {
						response = rule.response
						break
					}
				}
				_ = writePacket(conn, 2, 0, response+"\n")
				_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			}(conn)
		}
	}()
	return &Resolver{addr: listener.Addr().String(), password: "password"}, recorder
}

func briefingRules() []scriptRule {
	return []scriptRule{
		{"get_item_production_statistics(surface)", `{"tick":100,"surface":"nauvis","force":"player","window":"ONE_MINUTE","items":[{"item":"iron-plate","produced":60,"consumed":30,"producedPerMinute":60,"consumedPerMinute":30}]}`},
		{`local types = {"assembling-machine"`, `{"tick":101,"surface":"nauvis","force":"player","pageInfo":{"total":4,"limit":1000,"offset":0,"hasMore":false},"machines":[
			{"name":"stone-furnace","type":"furnace","position":{"x":3.5,"y":2.5},"status":"no_fuel","fuel":{}},
			{"name":"assembling-machine-1","type":"assembling-machine","position":{"x":8.5,"y":1.5},"status":"fluid_ingredient_shortage","fuel":{}},
			{"name":"electric-mining-drill","type":"mining-drill","position":{"x":1.5,"y":9.5},"status":"no_power","fuel":{}},
			{"name":"lab","type":"lab","position":{"x":5.5,"y":5.5},"status":"working","fuel":{}}],
			"summaries":[{"type":"furnace","total":1,"working":0,"idle":0,"noFuel":1,"noPower":0,"noIngredients":0,"outputFull":0,"other":0}]}`},
		{"by_id, networks", `{"tick":102,"surface":"nauvis","force":"player","pageInfo":{"total":2,"limit":1000,"offset":0,"hasMore":false},"networks":[{"id":"1","polesInArea":3,"productionWatts":900000,"consumptionWatts":400000},{"id":"2","polesInArea":1,"productionWatts":0,"consumptionWatts":60}]}`},
		{"researchEnabled=force.research_enabled", `{"tick":103,"force":"player","researchEnabled":true,"researchQueue":{},"pageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"technologies":{}}`},
		{"enemy_limit", `{"tick":104,"surface":"nauvis","force":"player","enemies":{"pageInfo":{"total":1,"limit":20,"offset":0,"hasMore":false},"entities":[{"name":"small-biter","type":"unit","force":"enemy","position":{"x":-40.5,"y":3.5},"pollution":{"position":{"x":0,"y":0},"amount":0}}]},"damaged":{"pageInfo":{"total":1,"limit":20,"offset":0,"hasMore":false},"entities":[{"name":"stone-wall","type":"wall","force":"player","position":{"x":-4.5,"y":0.5},"health":50,"maxHealth":350,"pollution":{"position":{"x":0,"y":0},"amount":0}}]},"playerAlerts":{"available":false,"pageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"alerts":{}},"evolutionFactor":0,"evolutionByTime":0,"evolutionByPollution":0,"evolutionByKills":0,"totalPollution":0,"killCounts":{}}`},
		{"storage_stock(area,false)", `{"tick":105,"surface":"nauvis","force":"player","area":{"leftTopX":0,"leftTopY":0,"rightBottomX":10,"rightBottomY":10},"pageInfo":{"total":2,"limit":1000,"offset":0,"hasMore":false},"ghostEntries":[{"id":"a","kind":"entity-ghost","name":"transport-belt","quality":"normal","position":{"x":1.5,"y":1.5},"placementAlternatives":{},"requestedMaterials":{},"buildable":true},{"id":"b","kind":"entity-ghost","name":"inserter","quality":"normal","position":{"x":2.5,"y":1.5},"placementAlternatives":{},"requestedMaterials":{},"buildable":false}]}`},
		{"to_be_deconstructed", `{"tick":106,"surface":"nauvis","state":"DECONSTRUCTION","pageInfo":{"total":7,"limit":1,"offset":0,"hasMore":true},"orders":[{"name":"tree-01","position":{"x":1.5,"y":1.5}}]}`},
		{"to_be_upgraded", `{"tick":107,"surface":"nauvis","state":"UPGRADE","pageInfo":{"total":2,"limit":1,"offset":0,"hasMore":true},"orders":[{"name":"transport-belt","position":{"x":1.5,"y":1.5},"upgradeTarget":"fast-transport-belt","upgradeQuality":"normal"}]}`},
		{"totals_by_key", `{"tick":108,"surface":"nauvis","force":"player","locationPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"locations":{},"totalPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"totals":{},"itemPageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},"items":[{"item":"iron-plate","quality":"normal","count":12}]}`},
	}
}

// The briefing must map every unhealthy source into one ordered attention list
// with severities, keep exact per-kind counts beside the limited list, and
// report each section's own read tick.
func TestFactoryBriefingAttentionMappingAndOrder(t *testing.T) {
	r, recorder := serveScriptedRCON(t, briefingRules())
	area := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10}
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{
		Surface: "nauvis", Force: "player", Area: area, Window: ProductionWindowOneMinute, AttentionLimit: 50,
		WatchItems: []*BriefingWatchItemInput{{Item: "iron-plate", Minimum: 100}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.ProductionTick != 100 || result.MachineTick != 101 || result.PowerTick != 102 || result.ResearchTick != 103 || result.SafetyTick != 104 {
		t.Fatalf("section ticks: %+v", result)
	}
	if result.ConstructionTick != 107 {
		t.Fatalf("constructionTick=%v, want the latest of ghosts/orders reads (107)", result.ConstructionTick)
	}
	if result.StockTick == nil || *result.StockTick != 108 {
		t.Fatalf("stockTick: %v", result.StockTick)
	}
	if len(result.Production) != 1 || result.Production[0].Item != "iron-plate" {
		t.Fatalf("production passthrough: %+v", result.Production)
	}
	if result.Power.Networks != 2 || result.Power.ProductionWatts != 900000 || result.Power.ConsumptionWatts != 400060 {
		t.Fatalf("power: %+v", result.Power)
	}
	if !result.Research.Enabled || result.Research.CurrentTechnology != nil {
		t.Fatalf("research: %+v", result.Research)
	}
	if got := result.Construction; got.Ghosts == nil || *got.Ghosts != 2 || got.GhostsBuildable == nil || *got.GhostsBuildable != 1 || got.DeconstructionOrders != 7 || got.UpgradeOrders != 2 {
		t.Fatalf("construction: %+v", got)
	}

	want := []struct {
		kind     AttentionKind
		severity AttentionSeverity
	}{
		{AttentionKindEnemy, AttentionSeverityCritical},
		{AttentionKindNoPower, AttentionSeverityCritical},
		{AttentionKindNoFuel, AttentionSeverityCritical},
		{AttentionKindLowStock, AttentionSeverityCritical},
		{AttentionKindFluidShortage, AttentionSeverityWarning},
		{AttentionKindDamaged, AttentionSeverityWarning},
		{AttentionKindNoResearch, AttentionSeverityInfo},
		{AttentionKindGhostBacklog, AttentionSeverityInfo},
		{AttentionKindDeconstruction, AttentionSeverityInfo},
	}
	if len(result.Attention) != len(want) || result.AttentionTotal != len(want) {
		t.Fatalf("attention=%d total=%d, want %d: %+v", len(result.Attention), result.AttentionTotal, len(want), result.Attention)
	}
	for i, w := range want {
		got := result.Attention[i]
		if got.Kind != w.kind || got.Severity != w.severity {
			t.Fatalf("attention[%d]=%s/%s, want %s/%s (%+v)", i, got.Kind, got.Severity, w.kind, w.severity, got)
		}
	}
	furnace := result.Attention[2]
	if furnace.Entity == nil || *furnace.Entity != "stone-furnace" || furnace.Position == nil || furnace.Position.X != 3.5 || furnace.Count != 1 {
		t.Fatalf("furnace attention: %+v", furnace)
	}
	if ghost := result.Attention[7]; ghost.Count != 2 || ghost.Position != nil {
		t.Fatalf("aggregate ghost attention: %+v", ghost)
	}
	if deconstruction := result.Attention[8]; deconstruction.Count != 7 {
		t.Fatalf("aggregate deconstruction attention: %+v", deconstruction)
	}

	counts := map[AttentionKind]int{}
	for _, c := range result.AttentionCounts {
		counts[c.Kind] = c.Count
	}
	if counts[AttentionKindNoFuel] != 1 || counts[AttentionKindEnemy] != 1 || counts[AttentionKindGhostBacklog] != 2 || counts[AttentionKindDeconstruction] != 7 || counts[AttentionKindLowStock] != 1 {
		t.Fatalf("attention counts: %+v", result.AttentionCounts)
	}
	if len(result.Stockouts) != 1 || result.Stockouts[0].Item != "iron-plate" || result.Stockouts[0].Have != 12 || result.Stockouts[0].Shortfall != 88 || result.Stockouts[0].ActorHas != nil {
		t.Fatalf("stockouts: %+v", result.Stockouts)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("complete briefing must not warn: %v", result.Warnings)
	}
	for _, marker := range []string{"get_item_production_statistics(surface)", `local types = {"assembling-machine"`, "by_id, networks", "enemy_limit", "storage_stock(area,false)", "to_be_deconstructed", "to_be_upgraded", "totals_by_key"} {
		if recorder.seen(marker) != 1 {
			t.Fatalf("read %q ran %d times, want exactly one independent call", marker, recorder.seen(marker))
		}
	}
}

// attentionLimit must cut the list after severity ordering without hiding the
// total or the per-kind counts, and a quiet factory must report nothing.
func TestFactoryBriefingLimitKeepsCriticalFirst(t *testing.T) {
	r, _ := serveScriptedRCON(t, briefingRules())
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{
		Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 2,
		Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attention) != 2 || result.AttentionTotal != 8 {
		t.Fatalf("limit result: attention=%d total=%d", len(result.Attention), result.AttentionTotal)
	}
	for _, item := range result.Attention {
		if item.Severity != AttentionSeverityCritical {
			t.Fatalf("limited list must keep the most severe first: %+v", item)
		}
	}
	if result.StockTick != nil || len(result.Stockouts) != 0 {
		t.Fatalf("no watch items must skip stock: tick=%v stockouts=%v", result.StockTick, result.Stockouts)
	}
}

// Without an area the ghost scan has no bound, so it must be skipped and named
// instead of silently scanning the surface or reporting zero.
func TestFactoryBriefingWithoutAreaSkipsGhosts(t *testing.T) {
	r, recorder := serveScriptedRCON(t, briefingRules())
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Construction.Ghosts != nil || result.Construction.GhostsBuildable != nil {
		t.Fatalf("ghost counts must be null without an area: %+v", result.Construction)
	}
	if recorder.seen("storage_stock(area,false)") != 0 {
		t.Fatalf("ghost scan must not run without an area")
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "ghost backlog skipped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing ghost warning: %v", result.Warnings)
	}
}

// A truncated machine scan must be disclosed because per-machine attention
// would otherwise understate the problem.
func TestFactoryBriefingWarnsOnTruncatedMachineScan(t *testing.T) {
	rules := briefingRules()
	rules[1] = scriptRule{`local types = {"assembling-machine"`, `{"tick":101,"surface":"nauvis","force":"player","pageInfo":{"total":9000,"limit":1000,"offset":0,"hasMore":true},"machines":[{"name":"stone-furnace","type":"furnace","position":{"x":3.5,"y":2.5},"status":"no_fuel","fuel":{}}],"summaries":[]}`}
	r, recorder := serveScriptedRCON(t, rules)
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.seen(`local types = {"assembling-machine"`); got != briefingMachinePages {
		t.Fatalf("machine pages read=%d, want the %d-page cap", got, briefingMachinePages)
	}
	warned := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "of 9000 machines") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing truncation warning: %v", result.Warnings)
	}
}

// A failing section must fail the whole briefing rather than return silently
// partial data.
func TestFactoryBriefingSectionErrorAborts(t *testing.T) {
	rules := briefingRules()
	rules[2] = scriptRule{"by_id, networks", `{"error":"surface not found"}`}
	r, _ := serveScriptedRCON(t, rules)
	_, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err == nil || !strings.Contains(err.Error(), "power:") || !strings.Contains(err.Error(), "surface not found") {
		t.Fatalf("want power section error, got %v", err)
	}
}

// Invalid requests must fail before any RCON dial.
func TestFactoryBriefingValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	base := func() *FactoryBriefingInput {
		return &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20}
	}
	cases := []struct {
		name   string
		mutate func(*FactoryBriefingInput)
		want   string
	}{
		{"empty surface", func(in *FactoryBriefingInput) { in.Surface = "" }, "surface and force are required"},
		{"empty force", func(in *FactoryBriefingInput) { in.Force = "" }, "surface and force are required"},
		{"limit low", func(in *FactoryBriefingInput) { in.AttentionLimit = 0 }, "attentionLimit must be between 1 and 100"},
		{"limit high", func(in *FactoryBriefingInput) { in.AttentionLimit = 101 }, "attentionLimit must be between 1 and 100"},
		{"bad window", func(in *FactoryBriefingInput) { in.Window = ProductionWindow("WEEK") }, "invalid production window"},
		{"huge area", func(in *FactoryBriefingInput) { in.Area = &FactoryAreaInput{RightBottomX: 300, RightBottomY: 8} }, "no greater than 256"},
		{"inverted area", func(in *FactoryBriefingInput) {
			in.Area = &FactoryAreaInput{LeftTopX: 5, RightBottomX: 1, RightBottomY: 8}
		}, "positive dimensions"},
		{"too many watch items", func(in *FactoryBriefingInput) {
			for i := 0; i < briefingMaxWatchItems+1; i++ {
				in.WatchItems = append(in.WatchItems, &BriefingWatchItemInput{Item: string(rune('a' + i)), Minimum: 1})
			}
		}, "at most 20 items"},
		{"empty watch item", func(in *FactoryBriefingInput) { in.WatchItems = []*BriefingWatchItemInput{{Minimum: 1}} }, "must name an item"},
		{"duplicate watch item", func(in *FactoryBriefingInput) {
			in.WatchItems = []*BriefingWatchItemInput{{Item: "iron-plate", Minimum: 1}, {Item: "iron-plate", Minimum: 2}}
		}, "is duplicated"},
		{"zero minimum", func(in *FactoryBriefingInput) { in.WatchItems = []*BriefingWatchItemInput{{Item: "iron-plate"}} }, "minimum must be finite and greater than 0"},
		{"bad actor", func(in *FactoryBriefingInput) { in.Actor = &ActorInput{} }, "exactly one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base()
			tc.mutate(input)
			_, err := factoryBriefingQuery(r, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	if _, err := factoryBriefingQuery(r, nil); err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("nil input: %v", err)
	}
}
