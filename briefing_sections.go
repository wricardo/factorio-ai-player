package main

import (
	"fmt"
	"math"
	"sort"
)

// briefingMachines reads machine summaries and classifies unhealthy machines.
// Summaries are computed by the engine before paging, so only the per-machine
// rows are bounded; hidden machines are named in warnings.
func briefingMachines(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector, warnings *[]string) (*MachinePage, error) {
	var first *MachinePage
	scanned, total := 0, 0
	for page := 0; page < briefingMachinePages; page++ {
		result, err := factoryMachinesQuery(r, &MachinesInput{
			Surface: input.Surface, Force: input.Force, Area: input.Area,
			Limit: briefingMachinePage, Offset: page * briefingMachinePage,
		})
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = result
		}
		for _, machine := range result.Machines {
			scanned++
			kind, unhealthy := briefingMachineKind(machine.Status)
			if !unhealthy {
				continue
			}
			collector.counts[kind]++
			collector.add(&AttentionItem{
				Kind: kind, Entity: &machine.Name, Position: machine.Position, UnitNumber: machine.UnitNumber, Count: 1,
				Message: fmt.Sprintf("%s%s: %s", machine.Name, positionText(machine.Position), machine.Status),
			})
		}
		if result.PageInfo != nil {
			total = result.PageInfo.Total
			if !result.PageInfo.HasMore {
				break
			}
		}
	}
	if scanned < total {
		*warnings = append(*warnings, fmt.Sprintf("machine attention covers the first %d of %d machines; summaries cover all", scanned, total))
	}
	return first, nil
}

// briefingSafety adds enemy and damaged-entity observations and returns the
// tick of the safety read.
func briefingSafety(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector) (float64, error) {
	safety, err := safetyQuery(r, &SafetyInput{
		Surface: input.Surface, Force: input.Force, Area: input.Area,
		EnemyLimit: input.AttentionLimit, DamagedLimit: input.AttentionLimit, AlertLimit: 1,
	})
	if err != nil {
		return 0, err
	}
	if safety.Enemies != nil {
		if safety.Enemies.PageInfo != nil {
			collector.counts[AttentionKindEnemy] += safety.Enemies.PageInfo.Total
		}
		for _, entity := range safety.Enemies.Entities {
			collector.add(&AttentionItem{
				Kind: AttentionKindEnemy, Entity: &entity.Name, Position: entity.Position, UnitNumber: entity.UnitNumber, Count: 1,
				Message: fmt.Sprintf("%s%s", entity.Name, positionText(entity.Position)),
			})
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

// briefingConstruction counts ghosts (only when an area bounds the scan) and
// queued deconstruction and upgrade orders. It returns the later of the reads'
// ticks, so the section tick is never earlier than any of its data.
func briefingConstruction(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector, warnings *[]string) (*BriefingConstruction, float64, error) {
	construction := &BriefingConstruction{}
	var tick float64
	note := func(t float64) {
		if t > tick {
			tick = t
		}
	}

	if input.Area != nil {
		ghosts, err := ghostsQuery(r, &GhostsInput{Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: briefingGhostLimit})
		if err != nil {
			return nil, 0, err
		}
		note(ghosts.Tick)
		total, buildable := 0, 0
		if ghosts.PageInfo != nil {
			total = ghosts.PageInfo.Total
		}
		for _, entry := range ghosts.GhostEntries {
			if entry.Buildable {
				buildable++
			}
		}
		construction.Ghosts = &total
		construction.GhostsBuildable = &buildable
		if total > len(ghosts.GhostEntries) {
			*warnings = append(*warnings, fmt.Sprintf("ghostsBuildable covers the first %d of %d ghosts", len(ghosts.GhostEntries), total))
		}
		if total > 0 {
			collector.counts[AttentionKindGhostBacklog] += total
			collector.add(&AttentionItem{
				Kind: AttentionKindGhostBacklog, Count: total,
				Message: fmt.Sprintf("%d ghosts queued, %d with observed material", total, buildable),
			})
		}
	} else {
		*warnings = append(*warnings, "ghost backlog skipped: pass area to count ghosts")
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

// briefingStock reads factory stock for each watched item and reports the ones
// below their minimum. It returns a nil tick when nothing is watched.
func briefingStock(r *Resolver, input *FactoryBriefingInput, collector *briefingCollector) ([]*Stockout, *float64, error) {
	stockouts := []*Stockout{}
	if len(input.WatchItems) == 0 {
		return stockouts, nil, nil
	}
	var tick float64
	for _, watch := range input.WatchItems {
		item := watch.Item
		page, err := stockQuery(r, &StockInput{
			Surface: input.Surface, Force: input.Force, Area: input.Area, Item: &item,
			Limit: 1, TotalsLimit: 1, ItemsLimit: 100,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", item, err)
		}
		if page.Tick > tick {
			tick = page.Tick
		}
		have := 0.0
		for _, row := range page.Items {
			if row.Item == item {
				have += row.Count
			}
		}
		if have >= watch.Minimum {
			continue
		}
		stockout := &Stockout{Item: item, Minimum: watch.Minimum, Have: have, Shortfall: watch.Minimum - have}
		if input.Actor != nil {
			held, err := briefingActorCount(r, input.Actor, item)
			if err != nil {
				return nil, nil, fmt.Errorf("actor inventory: %w", err)
			}
			stockout.ActorHas = &held
		}
		stockouts = append(stockouts, stockout)
		collector.counts[AttentionKindLowStock]++
		collector.add(&AttentionItem{
			Kind: AttentionKindLowStock, Count: 1,
			Message: fmt.Sprintf("%s: %s of %s in observed stock", item, numberValue(&have), numberValue(&watch.Minimum)),
		})
	}
	sort.Slice(stockouts, func(i, j int) bool { return stockouts[i].Item < stockouts[j].Item })
	return stockouts, &tick, nil
}

func briefingActorCount(r *Resolver, actor *ActorInput, item string) (float64, error) {
	inventory, err := command[ActorInventory](r, "inventory", *actor)
	if err != nil {
		return 0, err
	}
	total := 0.0
	for _, stack := range inventory.Items {
		if stack.Name == item {
			total += stack.Count
		}
	}
	if math.IsNaN(total) {
		return 0, nil
	}
	return total, nil
}
