package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// defectAreaLimit matches the other read-only area scans: the joins are done in
// Go over one snapshot per read, not per entity, so a whole base fits.
const defectAreaLimit = 1024

var defectKindOrder = []DefectKind{
	DefectKindBoilerNoFuelInserter,
	DefectKindMachineNoInput,
	DefectKindBeltMixedLane,
	DefectKindProducerUnplugged,
	DefectKindInserterDanglingPickup,
	DefectKindInserterDanglingDrop,
	DefectKindInserterSharedSource,
	DefectKindBeltDead,
	DefectKindBeltStarveHead,
	DefectKindBeltStallEnd,
	DefectKindOrphanEntity,
	DefectKindGhostNoRoboport,
}

var defectSeverity = map[DefectKind]DefectSeverity{
	DefectKindBoilerNoFuelInserter:   DefectSeverityCritical,
	DefectKindMachineNoInput:         DefectSeverityWarning,
	DefectKindBeltMixedLane:          DefectSeverityCritical,
	DefectKindProducerUnplugged:      DefectSeverityCritical,
	DefectKindInserterDanglingPickup: DefectSeverityWarning,
	DefectKindInserterDanglingDrop:   DefectSeverityWarning,
	DefectKindInserterSharedSource:   DefectSeverityWarning,
	DefectKindBeltDead:               DefectSeverityWarning,
	DefectKindBeltStarveHead:         DefectSeverityInfo,
	DefectKindBeltStallEnd:           DefectSeverityInfo,
	DefectKindOrphanEntity:           DefectSeverityInfo,
	DefectKindGhostNoRoboport:        DefectSeverityInfo,
}

var defectKindRank = func() map[DefectKind]int {
	ranks := make(map[DefectKind]int, len(defectKindOrder))
	for i, kind := range defectKindOrder {
		ranks[kind] = i
	}
	return ranks
}()

var defectSeverityRank = map[DefectSeverity]int{
	DefectSeverityCritical: 0,
	DefectSeverityWarning:  1,
	DefectSeverityInfo:     2,
}

// machineNeedsInput lists the entity types that cannot run without an inserter
// dropping into them. Steam engines, drills, belts and pipes deliberately stay
// out: their input or output path is not an inserter.
var machineNeedsInput = map[string]bool{
	"assembling-machine": true,
	"furnace":            true,
	"lab":                true,
	"chemical-plant":     true,
	"oil-refinery":       true,
	"rocket-silo":        true,
}

// powerProducers are the engine types whose meaningful connectivity check is a
// network id, not the consumer-side connectedToProducingNetwork predicate.
var powerProducers = map[string]bool{
	"generator":                 true,
	"solar-panel":               true,
	"fusion-generator":          true,
	"burner-generator":          true,
	"electric-energy-interface": true,
}

