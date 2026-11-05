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
		{"local status_kind=", `{"tick":101,"surface":"nauvis","force":"player","scanned":9,"total":4,"alerts":[
			{"kind":"NO_POWER","severity":"CRITICAL","status":"no_power","name":"electric-mining-drill","type":"mining-drill","position":{"x":1.5,"y":9.5},"unitNumber":11,"direction":0,"suggestedAction":"connect the entity to a powered electric network"},
			{"kind":"NO_FUEL","severity":"CRITICAL","status":"no_fuel","name":"stone-furnace","type":"furnace","position":{"x":3.5,"y":2.5},"unitNumber":12,"direction":0,"suggestedAction":"feed fuel to the entity"},
			{"kind":"NO_RESOURCES","severity":"WARNING","status":"no_minable_resources","name":"electric-mining-drill","type":"mining-drill","position":{"x":6.5,"y":6.5},"unitNumber":13,"direction":0,"suggestedAction":"move the drill onto resources or remove it"},
			{"kind":"FLUID_SHORTAGE","severity":"WARNING","status":"fluid_ingredient_shortage","name":"assembling-machine-1","type":"assembling-machine","position":{"x":8.5,"y":1.5},"unitNumber":14,"direction":0,"suggestedAction":"supply the missing fluid"}],
			"attentionCounts":[{"kind":"NO_POWER","count":1},{"kind":"NO_FUEL","count":1},{"kind":"NO_RESOURCES","count":1},{"kind":"FLUID_SHORTAGE","count":1}],
			"machines":[{"type":"furnace","total":1,"working":0,"idle":0,"noFuel":1,"noPower":0,"noIngredients":0,"outputFull":0,"other":0}]}`},
		{"by_id, networks", `{"tick":102,"surface":"nauvis","force":"player","pageInfo":{"total":2,"limit":1000,"offset":0,"hasMore":false},"networks":[{"id":"1","polesInArea":3,"productionWatts":900000,"consumptionWatts":400000,"maxProductionWatts":1800000,"accumulatorChargeJoules":2500000,"accumulatorCapacityJoules":5000000,"lowPowerConsumers":2,"satisfaction":0.222},{"id":"2","polesInArea":1,"productionWatts":0,"consumptionWatts":60,"maxProductionWatts":0,"accumulatorChargeJoules":0,"accumulatorCapacityJoules":0,"lowPowerConsumers":0}]}`},
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
	// Headroom must be visible: capacity is the sum of per-network maximum
	// production, satisfaction is the observed load ratio (0 with no capacity),
	// and accumulator/low-power facts survive aggregation.
	if result.Power.CapacityWatts != 1800000 {
		t.Fatalf("capacityWatts: %+v", result.Power)
	}
	if want := 400060.0 / 1800000.0; result.Power.Satisfaction != want {
		t.Fatalf("satisfaction=%v, want %v", result.Power.Satisfaction, want)
	}
	if result.Power.AccumulatorChargeJoules != 2500000 || result.Power.AccumulatorCapacityJoules != 5000000 {
		t.Fatalf("accumulator sums: %+v", result.Power)
	}
	if result.Power.LowPowerConsumers != 2 {
		t.Fatalf("lowPowerConsumers: %+v", result.Power)
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
		{AttentionKindNoResources, AttentionSeverityWarning},
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
	if ghost := result.Attention[8]; ghost.Count != 2 || ghost.Position != nil {
		t.Fatalf("aggregate ghost attention: %+v", ghost)
	}
	if deconstruction := result.Attention[9]; deconstruction.Count != 7 {
		t.Fatalf("aggregate deconstruction attention: %+v", deconstruction)
	}
	depleted := result.Attention[4]
	if depleted.Kind != AttentionKindNoResources || depleted.Entity == nil || *depleted.Entity != "electric-mining-drill" ||
		!strings.Contains(depleted.Message, "no_minable_resources") || !strings.Contains(depleted.Message, "move the drill") {
		t.Fatalf("depleted drill attention: %+v", depleted)
	}

	counts := map[AttentionKind]int{}
	for _, c := range result.AttentionCounts {
		counts[c.Kind] = c.Count
	}
	if counts[AttentionKindNoFuel] != 1 || counts[AttentionKindEnemy] != 1 || counts[AttentionKindGhostBacklog] != 2 || counts[AttentionKindDeconstruction] != 7 || counts[AttentionKindLowStock] != 1 {
		t.Fatalf("attention counts: %+v", result.AttentionCounts)
	}
	if counts[AttentionKindNoResources] != 1 || counts[AttentionKindNoPower] != 1 || counts[AttentionKindFluidShortage] != 1 {
		t.Fatalf("status scan counts must cover non-crafting types: %+v", result.AttentionCounts)
	}
	if len(result.Stockouts) != 1 || result.Stockouts[0].Item != "iron-plate" || result.Stockouts[0].Have != 12 || result.Stockouts[0].Shortfall != 88 || result.Stockouts[0].ActorHas != nil {
		t.Fatalf("stockouts: %+v", result.Stockouts)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("complete briefing must not warn: %v", result.Warnings)
	}
	for _, marker := range []string{"get_item_production_statistics(surface)", "local status_kind=", "by_id, networks", "enemy_limit", "storage_stock(area,false)", "to_be_deconstructed", "to_be_upgraded", "totals_by_key"} {
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
	if len(result.Attention) != 2 || result.AttentionTotal != 9 {
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

// Without an area the ghost read must cover the whole surface instead of being
// skipped: the counts are the real backlog and no skip warning is emitted.
func TestFactoryBriefingWithoutAreaCountsWholeSurfaceGhosts(t *testing.T) {
	r, recorder := serveScriptedRCON(t, briefingRules())
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Construction.Ghosts == nil || *result.Construction.Ghosts != 2 || result.Construction.GhostsBuildable == nil || *result.Construction.GhostsBuildable != 1 {
		t.Fatalf("ghost counts must cover the whole surface without an area: %+v", result.Construction)
	}
	if got := recorder.seen("storage_stock(area,false)"); got != 1 {
		t.Fatalf("whole-surface ghost scan ran %d times, want exactly one", got)
	}
	if got := recorder.seen("local area,limit,offset=nil,"); got != 1 {
		t.Fatalf("ghost read must pass a nil area for the whole surface, saw %d such calls", got)
	}
	if got := recorder.seen("local area,limit,offset={{"); got != 0 {
		t.Fatalf("ghost read must not invent bounds without an area, saw %d bounded calls", got)
	}
	counts := map[AttentionKind]int{}
	for _, count := range result.AttentionCounts {
		counts[count.Kind] = count.Count
	}
	if counts[AttentionKindGhostBacklog] != 2 {
		t.Fatalf("whole-surface ghosts must stay in attention counts: %+v", result.AttentionCounts)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "ghost backlog skipped") {
			t.Fatalf("whole-surface ghost read must not warn about a skipped backlog: %v", result.Warnings)
		}
	}
}

// A capped alert row list must be disclosed while attentionCounts and
// attentionTotal stay exact, and the status scan must never be paged.
func TestFactoryBriefingWarnsOnTruncatedAlertRows(t *testing.T) {
	rules := briefingRules()
	rules[1] = scriptRule{"local status_kind=", `{"tick":101,"surface":"nauvis","force":"player","scanned":2500,"total":2500,"alerts":[
		{"kind":"NO_POWER","severity":"CRITICAL","status":"no_power","name":"inserter","type":"inserter","position":{"x":1.5,"y":9.5},"unitNumber":11,"direction":0,"suggestedAction":"connect the entity to a powered electric network"}],
		"attentionCounts":[{"kind":"NO_POWER","count":2500}],"machines":[]}`}
	r, recorder := serveScriptedRCON(t, rules)
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.seen("local status_kind="); got != 1 {
		t.Fatalf("status scan read=%d, want one call with no paging", got)
	}
	warned := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "of 2500 unhealthy entities") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("missing alert truncation warning: %v", result.Warnings)
	}
	counts := map[AttentionKind]int{}
	for _, c := range result.AttentionCounts {
		counts[c.Kind] = c.Count
	}
	if counts[AttentionKindNoPower] != 2500 {
		t.Fatalf("counts must cover the whole scan: %+v", result.AttentionCounts)
	}
	// Every briefing row (one alert, one research, one damaged, one ghost, one
	// deconstruction) plus the hidden alert rows counted by attentionTotal.
	// Surface-wide enemies are named in warnings instead of becoming an
	// attention row.
	if len(result.Attention) != 5 || result.AttentionTotal != 5+2499 {
		t.Fatalf("attention=%d total=%d, want 5 rows and the 2499 hidden rows counted", len(result.Attention), result.AttentionTotal)
	}
}

