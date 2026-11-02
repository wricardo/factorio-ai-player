package main

import (
	"net"
	"strings"
	"testing"
)

// serveLuaSequence answers successive RCON exchanges with the queued responses
// and records each command, so a composing query like itemBalance can be tested
// end to end without a game.
func serveLuaSequence(t *testing.T, responses ...string) (*Resolver, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	commands := make(chan string, 64)
	go func() {
		index := 0
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if _, _, _, err = readPacket(conn); err != nil {
				conn.Close()
				continue
			}
			if err = writePacket(conn, 1, 2, ""); err != nil {
				conn.Close()
				continue
			}
			if _, _, body, err := readPacket(conn); err == nil {
				select {
				case commands <- body:
				default:
				}
			}
			response := ""
			if index < len(responses) {
				response = responses[index]
			}
			if index < len(responses) {
				index++
			}
			if err = writePacket(conn, 2, 0, response+"\n"); err != nil {
				conn.Close()
				continue
			}
			_ = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
			conn.Close()
		}
	}()
	return &Resolver{addr: listener.Addr().String(), password: "password"}, commands
}

// itemBalance must sum observed statistics and theoretical potentials while
// keeping them separate, and it must flag only consumers that are actually
// starving on the item.
func TestItemBalanceCombinesFlowStatisticsStockAndStarvedConsumers(t *testing.T) {
	flow := `{"tick":900,"force":"player","surface":"nauvis","item":"coal",
	  "producerPageInfo":{"total":1,"limit":1000,"offset":0,"hasMore":false},
	  "consumerPageInfo":{"total":3,"limit":1000,"offset":0,"hasMore":false},
	  "producers":[{"name":"burner-mining-drill","type":"mining-drill","surface":"nauvis","force":"player","position":{"x":1,"y":1},"status":"working","role":"RECIPE","materialType":"item","bufferedItems":[],"theoreticalRatePerSecond":0.75}],
	  "consumers":[
	    {"name":"stone-furnace","type":"furnace","surface":"nauvis","force":"player","position":{"x":2,"y":2},"status":"no_fuel","role":"FUEL","materialType":"item","bufferedItems":[],"theoreticalRatePerSecond":0.0375},
	    {"name":"boiler","type":"boiler","surface":"nauvis","force":"player","position":{"x":3,"y":3},"status":"working","role":"FUEL","materialType":"item","bufferedItems":[{"name":"coal","quality":"normal","count":5}],"theoreticalRatePerSecond":0.45},
	    {"name":"stone-furnace","type":"furnace","surface":"nauvis","force":"player","position":{"x":4,"y":4},"status":"no_ingredients","role":"RECIPE","materialType":"item","recipe":"iron-plate","bufferedItems":[],"theoreticalRatePerSecond":0.625}],
	  "rateAssumptions":"fixture"}`
	production := `{"tick":901,"surface":"nauvis","force":"player","window":"TEN_MINUTES","items":[{"item":"coal","produced":610.6,"consumed":190.5,"producedPerMinute":61.06,"consumedPerMinute":19.05}],"fluids":[]}`
	stock := `{"tick":902,"surface":"nauvis","force":"player",
	  "locationPageInfo":{"total":2,"limit":1,"offset":0,"hasMore":true},
	  "locations":[{"category":"stored","entityName":"wooden-chest","entityType":"container","position":{"x":1,"y":2},"inventoryKind":"chest","stack":{"name":"coal","quality":"normal","count":40}}],
	  "totalPageInfo":{"total":2,"limit":1000,"offset":0,"hasMore":false},
	  "totals":[{"item":"coal","quality":"normal","category":"stored","count":40},{"item":"coal","quality":"normal","category":"in-transit","count":10}],
	  "itemPageInfo":{"total":1,"limit":1,"offset":0,"hasMore":false},
	  "items":[{"item":"coal","quality":"normal","count":50}]}`
	r, commands := serveLuaSequence(t, flow, production, stock)
	balance, err := itemBalanceQuery(r, ItemBalanceInput{Surface: "nauvis", Force: "player", Item: "coal", Window: ProductionWindowTenMinutes})
	if err != nil {
		t.Fatal(err)
	}
	if balance.ProducedPerMinute != 61.06 || balance.ConsumedPerMinute != 19.05 || !nearlyEqual(balance.Net, 42.01) {
		t.Fatalf("observed rates: %+v", balance)
	}
	if !nearlyEqual(balance.TheoreticalSupply, 45) || !nearlyEqual(balance.TheoreticalDemand, 66.75) {
		t.Fatalf("theoretical sums: supply %v demand %v", balance.TheoreticalSupply, balance.TheoreticalDemand)
	}
	if balance.Stock.Total != 50 || len(balance.Stock.ByCategory) != 2 ||
		balance.Stock.ByCategory[0].Category != "in-transit" || balance.Stock.ByCategory[1].Category != "stored" ||
		balance.Stock.ByCategory[1].Count != 40 {
		t.Fatalf("stock: %+v", balance.Stock)
	}
	if balance.Truncated {
		t.Fatal("a complete itemFlow scan must not report truncation")
	}
	if len(balance.StarvedConsumers) != 2 {
		t.Fatalf("starved consumers: %+v", balance.StarvedConsumers)
	}
	itemFlowCall := <-commands
	productionCall := <-commands
	stockCall := <-commands
	if !strings.Contains(itemFlowCall, `local item,area,limit,offset=`) || !strings.Contains(itemFlowCall, `,nil,10000,0`) {
		t.Fatalf("itemFlow scan must use the 1000-row page:\n%s", itemFlowCall)
	}
	if !strings.Contains(productionCall, `local window=`) || !strings.Contains(productionCall, "get_fluid_production_statistics") {
		t.Fatalf("production read missing window or fluid statistics:\n%s", productionCall)
	}
	if !strings.Contains(stockCall, `local surface_name, force_name, item, category_filter = `) ||
		!strings.Contains(stockCall, `record("worker"`) {
		t.Fatalf("stock read must filter the item and count workers:\n%s", stockCall)
	}
}