func factoryDefectsQuery(r *Resolver, input FactoryDefectsInput) (*FactoryDefects, error) {
	if input.Surface == "" || input.Force == "" {
		return nil, errors.New("surface and force are required")
	}
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	if input.Limit < 1 || input.Limit > maxPageSize {
		return nil, pageLimitError("limit")
	}
	want := make(map[DefectKind]bool, len(defectKindOrder))
	if len(input.Kinds) == 0 {
		for _, kind := range defectKindOrder {
			want[kind] = true
		}
	} else {
		for _, kind := range input.Kinds {
			want[kind] = true
		}
	}
	needsBelt := want[DefectKindBeltDead] || want[DefectKindBeltStarveHead] ||
		want[DefectKindBeltStallEnd] || want[DefectKindBeltMixedLane]
	needsPower := want[DefectKindProducerUnplugged]
	needsGhost := want[DefectKindGhostNoRoboport]

	out := &FactoryDefects{
		Surface: input.Surface,
		Force:   input.Force,
		Limit:   input.Limit,
	}
	warnings := []string{}

	graph, err := factoryGraphQuery(r, input.Area, input.Force, input.Surface, true, false)
	if err != nil {
		return nil, fmt.Errorf("factoryGraph: %w", err)
	}
	out.GraphTick = graph.Tick
	out.Area = areaToWorld(input.Area)
	out.ScannedNodes = len(graph.Nodes)
	out.ScannedInserters = len(graph.CompactInserters)

	defects := graphDefects(input, want, graph)

	if needsBelt {
		belts, err := beltComponentsQuery(r, BeltComponentsInput{
			Surface: input.Surface, Force: input.Force, Area: input.Area,
			MaxEntities: 5000, Limit: maxBeltComponents, Offset: 0,
		})
		if err != nil {
			return nil, fmt.Errorf("beltComponents: %w", err)
		}
		out.BeltTick = &belts.Tick
		out.ScannedBelts = belts.BeltTotal
		defects = append(defects, beltDefects(input, want, belts)...)
		if belts.PageInfo != nil && belts.PageInfo.HasMore {
			warnings = append(warnings, fmt.Sprintf("belt components cover %d of %d", len(belts.Components), belts.PageInfo.Total))
		}
	}

	if needsPower {
		power, err := powerConsumersQuery(r, &PowerInsightsInput{
			Surface: input.Surface, Force: input.Force, Area: input.Area,
			Limit: maxPageSize, OnlyIssues: true,
		})
		if err != nil {
			return nil, fmt.Errorf("powerConsumers: %w", err)
		}
		out.PowerTick = &power.Tick
		defects = append(defects, powerDefects(input, want, power)...)
		if power.PageInfo != nil && power.PageInfo.HasMore {
			warnings = append(warnings, fmt.Sprintf("power issues cover %d of %d entities", len(power.PowerEntities), power.PageInfo.Total))
		}
	}

	if needsGhost {
		ghosts, err := ghostsQuery(r, &GhostsInput{Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: 1})
		if err != nil {
			return nil, fmt.Errorf("ghosts: %w", err)
		}
		networks, err := logisticNetworks(r, &LogisticNetworksInput{
			Surface: input.Surface, Force: input.Force, Area: input.Area, Limit: 1,
		})
		if err != nil {
			return nil, fmt.Errorf("logisticNetworks: %w", err)
		}
		out.GhostTick = &networks.Tick
		defects = append(defects, ghostDefect(input, want, ghosts, networks)...)
	}

	sort.SliceStable(defects, func(i, j int) bool {
		a, b := defects[i], defects[j]
		if defectSeverityRank[a.Severity] != defectSeverityRank[b.Severity] {
			return defectSeverityRank[a.Severity] < defectSeverityRank[b.Severity]
		}
		if a.Kind != b.Kind {
			return defectKindRank[a.Kind] < defectKindRank[b.Kind]
		}
		if a.Position.Y != b.Position.Y {
			return a.Position.Y < b.Position.Y
		}
		if a.Position.X != b.Position.X {
			return a.Position.X < b.Position.X
		}
		return a.Entity < b.Entity
	})

	out.Total = len(defects)
	counts := map[DefectKind]int{}
	for _, defect := range defects {
		counts[defect.Kind]++
	}
	for _, kind := range defectKindOrder {
		if n := counts[kind]; n > 0 {
			out.Counts = append(out.Counts, &FactoryDefectCount{Kind: kind, Count: n})
		}
	}
	if len(defects) > input.Limit {
		defects = defects[:input.Limit]
	}
	out.Defects = defects
	if out.Defects == nil {
		out.Defects = []*FactoryDefect{}
	}
	if out.Counts == nil {
		out.Counts = []*FactoryDefectCount{}
	}
	out.Warnings = warnings
	return out, nil
}