// Watched items share one stock scan and one actor inventory read, instead of
// one full stock scan per item plus one inventory call per stockout.
func TestFactoryBriefingStockReadsOnce(t *testing.T) {
	rules := briefingRules()
	rules = append(rules, scriptRule{`subject.inventory="main"`, `{"tick":109,"surface":"nauvis","force":"player","player":"wricardo","character":"55","inventory":"main","items":[{"name":"iron-plate","count":7,"quality":"normal"}],"item_entries":1}`})
	r, recorder := serveScriptedRCON(t, rules)
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{
		Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20,
		Actor: &ActorInput{Player: new("wricardo")},
		WatchItems: []*BriefingWatchItemInput{
			{Item: "iron-plate", Minimum: 100},
			{Item: "copper-plate", Minimum: 100},
			{Item: "coal", Minimum: 50},
			{Item: "stone", Minimum: 50},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.seen("totals_by_key"); got != 1 {
		t.Fatalf("stock scans=%d, want exactly one for four watched items", got)
	}
	// A factory minimum must not be satisfied by items a player, a worker, the
	// ground or a belt happens to carry, so the default scope is STORED.
	if got := recorder.seen(stockScopeMarker("stored")); got != 1 {
		t.Fatalf("stock scans in the stored scope=%d, want exactly one", got)
	}
	if got := recorder.seen(`subject.inventory="main"`); got != 1 {
		t.Fatalf("actor inventory reads=%d, want exactly one for four stockouts", got)
	}
	if len(result.Stockouts) != 4 {
		t.Fatalf("stockouts: %+v", result.Stockouts)
	}
	for _, stockout := range result.Stockouts {
		if stockout.ActorHas == nil {
			t.Fatalf("actorHas must be filled for every stockout: %+v", stockout)
		}
		if stockout.Item == "iron-plate" && *stockout.ActorHas != 7 {
			t.Fatalf("iron-plate actorHas=%v, want the single inventory read", *stockout.ActorHas)
		}
		if stockout.Item != "iron-plate" && *stockout.ActorHas != 0 {
			t.Fatalf("%s actorHas=%v, want 0", stockout.Item, *stockout.ActorHas)
		}
	}
	enemyWarning := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "not classified as attention") {
			enemyWarning = true
		}
	}
	if len(result.Warnings) != 1 || !enemyWarning {
		t.Fatalf("warnings: %v", result.Warnings)
	}
}