// A truncated itemFlow page must surface as truncated instead of looking like a
// complete consumer list.
func TestItemBalanceReportsTruncatedScan(t *testing.T) {
	flow := `{"tick":1,"force":"player","surface":"nauvis","item":"coal",
	  "producerPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},
	  "consumerPageInfo":{"total":1200,"limit":1000,"offset":0,"hasMore":true},
	  "producers":[],"consumers":[],"rateAssumptions":"x"}`
	production := `{"tick":2,"surface":"nauvis","force":"player","window":"ONE_MINUTE","items":[],"fluids":[]}`
	stock := `{"tick":3,"surface":"nauvis","force":"player","locationPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"locations":[],"totalPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},"totals":[],"itemPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"items":[]}`
	r, _ := serveLuaSequence(t, flow, production, stock)
	balance, err := itemBalanceQuery(r, ItemBalanceInput{Surface: "nauvis", Force: "player", Item: "coal", Window: ProductionWindowOneMinute})
	if err != nil {
		t.Fatal(err)
	}
	if !balance.Truncated || balance.Stock.Total != 0 || len(balance.Stock.ByCategory) != 0 || len(balance.StarvedConsumers) != 0 {
		t.Fatalf("truncated scan: %+v", balance)
	}
}

// An observed flow row for a fluid still fills the observed rates.
func TestItemBalanceUsesFluidFlowRows(t *testing.T) {
	flow := `{"tick":1,"force":"player","surface":"nauvis","item":"steam","producerPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},"consumerPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},"producers":[],"consumers":[],"rateAssumptions":"x"}`
	production := `{"tick":2,"surface":"nauvis","force":"player","window":"ONE_MINUTE","items":[],"fluids":[{"item":"steam","produced":120,"consumed":120,"producedPerMinute":120,"consumedPerMinute":115}]}`
	stock := `{"tick":3,"surface":"nauvis","force":"player","locationPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"locations":[],"totalPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},"totals":[],"itemPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"items":[]}`
	r, _ := serveLuaSequence(t, flow, production, stock)
	balance, err := itemBalanceQuery(r, ItemBalanceInput{Surface: "nauvis", Force: "player", Item: "steam", Window: ProductionWindowOneMinute})
	if err != nil {
		t.Fatal(err)
	}
	if balance.ProducedPerMinute != 120 || balance.ConsumedPerMinute != 115 || balance.Net != 5 {
		t.Fatalf("fluid rates: %+v", balance)
	}
}

