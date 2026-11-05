package main

import (
	"fmt"
	"sort"
)

// briefingAlerts reads the one-call status scan of every status-bearing force
// entity (crafting machines, boilers, generators, inserters, turrets, reactors,
// radars, drills, and so on), materializing the unhealthy rows and copying the
// machine summaries. Counts come from the full scan, so attentionTotal stays
// exact even when the row list is capped.
func briefingAlerts(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector, warnings *[]string) (*FactoryAlerts, error) {
	alerts, err := factoryAlertsQuery(r, &FactoryAlertsInput{
		Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: briefingAlertRows,
	})
	if err != nil {
		return nil, err
	}
	for _, count := range alerts.AttentionCounts {
		collector.counts[count.Kind] += count.Count
	}
	for _, alert := range alerts.Alerts {
		name := alert.Name
		collector.add(&AttentionItem{
			Kind: alert.Kind, Entity: &name, Position: alert.Position, UnitNumber: alert.UnitNumber, Count: 1,
			Message: fmt.Sprintf("%s%s: %s (%s)", alert.Name, positionText(alert.Position), alert.Status, alert.SuggestedAction),
		})
	}
	if hidden := alerts.Total - len(alerts.Alerts); hidden > 0 {
		collector.extra += hidden
		*warnings = append(*warnings, fmt.Sprintf("factory attention lists the first %d of %d unhealthy entities; attentionCounts covers all", len(alerts.Alerts), alerts.Total))
	}
	return alerts, nil
}

// briefingSafety adds enemy and damaged-entity observations and returns the
// tick of the safety read. Enemies are classified as attention only inside an
// explicit area: unbounded, the safety scan walks every hostile on the surface,
// so its total would mark the whole map CRITICAL attention and outrank every
// factory-local problem. Without an area the count is disclosed as a warning.
func briefingSafety(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector, warnings *[]string) (float64, error) {
	enemyLimit := input.AttentionLimit
	if input.Area == nil {
		enemyLimit = 1
	}
	safety, err := safetyQuery(r, &SafetyInput{
		Surface: input.Surface, Force: input.Force, Area: input.Area,
		EnemyLimit: enemyLimit, DamagedLimit: input.AttentionLimit, AlertLimit: 1,
	})
	if err != nil {
		return 0, err
	}
	if safety.Enemies != nil {
		total := 0
		if safety.Enemies.PageInfo != nil {
			total = safety.Enemies.PageInfo.Total
		}
		if input.Area == nil {
			if total > 0 {
				*warnings = append(*warnings, fmt.Sprintf("%d hostile entities on %s are not classified as attention; pass area to report enemy threats", total, input.Surface))
			}
		} else {
			collector.counts[AttentionKindEnemy] += total
			for _, entity := range safety.Enemies.Entities {
				collector.add(&AttentionItem{
					Kind: AttentionKindEnemy, Entity: &entity.Name, Position: entity.Position, UnitNumber: entity.UnitNumber, Count: 1,
					Message: fmt.Sprintf("%s%s", entity.Name, positionText(entity.Position)),
				})
			}
		}
	}
	if safety.Damaged != nil {
		if safety.Damaged.PageInfo != nil {
			collector.counts[AttentionKindDamaged] += safety.Damaged.PageInfo.Total
		}
		for _, entity := range safety.Damaged.Entities {
			message := fmt.Sprintf("%s%s", entity.Name, positionText(entity.Position))
			if entity.Health != nil && entity.MaxHealth != nil {
				message += fmt.Sprintf(": health %s of %s", numberValue(entity.Health), numberValue(entity.MaxHealth))
			}
			collector.add(&AttentionItem{
				Kind: AttentionKindDamaged, Entity: &entity.Name, Position: entity.Position, UnitNumber: entity.UnitNumber, Count: 1,
				Message: message,
			})
		}
	}
	return safety.Tick, nil
}

