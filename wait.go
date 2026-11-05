package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// maxWaitSeconds bounds every wall-clock wait exposed by the API so a
	// single call can never block an agent for minutes; the game runs at a
	// multiple of real time, so 10 seconds covers long waits.
	maxWaitSeconds = 10
	// maxWaitDuration is maxWaitSeconds as a time.Duration.
	maxWaitDuration           = maxWaitSeconds * time.Second
	waitDefaultTimeoutSeconds = float64(maxWaitSeconds)
	waitDefaultPollSeconds    = 0.25
	waitMaxTimeoutSeconds     = float64(maxWaitSeconds)
	waitMinPollSeconds        = 0.05
	waitMaxPollSeconds        = 5.0
	waitMaxStatuses           = 32
)

// clampWaitSeconds caps a requested wall-clock wait in whole seconds at
// maxWaitSeconds. Callers still reject non-positive values.
func clampWaitSeconds(seconds int) time.Duration {
	if seconds > maxWaitSeconds {
		seconds = maxWaitSeconds
	}
	return time.Duration(seconds) * time.Second
}

// waitObservation is one poll result decoded from the backend Lua.
type waitObservation struct {
	Tick       float64  `json:"tick"`
	Satisfied  bool     `json:"satisfied"`
	Count      *float64 `json:"count,omitempty"`
	Status     *string  `json:"status,omitempty"`
	Researched *bool    `json:"researched,omitempty"`
	QueueSize  *float64 `json:"queueSize,omitempty"`
	Detail     string   `json:"detail"`
}

