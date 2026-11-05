package main

import "fmt"

// Tooling limits bound one GraphQL call so a single request cannot make the
// game thread or the RCON transport unresponsive. They are deliberately
// generous and are not gameplay rules: any request the engine can complete
// should pass. Game-semantic ranges (entity directions 0-15, inserter
// priority, color components, train limits, stack sizes and inventory sizes)
// stay at their engine limits.
const (
	// maxAreaTiles is the largest side of a bounded area scan or write.
	maxAreaTiles = 1024

	// maxPageSize is the largest page any paginated read returns.
	maxPageSize = 10000

	// maxBatchTargets is the largest explicit entity-target list in one call.
	maxBatchTargets = 1000

	// maxPlacements is the largest explicit position list in one call.
	maxPlacements = 10000

	// maxBlueprintEntities and maxBlueprintTiles bound one blueprint export,
	// import or ghost plan.
	maxBlueprintEntities = 10000
	maxBlueprintTiles    = 20000

	// maxBlueprintCharacters bounds an imported blueprint string.
	maxBlueprintCharacters = 2_000_000

	// The blueprint catalog is persisted in script storage, so its bounds keep
	// one save's catalog from growing without limit. maxBlueprintBookEntries is
	// far below the engine's 65535 book slots because every catalog call imports
	// and re-exports the whole book.
	maxBlueprintBooks              = 200
	maxBlueprintBookEntries        = 1000
	maxBlueprintCatalogCharacters  = 16_000_000
	maxBlueprintBookNameCharacters = 64

	// maxAttentionItems bounds one factoryBriefing attention list.
	maxAttentionItems = 1000

	// maxWatchItems bounds the stockout watch list of one briefing.
	maxWatchItems = 100

	// maxBriefingRows bounds each internally paginated briefing section.
	maxBriefingRows = 10000

	// maxBeltTraversal bounds one belt component or stretch walk.
	maxBeltTraversal = 20000

	// maxBeltComponents bounds how many components one belt audit returns.
	maxBeltComponents = 1000

	// maxCircuitScan and maxCircuitMembers bound circuitNetwork reads.
	maxCircuitScan    = 50000
	maxCircuitMembers = 10000

	// maxFluidBoxes bounds a fluid-segment scan and a single inspection read.
	maxFluidBoxes           = 20000
	maxInspectionFluidBoxes = 50000

	// maxProbeSamples bounds a placement probe's sampled tile count and the
	// number of sites it may report.
	maxProbeSamples = 65536
	maxProbeSites   = 1000

	// maxMapDimension bounds mapArea output rows and columns.
	maxMapDimension = 256

	// maxNeighborRadius and maxDrillRadius are tile radii, not entity counts.
	maxNeighborRadius = 1000
	maxDrillRadius    = 1000

	// maxTrainScheduleRecords bounds one setTrainSchedule call.
	maxTrainScheduleRecords = 1000

	// maxControlConditions bounds combinator conditions and outputs.
	maxControlConditions = 1000

	// maxPlanExpansions and maxPlanRecipes bound one production plan; the
	// catalogue and entity scans it drives are capped in its Lua at 50000.
	maxPlanExpansions = 100000
	maxPlanRecipes    = 20000
)

// limitError reports a bounded numeric field in a uniform way.
func limitError(field string, max int) error {
	return fmt.Errorf("%s must be between 1 and %d", field, max)
}

// pageLimitError reports a pagination bound.
func pageLimitError(field string) error {
	return limitError(field, maxPageSize)
}

// areaLimitError reports the shared area-side bound.
func areaLimitError() error {
	return fmt.Errorf("area must have positive dimensions no greater than %d tiles", maxAreaTiles)
}