// stockScopeMarker identifies the stock read that filters for one category.
func stockScopeMarker(category string) string {
	return "item, category_filter = " + luaString("nauvis") + ", " + luaString("player") + ", " + luaString("") + ", " + luaString(category)
}

// The stock scope must follow stockCategory, and ALL must restore the
// unfiltered read, so a caller can still audit every inventory kind.
func TestFactoryBriefingStockCategoryScope(t *testing.T) {
	cases := []struct {
		category StockCategory
		filter   string
	}{
		{StockCategoryStored, "stored"},
		{StockCategoryInTransit, "in-transit"},
		{StockCategoryPlayer, "player"},
		{StockCategoryAll, ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.category), func(t *testing.T) {
			r, recorder := serveScriptedRCON(t, briefingRules())
			_, err := factoryBriefingQuery(r, &FactoryBriefingInput{
				Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20,
				StockCategory: tc.category,
				WatchItems:    []*BriefingWatchItemInput{{Item: "iron-plate", Minimum: 100}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := recorder.seen(stockScopeMarker(tc.filter)); got != 1 {
				t.Fatalf("stock scans with category %q=%d, want exactly one", tc.filter, got)
			}
		})
	}
}

// An unknown category must fail before any read instead of silently watching
// every inventory.
func TestFactoryBriefingRejectsUnknownStockCategory(t *testing.T) {
	r, _ := serveScriptedRCON(t, briefingRules())
	_, err := factoryBriefingQuery(r, &FactoryBriefingInput{
		Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20,
		StockCategory: StockCategory("BOGUS"),
		WatchItems:    []*BriefingWatchItemInput{{Item: "iron-plate", Minimum: 100}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown stock category") {
		t.Fatalf("err=%v, want unknown stock category", err)
	}
}

// Enemies must never become surface-wide CRITICAL attention: unbounded, every
// hostile on the map would outrank the factory's real problems. Without an area
// the count is disclosed as a warning; with an area the same threats are
// classified, so the two reads agree on scope.
func TestFactoryBriefingEnemyAttentionRequiresArea(t *testing.T) {
	rules := briefingRules()
	for i := range rules {
		if rules[i].marker == "enemy_limit" {
			rules[i].response = strings.Replace(rules[i].response, `"total":1`, `"total":908`, 1)
		}
	}

	r, _ := serveScriptedRCON(t, rules)
	unbounded, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range unbounded.AttentionCounts {
		if count.Kind == AttentionKindEnemy {
			t.Fatalf("surface-wide enemies must not be counted as attention: %+v", unbounded.AttentionCounts)
		}
	}
	for _, item := range unbounded.Attention {
		if item.Kind == AttentionKindEnemy {
			t.Fatalf("surface-wide enemies must not become attention rows: %+v", item)
		}
	}
	warned := false
	for _, warning := range unbounded.Warnings {
		if strings.Contains(warning, "908 hostile entities") && strings.Contains(warning, "pass area") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the hidden enemy count must be named in warnings: %v", unbounded.Warnings)
	}
	// 908 hostiles must not turn into hundreds of attention items: this fixture
	// has four status faults, one research, one damaged entity and seven
	// deconstruction orders only.
	if unbounded.AttentionTotal > 20 {
		t.Fatalf("surface-wide enemies must not flood attention: total=%d counts=%+v", unbounded.AttentionTotal, unbounded.AttentionCounts)
	}
	// The same fixture through the other read: the filter is rejected rather than
	// accepted as a no-op, so the two ops agree that ENEMY is out of this scan.
	alertsSource, _ := serveScriptedRCON(t, rules)
	if _, err := factoryAlertsQuery(alertsSource, &FactoryAlertsInput{Surface: "nauvis", Force: "player", Limit: 10, Kinds: []AttentionKind{AttentionKindEnemy}}); err == nil || !strings.Contains(err.Error(), "only produces") {
		t.Fatalf("factoryAlerts must reject the ENEMY filter on the same fixture, got %v", err)
	}

	r, _ = serveScriptedRCON(t, rules)
	bounded, err := factoryBriefingQuery(r, &FactoryBriefingInput{
		Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20,
		Area: &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: 10, RightBottomY: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[AttentionKind]int{}
	for _, count := range bounded.AttentionCounts {
		counts[count.Kind] = count.Count
	}
	if counts[AttentionKindEnemy] != 908 {
		t.Fatalf("area-bounded enemy count: %+v", bounded.AttentionCounts)
	}
	if len(bounded.Attention) == 0 || bounded.Attention[0].Kind != AttentionKindEnemy || bounded.Attention[0].Severity != AttentionSeverityCritical {
		t.Fatalf("an area-bounded threat must lead the attention list: %+v", bounded.Attention)
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
		{"limit low", func(in *FactoryBriefingInput) { in.AttentionLimit = 0 }, "attentionLimit must be between 1 and 1000"},
		{"limit high", func(in *FactoryBriefingInput) { in.AttentionLimit = maxAttentionItems + 1 }, "attentionLimit must be between 1 and 1000"},
		{"bad window", func(in *FactoryBriefingInput) { in.Window = ProductionWindow("WEEK") }, "invalid production window"},
		{"huge area", func(in *FactoryBriefingInput) {
			in.Area = &FactoryAreaInput{RightBottomX: maxAreaTiles + 1, RightBottomY: 8}
		}, "no greater than 1024"},
		{"inverted area", func(in *FactoryBriefingInput) {
			in.Area = &FactoryAreaInput{LeftTopX: 5, RightBottomX: 1, RightBottomY: 8}
		}, "positive dimensions"},
		{"too many watch items", func(in *FactoryBriefingInput) {
			for i := 0; i < briefingMaxWatchItems+1; i++ {
				in.WatchItems = append(in.WatchItems, &BriefingWatchItemInput{Item: string(rune('a' + i)), Minimum: 1})
			}
		}, "at most 100 items"},
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

// A scope with no generating capacity must report satisfaction 0 instead of
// dividing by zero, while still exposing accumulator and low-power facts; the
// power read itself has to ask for the headroom fields.
func TestFactoryBriefingPowerWithoutCapacity(t *testing.T) {
	rules := briefingRules()
	rules[2] = scriptRule{"by_id, networks", `{"tick":102,"surface":"nauvis","force":"player","pageInfo":{"total":1,"limit":1000,"offset":0,"hasMore":false},"networks":[{"id":"1","polesInArea":1,"productionWatts":0,"consumptionWatts":600,"maxProductionWatts":0,"accumulatorChargeJoules":0,"accumulatorCapacityJoules":0,"lowPowerConsumers":3}]}`}
	r, recorder := serveScriptedRCON(t, rules)
	result, err := factoryBriefingQuery(r, &FactoryBriefingInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute, AttentionLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Power.Satisfaction != 0 || result.Power.CapacityWatts != 0 {
		t.Fatalf("no capacity must report satisfaction 0: %+v", result.Power)
	}
	if result.Power.ConsumptionWatts != 600 || result.Power.LowPowerConsumers != 3 {
		t.Fatalf("observed load and faults must survive aggregation: %+v", result.Power)
	}
	if got := recorder.seen("maxProductionWatts=capacity.capacity"); got != 1 {
		t.Fatalf("briefing power read must fetch capacity, ran %d times", got)
	}
	if got := recorder.seen("accumulatorChargeJoules=capacity.charge"); got != 1 {
		t.Fatalf("briefing power read must fetch accumulator charge, ran %d times", got)
	}
}