// briefingConstruction counts ghosts and queued deconstruction and upgrade
// orders. The ghost read needs no area: an omitted briefing area scans the whole
// surface, so the backlog is never skipped. It returns the later of the reads'
// ticks, so the section tick is never earlier than any of its data.
func briefingConstruction(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector, warnings *[]string) (*BriefingConstruction, float64, error) {
	construction := &BriefingConstruction{}
	var tick float64
	note := func(t float64) {
		if t > tick {
			tick = t
		}
	}

	ghosts, err := ghostsQuery(r, &GhostsInput{Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: briefingGhostLimit})
	if err != nil {
		return nil, 0, err
	}
	note(ghosts.Tick)
	total, buildable, blocked := 0, 0, 0
	if ghosts.PageInfo != nil {
		total = ghosts.PageInfo.Total
	}
	for _, entry := range ghosts.GhostEntries {
		if entry.Buildable {
			buildable++
		}
		if entry.Blocked {
			blocked++
		}
	}
	construction.Ghosts = &total
	construction.GhostsBuildable = &buildable
	if total > len(ghosts.GhostEntries) {
		*warnings = append(*warnings, fmt.Sprintf("ghostsBuildable covers the first %d of %d ghosts", len(ghosts.GhostEntries), total))
	}
	if total > 0 {
		collector.counts[AttentionKindGhostBacklog] += total
		message := fmt.Sprintf("%d ghosts queued, %d with observed material", total, buildable)
		if blocked > 0 {
			message = fmt.Sprintf("%d ghosts queued, %d with observed material, %d blocked by colliding entities", total, buildable, blocked)
		}
		collector.add(&AttentionItem{
			Kind: AttentionKindGhostBacklog, Count: total,
			Message: message,
		})
	}

	for _, state := range []ConstructionOrderState{ConstructionOrderStateDeconstruction, ConstructionOrderStateUpgrade} {
		page, err := constructionOrders(r, &ConstructionOrdersInput{Surface: input.Surface, Area: input.Area, State: state, Limit: 1})
		if err != nil {
			return nil, 0, err
		}
		note(page.Tick)
		total := 0
		if page.PageInfo != nil {
			total = page.PageInfo.Total
		}
		if state == ConstructionOrderStateDeconstruction {
			construction.DeconstructionOrders = total
			if total > 0 {
				collector.counts[AttentionKindDeconstruction] += total
				collector.add(&AttentionItem{
					Kind: AttentionKindDeconstruction, Count: total,
					Message: fmt.Sprintf("%d entities marked for deconstruction", total),
				})
			}
		} else {
			construction.UpgradeOrders = total
		}
	}
	return construction, tick, nil
}

// briefingStock reads factory stock once for every watched item in the
// configured category and reports the ones below their minimum. The actor
// inventory is also read once, not once per stockout. It returns a nil tick
// when nothing is watched.
func briefingStock(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector) ([]*Stockout, *float64, error) {
	stockouts := []*Stockout{}
	if len(input.WatchItems) == 0 {
		return stockouts, nil, nil
	}
	category := input.StockCategory
	page, err := stockQuery(r, &StockInput{
		Surface: input.Surface, Force: input.Force, Area: input.Area, Category: &category,
		Limit: 1, TotalsLimit: 1, ItemsLimit: briefingStockItems,
	})
	if err != nil {
		return nil, nil, err
	}
	have := map[string]float64{}
	for _, row := range page.Items {
		have[row.Item] += row.Count
	}
	var held map[string]float64
	if input.Actor != nil {
		held, err = briefingActorInventory(r, input.Actor)
		if err != nil {
			return nil, nil, fmt.Errorf("actor inventory: %w", err)
		}
	}
	for _, watch := range input.WatchItems {
		item := watch.Item
		count := have[item]
		if count >= watch.Minimum {
			continue
		}
		stockout := &Stockout{Item: item, Minimum: watch.Minimum, Have: count, Shortfall: watch.Minimum - count}
		if held != nil {
			actorHas := held[item]
			stockout.ActorHas = &actorHas
		}
		stockouts = append(stockouts, stockout)
		collector.counts[AttentionKindLowStock]++
		collector.add(&AttentionItem{
			Kind: AttentionKindLowStock, Count: 1,
			Message: fmt.Sprintf("%s: %s of %s in observed stock", item, numberValue(&count), numberValue(&watch.Minimum)),
		})
	}
	tick := page.Tick
	sort.Slice(stockouts, func(i, j int) bool { return stockouts[i].Item < stockouts[j].Item })
	return stockouts, &tick, nil
}

// briefingActorInventory reads the actor's main inventory once and indexes it by
// item name, so every stockout shares one RCON read.
func briefingActorInventory(r *Resolver, actor *ActorInput) (map[string]float64, error) {
	inventory, err := command[ActorInventory](r, "inventory", *actor)
	if err != nil {
		return nil, err
	}
	counts := map[string]float64{}
	for _, stack := range inventory.Items {
		counts[stack.Name] += stack.Count
	}
	return counts, nil
}
