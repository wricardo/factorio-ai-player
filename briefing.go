package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	briefingMaxAttention  = 100
	briefingMaxWatchItems = 20
	briefingMachinePage   = 1000
	briefingMachinePages  = 3
	briefingNetworkLimit  = 1000
	briefingGhostLimit    = 1000
)

// briefingSeverity classifies a kind. It reports how bad an observation is, not
// what to do about it.
var briefingSeverity = map[AttentionKind]AttentionSeverity{
	AttentionKindNoFuel:         AttentionSeverityCritical,
	AttentionKindNoPower:        AttentionSeverityCritical,
	AttentionKindEnemy:          AttentionSeverityCritical,
	AttentionKindLowStock:       AttentionSeverityCritical,
	AttentionKindFluidShortage:  AttentionSeverityWarning,
	AttentionKindNoIngredients:  AttentionSeverityWarning,
	AttentionKindOutputFull:     AttentionSeverityWarning,
	AttentionKindNoRecipe:       AttentionSeverityWarning,
	AttentionKindDamaged:        AttentionSeverityWarning,
	AttentionKindNoResearch:     AttentionSeverityInfo,
	AttentionKindGhostBacklog:   AttentionSeverityInfo,
	AttentionKindDeconstruction: AttentionSeverityInfo,
}

var briefingSeverityRank = map[AttentionSeverity]int{
	AttentionSeverityCritical: 0,
	AttentionSeverityWarning:  1,
	AttentionSeverityInfo:     2,
}

// briefingKindOrder keeps ties deterministic.
var briefingKindOrder = []AttentionKind{
	AttentionKindEnemy, AttentionKindNoPower, AttentionKindNoFuel, AttentionKindLowStock,
	AttentionKindFluidShortage, AttentionKindNoIngredients, AttentionKindOutputFull, AttentionKindNoRecipe, AttentionKindDamaged,
	AttentionKindNoResearch, AttentionKindGhostBacklog, AttentionKindDeconstruction,
}

// briefingMachineKind maps an engine machine status to an attention kind.
func briefingMachineKind(status string) (AttentionKind, bool) {
	switch status {
	case "no_fuel":
		return AttentionKindNoFuel, true
	case "no_power", "low_power", "not_plugged_in_electric_network":
		return AttentionKindNoPower, true
	case "no_ingredients", "item_ingredient_shortage", "missing_science_packs":
		return AttentionKindNoIngredients, true
	case "fluid_ingredient_shortage", "no_input_fluid", "missing_required_fluid":
		return AttentionKindFluidShortage, true
	case "full_output", "full_burnt_result_output", "waiting_for_space_in_destination":
		return AttentionKindOutputFull, true
	case "no_recipe":
		return AttentionKindNoRecipe, true
	}
	return "", false
}

type briefingCollector struct {
	items  []*AttentionItem
	counts map[AttentionKind]int
}

func newBriefingCollector() *briefingCollector {
	return &briefingCollector{counts: map[AttentionKind]int{}}
}

func (c *briefingCollector) add(item *AttentionItem) {
	item.Severity = briefingSeverity[item.Kind]
	c.items = append(c.items, item)
}

// sorted orders by severity, kind, position and name so repeated briefings are
// byte-identical for the same game state.
func (c *briefingCollector) sorted() []*AttentionItem {
	kindRank := map[AttentionKind]int{}
	for i, kind := range briefingKindOrder {
		kindRank[kind] = i
	}
	out := append([]*AttentionItem(nil), c.items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := briefingSeverityRank[a.Severity], briefingSeverityRank[b.Severity]; ra != rb {
			return ra < rb
		}
		if ka, kb := kindRank[a.Kind], kindRank[b.Kind]; ka != kb {
			return ka < kb
		}
		ay, by := math.Inf(1), math.Inf(1)
		ax, bx := math.Inf(1), math.Inf(1)
		if a.Position != nil {
			ax, ay = a.Position.X, a.Position.Y
		}
		if b.Position != nil {
			bx, by = b.Position.X, b.Position.Y
		}
		if ay != by {
			return ay < by
		}
		if ax != bx {
			return ax < bx
		}
		return stringValue(a.Entity) < stringValue(b.Entity)
	})
	return out
}

func (c *briefingCollector) countList() []*AttentionCount {
	out := make([]*AttentionCount, 0, len(c.counts))
	for _, kind := range briefingKindOrder {
		if n := c.counts[kind]; n > 0 {
			out = append(out, &AttentionCount{Kind: kind, Count: n})
		}
	}
	return out
}

