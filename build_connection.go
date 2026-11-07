package main

import "errors"

// buildConnection plans one connection and then builds it in a single call.
//
// It is a thin orchestrator over the two existing halves: connectionPlanQuery
// reads the route without touching the world, and connectEntities applies it.
// No Lua is added here, so the plan and the build stay exactly as
// connectionPlan and connectEntities behave on their own.
//
// Nothing is transactional. A plan with conflicts places nothing and returns
// the typed conflicts. A possible plan is applied afterwards, and per-tile
// failures during that second read are reported in the placement result, so a
// caller must re-read the world instead of assuming a rollback.
func buildConnection(r *Resolver, input ConnectEntitiesInput, dryRun bool) (*BuildConnectionResult, error) {
	if input.Surface == "" {
		return nil, errors.New("surface must not be empty")
	}
	if input.Force == "" {
		return nil, errors.New("force must not be empty")
	}
	plan, err := connectionPlanQuery(r, connectionPlanInputFrom(input))
	if err != nil {
		return nil, err
	}
	built := &BuildConnectionResult{
		Tick:    plan.Tick,
		Surface: plan.Surface,
		Force:   plan.Force,
		DryRun:  dryRun,
		Plan:    plan,
	}
	if dryRun || !plan.Possible {
		return built, nil
	}
	result, err := connectEntities(r, input)
	if err != nil {
		return nil, err
	}
	built.Tick = result.Tick
	built.Result = result
	return built, nil
}

// connectionPlanInputFrom maps the mutation input onto the read-only planner
// input. The actor and the message are placement-only, and a single-tile run has
// no plan to produce, which the planner reports itself.
func connectionPlanInputFrom(in ConnectEntitiesInput) *ConnectionPlanInput {
	return &ConnectionPlanInput{
		Surface:                in.Surface,
		Force:                  in.Force,
		From:                   in.From,
		To:                     in.To,
		Kind:                   in.Kind,
		Item:                   in.Item,
		Bend:                   in.Bend,
		AllowFluidAdjacency:    in.AllowFluidAdjacency,
		Routing:                in.Routing,
		AllowUnderground:       in.AllowUnderground,
		UndergroundItem:        in.UndergroundItem,
		MaxUndergroundDistance: in.MaxUndergroundDistance,
		AvoidAreas:             in.AvoidAreas,
		PreferExisting:         in.PreferExisting,
		MaxExpandedTiles:       in.MaxExpandedTiles,
		SearchMargin:           in.SearchMargin,
		FromEntity:             in.FromEntity,
		ToEntity:               in.ToEntity,
		AllowBeltAdjacency:     in.AllowBeltAdjacency,
	}
}