// graphDefects joins resolved inserter endpoints against node types. Every rule
// is per type: engines, drills, belts and pipes never require an input arm.
func graphDefects(input FactoryDefectsInput, want map[DefectKind]bool, graph *FactoryGraph) []*FactoryDefect {
	var defects []*FactoryDefect
	add := func(kind DefectKind, entity string, entityType *string, unitNumber *string, position *Position, detail string) {
		if !want[kind] || position == nil {
			return
		}
		defects = append(defects, &FactoryDefect{
			Kind: kind, Severity: defectSeverity[kind], Entity: entity, Type: entityType,
			UnitNumber: unitNumber, Position: position, Detail: detail,
		})
	}
	droppedOn := map[string][]*CompactInserter{}
	pickedFrom := map[string][]*CompactInserter{}
	inserterKeys := map[string]bool{}
	for _, arm := range graph.CompactInserters {
		inserterKeys[arm.Inserter] = true
		if arm.Drop != nil {
			droppedOn[*arm.Drop] = append(droppedOn[*arm.Drop], arm)
		}
		if arm.Pickup != nil {
			pickedFrom[*arm.Pickup] = append(pickedFrom[*arm.Pickup], arm)
		}
	}
	degree := map[string]int{}
	for _, edge := range graph.CompactEdges {
		degree[edge.From]++
		degree[edge.To]++
	}
	nodesByKey := map[string]*LogisticsEndpoint{}
	for _, node := range graph.Nodes {
		if node.UnitNumber != nil {
			nodesByKey[input.Surface+":"+*node.UnitNumber] = node
		}
	}
	for _, node := range graph.Nodes {
		if node.UnitNumber == nil || node.Position == nil {
			continue
		}
		key := input.Surface + ":" + *node.UnitNumber
		switch {
		case node.Type == "boiler":
			if len(droppedOn[key]) == 0 {
				add(DefectKindBoilerNoFuelInserter, node.Name, &node.Type, node.UnitNumber, node.Position,
					"no inserter drops into this boiler, so it can never take fuel")
			}
		case machineNeedsInput[node.Type]:
			if len(droppedOn[key]) == 0 {
				add(DefectKindMachineNoInput, node.Name, &node.Type, node.UnitNumber, node.Position,
					"no inserter drops into this machine")
			}
		}
		if len(pickedFrom[key]) >= 2 {
			names := make([]string, 0, len(pickedFrom[key]))
			for _, arm := range pickedFrom[key] {
				names = append(names, strings.TrimPrefix(arm.Inserter, input.Surface+":"))
			}
			sort.Strings(names)
			add(DefectKindInserterSharedSource, node.Name, &node.Type, node.UnitNumber, node.Position,
				fmt.Sprintf("%d inserters pick from this entity: %s", len(names), strings.Join(names, ", ")))
		}
		if degree[key] == 0 && !inserterKeys[key] {
			add(DefectKindOrphanEntity, node.Name, &node.Type, node.UnitNumber, node.Position,
				"no engine edge and no inserter touches this entity")
		}
	}
	for _, arm := range graph.CompactInserters {
		inserter, ok := nodesByKey[arm.Inserter]
		if !ok || inserter.Position == nil {
			continue
		}
		if arm.Pickup == nil {
			add(DefectKindInserterDanglingPickup, inserter.Name, &inserter.Type, inserter.UnitNumber, inserter.Position,
				"the pickup side resolves to nothing insertable")
		}
		if arm.Drop == nil {
			add(DefectKindInserterDanglingDrop, inserter.Name, &inserter.Type, inserter.UnitNumber, inserter.Position,
				"the drop side resolves to nothing insertable")
		}
	}
	return defects
}