// Invalid framing must fail before any RCON dial.
func TestItemBalanceValidation(t *testing.T) {
	r := &Resolver{addr: "127.0.0.1:1", password: "password"}
	for name, input := range map[string]ItemBalanceInput{
		"missing item": {Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute},
		"empty force":  {Surface: "nauvis", Item: "coal", Window: ProductionWindowOneMinute},
		"bad window":   {Surface: "nauvis", Force: "player", Item: "coal", Window: ProductionWindow("FIVE_MINUTES")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := itemBalanceQuery(r, input); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

// A consumer is starved only when it is short of the requested item: a burner
// with none of the fuel buffered or an engine no_fuel status, and a recipe
// consumer reporting missing ingredients or fuel. Faults that are unrelated to
// the item must never surface as starvation.
func TestItemBalanceStarvationClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint *ItemFlowEndpoint
		starved  bool
	}{
		{"missing fuel status", &ItemFlowEndpoint{Status: "no_fuel", Role: ItemFlowRoleFuel}, true},
		{"ingredient shortage", &ItemFlowEndpoint{Status: "item_ingredient_shortage", Role: ItemFlowRoleRecipe}, true},
		{"empty burner inventory", &ItemFlowEndpoint{Status: "working", Role: ItemFlowRoleFuel}, true},
		{"fuelled burner", &ItemFlowEndpoint{Status: "working", Role: ItemFlowRoleFuel, BufferedItems: []*InventoryStack{{Name: "coal", Count: 4}}}, false},
		{"depleted drill holding fuel", &ItemFlowEndpoint{Status: "no_minable_resources", Role: ItemFlowRoleFuel, BufferedItems: []*InventoryStack{{Name: "coal", Count: 5}}}, false},
		{"depleted drill without fuel", &ItemFlowEndpoint{Status: "no_minable_resources", Role: ItemFlowRoleFuel}, true},
		{"full output with fuel", &ItemFlowEndpoint{Status: "full_output", Role: ItemFlowRoleFuel, BufferedItems: []*InventoryStack{{Name: "coal", Count: 2}}}, false},
		{"recipe consumer with unrelated power fault", &ItemFlowEndpoint{Status: "no_power", Role: ItemFlowRoleRecipe, BufferedItems: []*InventoryStack{{Name: "coal", Count: 2}}}, false},
		{"working recipe consumer", &ItemFlowEndpoint{Status: "working", Role: ItemFlowRoleRecipe}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := itemBalanceStarved(tc.endpoint); got != tc.starved {
				t.Fatalf("starved = %v, want %v", got, tc.starved)
			}
		})
	}
}

func TestProductionItemRowPrefersItemsThenFluids(t *testing.T) {
	production := &FactoryProduction{Items: []*ProductionItem{{Item: "coal", ProducedPerMinute: 3}}, Fluids: []*ProductionItem{{Item: "steam", ProducedPerMinute: 4}}}
	if row := productionItemRow(production, "coal"); row == nil || row.ProducedPerMinute != 3 {
		t.Fatalf("item row: %+v", row)
	}
	if row := productionItemRow(production, "steam"); row == nil || row.ProducedPerMinute != 4 {
		t.Fatalf("fluid row: %+v", row)
	}
	if row := productionItemRow(production, "iron-ore"); row != nil {
		t.Fatalf("unknown row: %+v", row)
	}
	if row := productionItemRow(nil, "coal"); row != nil {
		t.Fatalf("nil production: %+v", row)
	}
}

// Depleted drills that still hold coal must not appear as starved coal
// consumers; only the drill whose fuel inventory is empty belongs there.
func TestItemBalanceExcludesDepletedDrillsThatHoldFuel(t *testing.T) {
	flow := `{"tick":10,"force":"player","surface":"nauvis","item":"coal",
	  "producerPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},
	  "consumerPageInfo":{"total":3,"limit":1000,"offset":0,"hasMore":false},
	  "producers":[],
	  "consumers":[
	    {"name":"burner-mining-drill","type":"mining-drill","surface":"nauvis","force":"player","position":{"x":1,"y":1},"status":"no_minable_resources","role":"FUEL","materialType":"item","bufferedItems":[{"name":"coal","quality":"normal","count":4}],"theoreticalRatePerSecond":0.0375},
	    {"name":"burner-mining-drill","type":"mining-drill","surface":"nauvis","force":"player","position":{"x":2,"y":2},"status":"no_minable_resources","role":"FUEL","materialType":"item","bufferedItems":[],"theoreticalRatePerSecond":0.0375},
	    {"name":"burner-mining-drill","type":"mining-drill","surface":"nauvis","force":"player","position":{"x":3,"y":3},"status":"working","role":"FUEL","materialType":"item","bufferedItems":[{"name":"coal","quality":"normal","count":9}],"theoreticalRatePerSecond":0.0375}],
	  "rateAssumptions":"fixture"}`
	production := `{"tick":11,"surface":"nauvis","force":"player","window":"ONE_MINUTE","items":[{"item":"coal","produced":23,"consumed":22,"producedPerMinute":23,"consumedPerMinute":22}],"fluids":[]}`
	stock := `{"tick":12,"surface":"nauvis","force":"player","locationPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"locations":[],"totalPageInfo":{"total":0,"limit":1000,"offset":0,"hasMore":false},"totals":[],"itemPageInfo":{"total":0,"limit":1,"offset":0,"hasMore":false},"items":[]}`
	r, _ := serveLuaSequence(t, flow, production, stock)
	balance, err := itemBalanceQuery(r, ItemBalanceInput{Surface: "nauvis", Force: "player", Item: "coal", Window: ProductionWindowOneMinute})
	if err != nil {
		t.Fatal(err)
	}
	if len(balance.StarvedConsumers) != 1 {
		t.Fatalf("starved consumers: %+v", balance.StarvedConsumers)
	}
	starved := balance.StarvedConsumers[0]
	if starved.Position == nil || starved.Position.X != 2 || len(starved.BufferedItems) != 0 {
		t.Fatalf("the unfuelled drill must be the only starved consumer: %+v", starved)
	}
}
