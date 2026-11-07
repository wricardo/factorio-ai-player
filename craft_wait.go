package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type craftCommandInput struct {
	Player    *string  `json:"player,omitempty"`
	Character *string  `json:"character,omitempty"`
	Recipe    string   `json:"recipe"`
	Count     *float64 `json:"count,omitempty"`
	Mode      string   `json:"mode,omitempty"`
}

func craftMutation(ctx context.Context, r *Resolver, input CraftInput) (*ActionResult, error) {
	if input.WaitTimeoutSeconds < 1 {
		return nil, errors.New("waitTimeoutSeconds must be at least 1")
	}
	if input.Expand {
		return craftExpand(ctx, r, input)
	}
	player, character := actorFields(input.Actor)
	result, err := commandQuiet[ActionResult](r, "craft", craftCommandInput{
		Player:    player,
		Character: character,
		Recipe:    input.Recipe,
		Count:     input.Count,
		Mode:      craftModeValue(input.Mode),
	})
	if err != nil {
		return nil, explainCraftRefusal(ctx, r, input, err)
	}
	if !input.Wait {
		announceMutation(r, input.Message, mutationSummary("craft", result))
		return result, nil
	}
	if result.QueueSize == nil {
		return nil, errors.New("craft response omitted queue size")
	}
	queueSize, completed, waited, err := waitForCraftQueue(ctx, *result.QueueSize, clampWaitSeconds(input.WaitTimeoutSeconds), func(ctx context.Context) (float64, error) {
		return craftQueueSize(ctx, r, input.Actor)
	})
	if err != nil {
		return nil, err
	}
	result.QueueSize = &queueSize
	result.CraftingComplete = &completed
	timedOut := !completed
	result.WaitTimedOut = &timedOut
	waitedSeconds := waited.Seconds()
	result.WaitedSeconds = &waitedSeconds
	announceMutation(r, input.Message, mutationSummary("craft", result))
	return result, nil
}

func waitForCraftQueue(ctx context.Context, queueSize float64, timeout time.Duration, status func(context.Context) (float64, error)) (float64, bool, time.Duration, error) {
	started := time.Now()
	if queueSize <= 0 {
		return 0, true, 0, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for queueSize > 0 {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return queueSize, false, time.Since(started), fmt.Errorf("crafting may still be active in queue: %w", ctx.Err())
			}
			return queueSize, false, time.Since(started), nil
		case <-ticker.C:
			latest, err := status(waitCtx)
			if err != nil {
				if ctx.Err() != nil {
					return queueSize, false, time.Since(started), fmt.Errorf("crafting may still be active in queue: %w", ctx.Err())
				}
				if waitCtx.Err() != nil {
					return queueSize, false, time.Since(started), nil
				}
				return queueSize, false, time.Since(started), fmt.Errorf("crafting may still be active; failed to read queue: %w", err)
			}
			queueSize = latest
		}
	}
	return queueSize, true, time.Since(started), nil
}

func craftQueueSize(ctx context.Context, r *Resolver, actor *ActorInput) (float64, error) {
	unit := uint64(0)
	playerValue, character := actorFields(actor)
	if character != nil {
		parsed, err := strconv.ParseUint(*character, 10, 32)
		if err != nil || parsed == 0 {
			return 0, errors.New("character must be a unit number from 1 to 4294967295")
		}
		unit = parsed
	}
	player := stringValueOr(playerValue, "")
	lua := luaFail + fmt.Sprintf(`local unit=%d; local p
if unit~=0 then p=game.get_entity_by_unit_number(unit); if not p or p.type~="character" then fail("character not found"); return end
else p=game.get_player(%s); if not p then fail("player not found"); return end end
rcon.print(helpers.table_to_json({queue_size=p.crafting_queue_size}))`, unit, luaString(player))
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return 0, err
	}
	var state struct {
		QueueSize *float64 `json:"queue_size"`
	}
	if err := json.Unmarshal([]byte(response), &state); err != nil {
		return 0, fmt.Errorf("decode craft queue status: %w", err)
	}
	if state.QueueSize == nil {
		return 0, errors.New("craft queue status omitted queue size")
	}
	return *state.QueueSize, nil
}

// craftRefusalMarker is the engine-side message craft returns when
// begin_crafting queued nothing, which does not say why.
const craftRefusalMarker = "could not queue recipe"

// explainCraftRefusal replaces the generic refusal with the concrete reason
// craftPlan already computes: not hand-craftable, or the ingredients the full
// hand-craft chain still lacks. It costs one extra read only on this failure
// path and falls back to the original error when the plan cannot say more.
func explainCraftRefusal(ctx context.Context, r *Resolver, input CraftInput, original error) error {
	if !strings.Contains(original.Error(), craftRefusalMarker) {
		return original
	}
	plan, err := craftPlanQuery(ctx, r, CraftPlanInput{Actor: input.Actor, Recipe: input.Recipe, Count: input.Count, Mode: input.Mode})
	if err != nil {
		return fmt.Errorf("craft %s refused: %w", input.Recipe, err)
	}
	if len(plan.Missing) == 0 {
		return fmt.Errorf("craft %s refused: the direct ingredients are not in the actor's inventory, but the hand-craft chain is satisfiable, so craftPlan can craft the intermediates first (%w)", input.Recipe, original)
	}
	parts := make([]string, 0, len(plan.Missing))
	for _, missing := range plan.Missing {
		parts = append(parts, fmt.Sprintf("%s %s", missing.Name, numberValue(&missing.Amount)))
	}
	return fmt.Errorf("craft %s refused: the actor lacks %s for the hand-craft chain (%w)", input.Recipe, strings.Join(parts, ", "), original)
}