// beltDefects turns one belt component snapshot into component-level rows.
func beltDefects(input FactoryDefectsInput, want map[DefectKind]bool, page *BeltComponentPage) []*FactoryDefect {
	var defects []*FactoryDefect
	componentType := "transport-belt"
	add := func(kind DefectKind, entity string, entityType *string, unitNumber *string, position *Position, detail string) {
		if !want[kind] || position == nil {
			return
		}
		defects = append(defects, &FactoryDefect{
			Kind: kind, Severity: defectSeverity[kind], Entity: entity, Type: entityType,
			UnitNumber: unitNumber, Position: position, Detail: detail,
		})
	}
	for _, component := range page.Components {
		anchor := componentAnchor(component)
		if component.Dead {
			add(DefectKindBeltDead, "belt-component-"+component.ID, &componentType, nil, anchor,
				fmt.Sprintf("%d belts, %d in-transit items, nothing feeds or drains the component", component.Belts, component.ItemCount))
		}
		if component.Mixed {
			add(DefectKindBeltMixedLane, "belt-component-"+component.ID, &componentType, nil, anchor,
				"at least one belt carries more than one item or quality on one side")
		}
		for _, end := range component.StarveHeads {
			add(DefectKindBeltStarveHead, end.Name, &end.Type, end.UnitNumber, end.Position,
				"component source that nothing drops onto")
		}
		for _, end := range component.StallEnds {
			add(DefectKindBeltStallEnd, end.Name, &end.Type, end.UnitNumber, end.Position,
				"component sink that nothing picks from")
		}
	}
	return defects
}

// powerDefects reports producers without a network id. The consumer-side
// connectedToProducingNetwork predicate is meaningless here.
func powerDefects(input FactoryDefectsInput, want map[DefectKind]bool, page *PowerEntityPage) []*FactoryDefect {
	var defects []*FactoryDefect
	if !want[DefectKindProducerUnplugged] {
		return defects
	}
	for _, entity := range page.PowerEntities {
		if !powerProducers[entity.Type] || entity.NetworkID != nil || entity.Position == nil {
			continue
		}
		entityType := entity.Type
		defects = append(defects, &FactoryDefect{
			Kind: DefectKindProducerUnplugged, Severity: defectSeverity[DefectKindProducerUnplugged],
			Entity: entity.Name, Type: &entityType, UnitNumber: entity.UnitNumber, Position: entity.Position,
			Detail: "generator reports no electric network id, so its output reaches no grid",
		})
	}
	return defects
}

// ghostDefect reports the aggregate case only: a backlog with no network to
// build it. Ghost counts are whole-area totals, so one row is the honest shape.
func ghostDefect(input FactoryDefectsInput, want map[DefectKind]bool, ghosts *GhostsPage, networks *LogisticNetworkPage) []*FactoryDefect {
	if !want[DefectKindGhostNoRoboport] {
		return nil
	}
	if ghosts.PageInfo == nil || ghosts.PageInfo.Total == 0 || networks.PageInfo == nil || networks.PageInfo.Total != 0 {
		return nil
	}
	entityType := "entity-ghost"
	return []*FactoryDefect{{
		Kind:     DefectKindGhostNoRoboport,
		Severity: defectSeverity[DefectKindGhostNoRoboport],
		Entity:   "ghost-backlog",
		Type:     &entityType,
		Position: areaCenter(input.Area),
		Detail: fmt.Sprintf("%d ghosts in the area and no logistic network covers it, so no robot can build them",
			ghosts.PageInfo.Total),
	}}
}

func componentAnchor(component *BeltComponent) *Position {
	for _, node := range component.Nodes {
		if node.Position != nil {
			return node.Position
		}
	}
	return nil
}

func areaCenter(area *FactoryAreaInput) *Position {
	return &Position{X: (area.LeftTopX + area.RightBottomX) / 2, Y: (area.LeftTopY + area.RightBottomY) / 2}
}

func areaToWorld(area *FactoryAreaInput) *WorldArea {
	if area == nil {
		return nil
	}
	if math.IsNaN(area.LeftTopX) || math.IsNaN(area.LeftTopY) || math.IsNaN(area.RightBottomX) || math.IsNaN(area.RightBottomY) {
		return nil
	}
	return &WorldArea{LeftTopX: area.LeftTopX, LeftTopY: area.LeftTopY, RightBottomX: area.RightBottomX, RightBottomY: area.RightBottomY}
}