// briefingInput validates the request and applies defaults.
func briefingInput(input *FactoryBriefingInput) (*FactoryBriefingInput, error) {
	if input == nil {
		return nil, errors.New("input is required")
	}
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.AttentionLimit < 1 || input.AttentionLimit > briefingMaxAttention {
		return nil, fmt.Errorf("attentionLimit must be between 1 and %d", briefingMaxAttention)
	}
	switch input.Window {
	case ProductionWindowOneMinute, ProductionWindowTenMinutes, ProductionWindowOneHour:
	default:
		return nil, fmt.Errorf("invalid production window %q", input.Window)
	}
	if input.Area != nil {
		if _, _, err := worldAreaBoundsLua(input.Area, 256); err != nil {
			return nil, err
		}
	}
	if len(input.WatchItems) > briefingMaxWatchItems {
		return nil, fmt.Errorf("watchItems must contain at most %d items", briefingMaxWatchItems)
	}
	seen := map[string]bool{}
	for i, watch := range input.WatchItems {
		if watch == nil {
			return nil, fmt.Errorf("watch item %d is missing", i+1)
		}
		if watch.Item == "" {
			return nil, fmt.Errorf("watch item %d must name an item", i+1)
		}
		if seen[watch.Item] {
			return nil, fmt.Errorf("watch item %q is duplicated", watch.Item)
		}
		seen[watch.Item] = true
		if math.IsNaN(watch.Minimum) || math.IsInf(watch.Minimum, 0) || watch.Minimum <= 0 {
			return nil, fmt.Errorf("watch item %q minimum must be finite and greater than 0", watch.Item)
		}
	}
	if input.Actor != nil {
		if _, _, err := settingsActorFrom(input.Actor); err != nil {
			return nil, err
		}
	}
	return input, nil
}

func briefingScanArea(area *FactoryAreaInput) *FactoryArea {
	if area == nil {
		return nil
	}
	return &FactoryArea{LeftTopX: area.LeftTopX, LeftTopY: area.LeftTopY, RightBottomX: area.RightBottomX, RightBottomY: area.RightBottomY}
}

// factoryBriefingQuery composes existing domain reads into one attention
// snapshot. Each read is its own RCON call, so each section carries its own tick.
func factoryBriefingQuery(r *Resolver, input *FactoryBriefingInput) (*FactoryBriefing, error) {
	input, err := briefingInput(input)
	if err != nil {
		return nil, err
	}
	out := &FactoryBriefing{Surface: input.Surface, Force: input.Force, Window: input.Window}
	collector := newBriefingCollector()
	warnings := []string{}

	production, err := factoryProductionQuery(r, &FactoryOverviewInput{Surface: input.Surface, Force: input.Force, Window: input.Window})
	if err != nil {
		return nil, fmt.Errorf("production: %w", err)
	}
	out.ProductionTick = production.Tick
	out.Production = production.Items

	machines, err := briefingMachines(r, input, collector, &warnings)
	if err != nil {
		return nil, fmt.Errorf("machines: %w", err)
	}
	out.MachineTick = machines.Tick
	out.Machines = machines.Summaries

	power, err := electricNetworksQuery(r, &PowerInsightsInput{Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: briefingNetworkLimit})
	if err != nil {
		return nil, fmt.Errorf("power: %w", err)
	}
	out.PowerTick = power.Tick
	out.Power = &BriefingPower{}
	for _, network := range power.Networks {
		out.Power.Networks++
		out.Power.ProductionWatts += network.ProductionWatts
		out.Power.ConsumptionWatts += network.ConsumptionWatts
	}
	if power.PageInfo != nil && power.PageInfo.HasMore {
		out.Power.Networks = power.PageInfo.Total
		warnings = append(warnings, fmt.Sprintf("power sums cover the first %d of %d networks", len(power.Networks), power.PageInfo.Total))
	}

	research, err := researchInsightsQuery(r, &ResearchInsightsInput{Force: input.Force, Status: ResearchInsightsStatusInProgress, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("research: %w", err)
	}
	out.ResearchTick = research.Tick
	out.Research = &BriefingResearch{
		Enabled:           research.ResearchEnabled,
		CurrentTechnology: research.CurrentTechnology,
		Progress:          research.Progress,
		Queue:             research.ResearchQueue,
	}
	if research.ResearchEnabled && research.CurrentTechnology == nil && len(research.ResearchQueue) == 0 {
		collector.counts[AttentionKindNoResearch]++
		collector.add(&AttentionItem{Kind: AttentionKindNoResearch, Count: 1, Message: "no research in progress and none queued"})
	}

	safetyTick, err := briefingSafety(r, input, collector)
	if err != nil {
		return nil, fmt.Errorf("safety: %w", err)
	}
	out.SafetyTick = safetyTick

	construction, constructionTick, err := briefingConstruction(r, input, collector, &warnings)
	if err != nil {
		return nil, fmt.Errorf("construction: %w", err)
	}
	out.Construction = construction
	out.ConstructionTick = constructionTick

	stockouts, stockTick, err := briefingStock(r, input, collector)
	if err != nil {
		return nil, fmt.Errorf("stock: %w", err)
	}
	out.Stockouts = stockouts
	out.StockTick = stockTick

	all := collector.sorted()
	out.AttentionTotal = len(all)
	out.AttentionCounts = collector.countList()
	if len(all) > input.AttentionLimit {
		all = all[:input.AttentionLimit]
	}
	out.Attention = all
	out.Warnings = warnings
	return out, nil
}