// waitUntil polls one condition from the backend until it holds or the
// wall-clock timeout expires. Each poll is a separate RCON call, which keeps
// the game thread and other agents free; the game keeps simulating between
// polls.
func waitUntil(ctx context.Context, r *Resolver, input WaitUntilInput) (*WaitResult, error) {
	timeout := input.TimeoutSeconds
	if timeout == 0 {
		timeout = waitDefaultTimeoutSeconds
	}
	if math.IsNaN(timeout) || math.IsInf(timeout, 0) || timeout < 1 {
		return nil, fmt.Errorf("timeoutSeconds must be at least 1")
	}
	if timeout > waitMaxTimeoutSeconds {
		timeout = waitMaxTimeoutSeconds
	}
	poll := input.PollSeconds
	if poll == 0 {
		poll = waitDefaultPollSeconds
	}
	if math.IsNaN(poll) || math.IsInf(poll, 0) || poll < waitMinPollSeconds || poll > waitMaxPollSeconds {
		return nil, fmt.Errorf("pollSeconds must be between %g and %g", waitMinPollSeconds, waitMaxPollSeconds)
	}
	kind, lua, err := waitPollLua(ctx, r, input.Condition)
	if err != nil {
		return nil, err
	}
	pollInterval := time.Duration(poll * float64(time.Second))
	start := time.Now()
	deadline := start.Add(time.Duration(timeout * float64(time.Second)))
	polls := 0
	for {
		observation, err := executeWaitPoll(ctx, r, lua)
		if err != nil {
			return nil, err
		}
		polls++
		if observation.Satisfied {
			return waitResult(kind, observation, false, start, polls), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return waitResult(kind, observation, true, start, polls), nil
		}
		delay := pollInterval
		if remaining < delay {
			delay = remaining
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// waitTickBaselineLua reads the current game tick so a ticks condition can
// become an absolute tick target.
const waitTickBaselineLua = luaFail + `local tick=game.tick
rcon.print(helpers.table_to_json{tick=tick,detail="tick baseline "..tick})`

// waitPollLua validates the selected condition and builds its one-poll Lua
// script. A ticks condition costs one extra engine read first: the baseline
// tick turns "wait N ticks" into an absolute target so the wait follows game
// speed instead of wall-clock time, while the caller's timeout still bounds it.
func waitPollLua(ctx context.Context, r *Resolver, condition *WaitConditionInput) (WaitConditionKind, string, error) {
	if condition == nil || condition.Ticks == nil {
		return waitConditionLua(condition)
	}
	if waitConditionFieldCount(condition) != 1 {
		return "", "", errors.New("condition must set exactly one field")
	}
	ticks := *condition.Ticks
	if math.IsNaN(ticks) || math.IsInf(ticks, 0) || ticks <= 0 {
		return "", "", errors.New("ticks must be finite and greater than 0")
	}
	baseline, err := executeWaitPoll(ctx, r, waitTickBaselineLua)
	if err != nil {
		return "", "", err
	}
	target := waitTickTarget(baseline.Tick, ticks)
	return waitTicksLua(baseline.Tick, target, ticks)
}

// waitTicksLua builds the one-poll script for an already resolved ticks
// condition; count reports elapsed ticks capped at the requested amount.
func waitTicksLua(baseline, target, ticks float64) (WaitConditionKind, string, error) {
	return WaitConditionKindTicks, luaFail + fmt.Sprintf(`local baseline=%s; local target=%s; local want=%s
local tick=game.tick
local elapsed=tick-baseline
if elapsed<0 then elapsed=0 end
local shown=elapsed; if shown>want then shown=want end
rcon.print(helpers.table_to_json{tick=tick,satisfied=tick>=target,count=shown,detail="waited "..shown.." of "..want.." ticks"})`,
		luaCoordinate(baseline), luaCoordinate(target), luaCoordinate(ticks)), nil
}

// waitTickTarget is the absolute tick a ticks condition waits for, rounding up
// so a fractional request never returns a tick early.
func waitTickTarget(baseline, ticks float64) float64 {
	return math.Ceil(baseline + ticks)
}

func executeWaitPoll(ctx context.Context, r *Resolver, lua string) (*waitObservation, error) {
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[waitObservation](response)
}

func waitResult(kind WaitConditionKind, observation *waitObservation, timedOut bool, start time.Time, polls int) *WaitResult {
	return &WaitResult{
		Tick:          observation.Tick,
		Condition:     kind,
		Satisfied:     observation.Satisfied,
		TimedOut:      timedOut,
		WaitedSeconds: time.Since(start).Seconds(),
		Polls:         polls,
		Last: &WaitObservation{
			Tick:       observation.Tick,
			Count:      observation.Count,
			Status:     observation.Status,
			Researched: observation.Researched,
			QueueSize:  observation.QueueSize,
			Detail:     observation.Detail,
		},
	}
}

// waitConditionFieldCount counts how many condition fields the input sets, so
// exactly-one validation is shared by the resolver and the Lua builder.
func waitConditionFieldCount(condition *WaitConditionInput) int {
	if condition == nil {
		return 0
	}
	selected := 0
	for _, present := range []bool{
		condition.Ticks != nil,
		condition.Inventory != nil,
		condition.CraftQueueEmpty != nil,
		condition.MachineStatus != nil,
		condition.Research != nil,
		condition.Fluid != nil,
		condition.Production != nil,
	} {
		if present {
			selected++
		}
	}
	return selected
}

// waitConditionLua validates the selected condition and builds its one-poll Lua
// script.
func waitConditionLua(condition *WaitConditionInput) (WaitConditionKind, string, error) {
	if condition == nil {
		return "", "", errors.New("condition is required")
	}
	if waitConditionFieldCount(condition) != 1 {
		return "", "", errors.New("condition must set exactly one field")
	}
	switch {
	case condition.Ticks != nil:
		// waitPollLua resolves this into an absolute tick before polling.
		return "", "", errors.New("ticks condition must be resolved before building its Lua")
	case condition.Inventory != nil:
		cond := condition.Inventory
		if cond.Actor == nil {
			return "", "", errors.New("inventory actor is required")
		}
		character, player, err := settingsActorFrom(cond.Actor)
		if err != nil {
			return "", "", err
		}
		if cond.Item == "" {
			return "", "", errors.New("inventory item must not be empty")
		}
		quality, err := resolveQuality(&cond.Quality)
		if err != nil {
			return "", "", err
		}
		if math.IsNaN(cond.Count) || math.IsInf(cond.Count, 0) || cond.Count <= 0 {
			return "", "", errors.New("inventory count must be finite and greater than 0")
		}
		return WaitConditionKindInventory, luaFail + waitActorLua(character, player) + fmt.Sprintf(`
local item=%s; local quality=%s; local target=%s
if not prototypes.item[item] then fail("item prototype not found"); return end
if not prototypes.quality[quality] then fail("quality not found"); return end
local inv=c.get_main_inventory(); if not inv then fail("actor has no main inventory"); return end
local have=inv.get_item_count{name=item,quality=quality}
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=have>=target,count=have,detail=item.." "..have.." of "..target})`,
			luaString(cond.Item), luaString(quality), luaCoordinate(cond.Count)), nil
	case condition.CraftQueueEmpty != nil:
		character, player, err := settingsActorFrom(condition.CraftQueueEmpty)
		if err != nil {
			return "", "", err
		}
		return WaitConditionKindCraftQueueEmpty, luaFail + waitActorLua(character, player) + `
local size=0
if c.crafting_queue then size=#c.crafting_queue end
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=size==0,queueSize=size,detail="crafting queue "..size})`, nil
	case condition.MachineStatus != nil:
		cond := condition.MachineStatus
		unit, name, x, y, surface, err := entityRefTarget(cond.Target)
		if err != nil {
			return "", "", err
		}
		if len(cond.Statuses) == 0 || len(cond.Statuses) > waitMaxStatuses {
			return "", "", fmt.Errorf("statuses must contain between 1 and %d names", waitMaxStatuses)
		}
		allowed := make([]string, 0, len(cond.Statuses))
		for i, status := range cond.Statuses {
			status = strings.TrimSpace(status)
			if status == "" {
				return "", "", fmt.Errorf("status %d must not be empty", i+1)
			}
			allowed = append(allowed, "["+luaString(status)+"]=true")
		}
		return WaitConditionKindMachineStatus, luaFail + entityRefPrelude("target", unit, name, x, y, surface) + fmt.Sprintf(`
local allowed={%s}
local statuses={}; for k,v in pairs(defines.entity_status) do statuses[v]=k end
local ok,raw=pcall(function() return target.status end)
if not ok then fail("target has no status"); return end
local status_name=statuses[raw] or ""
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=allowed[status_name]==true,status=status_name,detail="status "..status_name})`,
			strings.Join(allowed, ",")), nil
	case condition.Research != nil:
		cond := condition.Research
		if cond.Force == "" {
			return "", "", errors.New("research force must not be empty")
		}
		if cond.Technology == "" {
			return "", "", errors.New("research technology must not be empty")
		}
		return WaitConditionKindResearch, luaFail + fmt.Sprintf(`
local force=game.forces[%s]; if not force then fail("force not found"); return end
local technology=%s
local tech=force.technologies[technology]; if not tech then fail("technology not found: "..technology); return end
local researched=tech.researched
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=researched,researched=researched,detail=technology.." researched="..tostring(researched)})`,
			luaString(cond.Force), luaString(cond.Technology)), nil
	case condition.Fluid != nil:
		cond := condition.Fluid
		unit, name, x, y, surface, err := entityRefTarget(cond.Target)
		if err != nil {
			return "", "", err
		}
		if math.IsNaN(cond.AtLeast) || math.IsInf(cond.AtLeast, 0) || cond.AtLeast < 0 {
			return "", "", errors.New("fluid atLeast must be finite and non-negative")
		}
		return WaitConditionKindFluid, luaFail + entityRefPrelude("target", unit, name, x, y, surface) + fmt.Sprintf(`
local wanted=%s; local target_amount=%s
local contents=target.get_fluid_contents()
local total=0
for fluid_name,amount in pairs(contents) do if wanted=="" or fluid_name==wanted then total=total+amount end end
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=total>=target_amount,count=total,detail="fluid "..total.." of "..target_amount})`,
			luaString(stringValueOr(cond.Fluid, "")), luaCoordinate(cond.AtLeast)), nil
	case condition.Production != nil:
		cond := condition.Production
		if cond.Surface == "" || cond.Force == "" || cond.Item == "" {
			return "", "", errors.New("production surface, force and item must not be empty")
		}
		if math.IsNaN(cond.Count) || math.IsInf(cond.Count, 0) || cond.Count <= 0 {
			return "", "", errors.New("production count must be finite and greater than 0")
		}
		precision, err := waitPrecision(cond.Window)
		if err != nil {
			return "", "", err
		}
		return WaitConditionKindProduction, luaFail + fmt.Sprintf(`
local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local force=game.forces[%s]; if not force then fail("force not found"); return end
local item=%s; local target=%s
local stats=force.get_item_production_statistics(surface)
local count=stats.get_flow_count{name=item,category="input",precision_index=defines.flow_precision_index.%s,count=true}
rcon.print(helpers.table_to_json{tick=game.tick,satisfied=count>=target,count=count,detail=item.." "..count.." of "..target})`,
			luaString(cond.Surface), luaString(cond.Force), luaString(cond.Item), luaCoordinate(cond.Count), precision), nil
	}
	return "", "", errors.New("condition must set exactly one field")
}

func waitPrecision(window ProductionWindow) (string, error) {
	switch window {
	case ProductionWindowOneMinute, "":
		return "one_minute", nil
	case ProductionWindowTenMinutes:
		return "ten_minutes", nil
	case ProductionWindowOneHour:
		return "one_hour", nil
	}
	return "", fmt.Errorf("unsupported production window %q", window)
}

// waitActorLua resolves the acting character and binds `c` for one poll.
func waitActorLua(character uint64, player string) string {
	return fmt.Sprintf(`local player=%s; local unit=%d; local actor; local c
if unit~=0 then actor=game.get_entity_by_unit_number(unit); if not actor or actor.type~="character" then fail("character not found"); return end; c=actor
else actor=game.get_player(player); if not actor then fail("player not found"); return end; c=actor.character; if not c then fail("player has no character"); return end end
`, luaString(player), character)
}

// waitEntityTarget validates an entity reference and returns its lookup fields.
func entityRefTarget(target *EntityRefInput) (uint64, string, float64, float64, string, error) {
	if target == nil {
		return 0, "", 0, 0, "", errors.New("target is required")
	}
	surface := stringValueOr(target.Surface, "nauvis")
	if surface == "" {
		surface = "nauvis"
	}
	name := stringValueOr(target.Entity, "")
	unitNumber := stringValueOr(target.UnitNumber, "")
	x, y := entityRefCoordinates(target)
	unit, err := entityTarget(name, unitNumber, x, y)
	if err != nil {
		return 0, "", 0, 0, "", err
	}
	if math.IsNaN(x) {
		x = 0
	}
	if math.IsNaN(y) {
		y = 0
	}
	return unit, name, x, y, surface, nil
}

// entityRefPrelude resolves one entity reference by unit number or by
// prototype and center position, binding `<label>`, `<label>_surface` and
// `<label>_position`. The label keeps two references apart in one script, so a
// pairwise transfer can resolve a source and a destination without name
// collisions.
func entityRefPrelude(label string, unit uint64, name string, x, y float64, surface string) string {
	return fmt.Sprintf(`local %[1]s_surface_name=%[2]s
local %[1]s_surface=game.surfaces[%[1]s_surface_name]; if not %[1]s_surface then fail("%[1]s surface not found"); return end
local %[1]s_unit=%[3]d; local %[1]s_name=%[4]s; local %[1]s_position={x=%[5]s,y=%[6]s}
local %[1]s=nil
if %[1]s_unit~=0 then
  %[1]s=game.get_entity_by_unit_number(%[1]s_unit)
  if not %[1]s and %[1]s_name~="" then
    for _,candidate in pairs(%[1]s_surface.find_entities_filtered{name=%[1]s_name}) do
      if candidate.unit_number==%[1]s_unit then %[1]s=candidate; break end
    end
  end
else
  if %[1]s_name=="" then fail("%[1]s entity name or unitNumber is required"); return end
  for _,candidate in pairs(%[1]s_surface.find_entities_filtered{name=%[1]s_name,position=%[1]s_position,radius=0.01}) do
    if math.abs(candidate.position.x-%[1]s_position.x)<0.001 and math.abs(candidate.position.y-%[1]s_position.y)<0.001 then %[1]s=candidate; break end
  end
end
if not %[1]s or not %[1]s.valid then
  if %[1]s_unit~=0 and %[1]s_name=="" then fail("entity not found by unit number; pass entity too"); return end
  fail("%[1]s entity not found"); return
end
`, label, luaString(surface), unit, luaString(name), luaCoordinate(x), luaCoordinate(y))
}
