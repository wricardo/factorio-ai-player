package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// craftHandLua defines hand_craftable(recipe) for the character crafting
// categories. craft and craftPlan share it so both refuse a recipe the actor
// cannot hand craft with the same reason.
const craftHandLua = `
local hand_categories={}
for name in pairs(prototypes.entity["character"].crafting_categories or {}) do hand_categories[name]=true end
local function hand_craftable(recipe)
  if hand_categories[recipe.category] then return true end
  for _,category in pairs(recipe.additional_categories or {}) do
    if hand_categories[category] then return true end
  end
  return false
end
`

// craftStockLua defines craft_free_stock() and craft_crafts_needed().
// Queued crafts do not take their ingredients when queued (the engine consumes
// them as each craft starts), so the main inventory over-reports what a new
// plan can spend and under-reports what the queue will deliver. The free stock
// is therefore inventory + queued products - queued ingredients, and UP_TO
// converts a wanted product total into the crafts still missing from it.
// Needs p; the target recipe argument is the one whose first item product
// UP_TO counts.
const craftStockLua = `
local function craft_free_stock()
  local stock={}
  local inventory=p.get_main_inventory()
  if inventory then
    for _,stack in ipairs(inventory.get_contents()) do
      stock[stack.name]=(stock[stack.name] or 0)+stack.count
    end
  end
  for _,entry in ipairs(p.crafting_queue or {}) do
    local queued=p.force.recipes[entry.recipe]
    if queued then
      for _,product in ipairs(queued.products) do
        if product.type=="item" then
          local amount=(product.amount or product.amount_min or 1)*(product.probability or 1)
          stock[product.name]=(stock[product.name] or 0)+amount*entry.count
        end
      end
      for _,ingredient in ipairs(queued.ingredients) do
        if ingredient.type=="item" then
          stock[ingredient.name]=(stock[ingredient.name] or 0)-ingredient.amount*entry.count
        end
      end
    end
  end
  return stock
end
local function craft_crafts_needed(recipe,stock,mode,count)
  if mode~="UP_TO" then return count end
  for _,product in ipairs(recipe.products) do
    if product.type=="item" then
      local per_craft=(product.amount or product.amount_min or 1)*(product.probability or 1)
      if per_craft<=0 then per_craft=1 end
      local deficit=count-math.max(0,stock[product.name] or 0)
      if deficit<=0 then return 0 end
      return math.ceil(deficit/per_craft)
    end
  end
  fail("UP_TO needs a recipe with an item product"); return 0
end
`

// craftPlanLua plans hand crafts from the actor's main inventory. Every
// missing item ingredient is expanded recursively through an enabled,
// non-hidden, hand-craftable producer recipe, so the emitted steps are ordered
// bottom-up and each step's ingredients exist in stock or come from an earlier
// step. Fluids and items without such a recipe land in missing with the
// aggregated shortfall. Format arguments: unit number, player name, requested
// crafts, recipe name.
const craftPlanLua = luaFail + craftHandLua + craftStockLua + `
local requested=%d
local mode=%s
local target=p.force.recipes[%s]
if not target then fail("recipe not found"); return end
if not target.enabled then fail("recipe is not enabled"); return end
if target.hidden then fail("recipe is hidden and cannot be hand crafted"); return end
if not hand_craftable(target) then fail("recipe is not hand-craftable by a character"); return end
local function amount_of(entry)
  if entry.amount then return entry.amount end
  if entry.amount_min then return entry.amount_min end
  return 1
end
local producers={}
-- A recycling recipe, or one whose product is probabilistic, cannot be relied
-- on to yield the item: scrap-recycling yields steel-plate with probability
-- 0.04, so planning it as a producer asks for scrap and delivers almost nothing.
local function deterministic_producer(candidate,item_name)
  if string.sub(candidate.category,1,9)=="recycling" then return false end
  local found=false
  for _,product in ipairs(candidate.products) do
    if product.name==item_name then
      if product.probability and product.probability<1 then return false end
      found=true
    end
  end
  return found
end
local function producer_for(item_name)
  if producers[item_name]~=nil then return producers[item_name] or nil end
  local best=nil
  for _,candidate in pairs(p.force.recipes) do
    if candidate.enabled and not candidate.hidden and hand_craftable(candidate) and deterministic_producer(candidate,item_name) then
      if best==nil or candidate.name<best.name then best=candidate end
    end
  end
  producers[item_name]=best or false
  return best
end
local stock=craft_free_stock()
requested=craft_crafts_needed(target,stock,mode,requested)
local steps={}
local missing={}
local missing_index={}
local function add_missing(name,kind,amount)
  local key=kind..":"..name
  if not missing_index[key] then
    missing_index[key]={name=name,type=kind,amount=0}
    missing[#missing+1]=missing_index[key]
  end
  missing_index[key].amount=missing_index[key].amount+amount
end
local visiting={}
local function plan(recipe, crafts, depth)
  if depth>16 then fail("craft dependency chain is deeper than 16 levels; craft the intermediates with craft instead"); return end
  if visiting[recipe.name] then fail("craft dependency cycle at recipe "..recipe.name); return end
  visiting[recipe.name]=true
  local ingredients={}
  for _,ingredient in ipairs(recipe.ingredients) do
    local amount=amount_of(ingredient)*crafts
    ingredients[#ingredients+1]={name=ingredient.name,type=ingredient.type,amount=amount}
    if ingredient.type=="fluid" then
      add_missing(ingredient.name,"fluid",amount)
    else
      local have=math.max(0,stock[ingredient.name] or 0)
      local used=math.min(have,amount)
      stock[ingredient.name]=have-used
      local need=amount-used
      if need>0 then
        local producer=producer_for(ingredient.name)
        if not producer then
          add_missing(ingredient.name,"item",need)
        else
          local per_craft=0
          for _,product in ipairs(producer.products) do
            if product.name==ingredient.name then per_craft=per_craft+amount_of(product) end
          end
          if per_craft<1 then per_craft=1 end
          local sub_crafts=math.ceil(need/per_craft)
          plan(producer,sub_crafts,depth+1)
          stock[ingredient.name]=stock[ingredient.name]+per_craft*sub_crafts-need
          for _,product in ipairs(producer.products) do
            if product.name~=ingredient.name then
              stock[product.name]=(stock[product.name] or 0)+amount_of(product)*sub_crafts
            end
          end
        end
      end
    end
  end
  local products={}
  for _,product in ipairs(recipe.products) do
    products[#products+1]={name=product.name,type=product.type,amount=amount_of(product)}
  end
  steps[#steps+1]={recipe=recipe.name,count=crafts,products=products,ingredients=ingredients}
  visiting[recipe.name]=nil
end
if requested>0 then plan(target,requested,1) end
local payload={tick=game.tick,surface=p.surface.name,force=p.force.name,recipe=target.name,requested=requested,
  complete=#missing==0,steps=steps,missing=missing}
if unit~=0 then payload.character=tostring(unit) else payload.player=p.name end
rcon.print(helpers.table_to_json(payload))`

// craftPlanAttempt is one bottom-up queue attempt: count crafts of recipe.
type craftPlanAttempt struct {
	recipe string
	count  float64
}

// craftQueuePlanLua queues the affordable attempts in one tick and reports the
// index of each attempt the engine actually started, because begin_crafting
// validates the ingredients at queue time and silently refuses the rest.
func craftQueuePlanLua(unit uint64, player string, attempts []craftPlanAttempt) string {
	var entries strings.Builder
	entries.WriteString("{")
	for i, attempt := range attempts {
		if i > 0 {
			entries.WriteString(",")
		}
		fmt.Fprintf(&entries, "{recipe=%s,count=%d}", luaString(attempt.recipe), int64(attempt.count))
	}
	entries.WriteString("}")
	return fmt.Sprintf(luaActionActor+luaFail+`local entries=%s
local queued={}
for index,entry in ipairs(entries) do
  local recipe=p.force.recipes[entry.recipe]
  local started=0
  if recipe and recipe.enabled then
    started=p.begin_crafting{recipe=entry.recipe,count=entry.count,silent=true}
  end
  if started>0 then queued[#queued+1]={index=index,count=started} end
end
rcon.print(helpers.table_to_json({queued=queued,queue_size=p.crafting_queue_size}))`, unit, luaString(player), entries.String())
}

func craftPlanLuaFor(actor *ActorInput, crafted float64, mode CraftMode, recipe string) (string, error) {
	unit, player, err := craftPlanActor(actor)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(luaActionActor+craftPlanLua, unit, luaString(player), int64(crafted), luaString(craftModeValue(mode)), luaString(recipe)), nil
}

func craftPlanActor(actor *ActorInput) (uint64, string, error) {
	playerValue, character := actorFields(actor)
	return settingsActor(stringValueOr(character, ""), stringValueOr(playerValue, ""))
}

// craftPlanCounts validates the requested craft count and returns it as an
// integer; the crafting API takes whole crafts only.
func craftPlanCounts(count *float64) (float64, error) {
	requested := 1.0
	if count != nil {
		requested = *count
	}
	if requested < 1 || requested > math.MaxUint32 || requested != math.Trunc(requested) {
		return 0, errors.New("count must be a whole number of crafts between 1 and 4294967295")
	}
	return requested, nil
}

// craftPlanPayload is the shape craftPlanLua prints.
type craftPlanPayload struct {
	Tick      float64             `json:"tick"`
	Surface   string              `json:"surface"`
	Force     string              `json:"force"`
	Player    *string             `json:"player,omitempty"`
	Character *string             `json:"character,omitempty"`
	Recipe    string              `json:"recipe"`
	Requested float64             `json:"requested"`
	Complete  bool                `json:"complete"`
	Steps     []*CraftPlanStep    `json:"steps"`
	Missing   []*CraftPlanMissing `json:"missing"`
}

// craftPlanQuery resolves the read-only Query.craftPlan field.
func craftPlanQuery(ctx context.Context, r *Resolver, input CraftPlanInput) (*CraftPlan, error) {
	if strings.TrimSpace(input.Recipe) == "" {
		return nil, errors.New("recipe is required")
	}
	requested, err := craftPlanCounts(input.Count)
	if err != nil {
		return nil, err
	}
	lua, err := craftPlanLuaFor(input.Actor, requested, input.Mode, input.Recipe)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	payload, err := factoryDecode[craftPlanPayload](response)
	if err != nil {
		return nil, fmt.Errorf("decode craft plan: %w", err)
	}
	return &CraftPlan{
		Tick:      payload.Tick,
		Surface:   payload.Surface,
		Force:     payload.Force,
		Actor:     &ActorIdentity{Player: payload.Player, Character: payload.Character},
		Player:    payload.Player,
		Character: payload.Character,
		Recipe:    payload.Recipe,
		Requested: payload.Requested,
		Complete:  payload.Complete,
		Steps:     nonNilSteps(payload.Steps),
		Missing:   nonNilMissing(payload.Missing),
	}, nil
}

func nonNilSteps(steps []*CraftPlanStep) []*CraftPlanStep {
	if steps == nil {
		return []*CraftPlanStep{}
	}
	return steps
}

func nonNilMissing(missing []*CraftPlanMissing) []*CraftPlanMissing {
	if missing == nil {
		return []*CraftPlanMissing{}
	}
	return missing
}

// craftModeValue returns the wire value of a craft mode, defaulting to MORE.
func craftModeValue(mode CraftMode) string {
	if mode == CraftModeUpTo {
		return string(CraftModeUpTo)
	}
	return string(CraftModeMore)
}

// craftExpand resolves Mutation.craft with expand true: plan the hand-craft
// chain, then queue it bottom-up. With wait false only the first round is
// queued (everything affordable now) and the rest is reported in remaining;
// with wait true the queue is drained between rounds so each intermediate
// exists before its consumer is queued.
func craftExpand(ctx context.Context, r *Resolver, input CraftInput) (*ActionResult, error) {
	plan, err := craftPlanQuery(ctx, r, CraftPlanInput{Actor: input.Actor, Recipe: input.Recipe, Count: input.Count, Mode: input.Mode})
	if err != nil {
		return nil, err
	}
	chain := &CraftPlanResult{
		Tick:          plan.Tick,
		Surface:       plan.Surface,
		Force:         plan.Force,
		Actor:         plan.Actor,
		Player:        plan.Player,
		Character:     plan.Character,
		Recipe:        plan.Recipe,
		Requested:     plan.Requested,
		Complete:      false,
		Queued:        []*CraftPlanStep{},
		Remaining:     nonNilSteps(plan.Steps),
		Missing:       nonNilMissing(plan.Missing),
		WaitTimedOut:  false,
		WaitedSeconds: 0,
	}
	if len(plan.Missing) > 0 {
		empty := 0.0
		chain.QueueSize = &empty
		announceMutation(r, input.Message, craftPlanSummary(chain, len(plan.Missing)))
		return craftChainResult(chain), nil
	}
	unit, player, err := craftPlanActor(input.Actor)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(clampWaitSeconds(input.WaitTimeoutSeconds))
	var waited time.Duration
	var queueSize float64
	if input.Wait {
		// UP_TO re-issues land on a queue that is already running; only a
		// drain wait can say when the earlier call's crafts are done.
		queueSize, err = craftQueueSize(ctx, r, input.Actor)
		if err != nil {
			return nil, err
		}
	}
	pending := append([]*CraftPlanStep(nil), plan.Steps...)
	var queued []*CraftPlanStep
	for len(pending) > 0 || (input.Wait && queueSize > 0) {
		if time.Now().After(deadline) {
			chain.WaitTimedOut = true
			break
		}
		if len(pending) > 0 {
			attempts := make([]craftPlanAttempt, 0, len(pending))
			for _, step := range pending {
				attempts = append(attempts, craftPlanAttempt{recipe: step.Recipe, count: step.Count})
			}
			started, size, err := craftQueueAttempts(ctx, r, unit, player, attempts)
			if err != nil {
				return nil, err
			}
			queueSize = size
			progress := false
			next := pending[:0]
			for i, step := range pending {
				began := started[i]
				if began <= 0 {
					next = append(next, step)
					continue
				}
				progress = true
				queued = append(queued, &CraftPlanStep{Recipe: step.Recipe, Count: began, Ingredients: step.Ingredients, Products: step.Products})
				step.Count -= began
				if step.Count > 0 {
					next = append(next, step)
				}
			}
			pending = next
			if !progress && queueSize <= 0 {
				return nil, fmt.Errorf("craft plan stalled: the engine refused %s though the plan expects its ingredients; inventory may have changed, rerun craftPlan", pending[0].Recipe)
			}
		}
		if !input.Wait {
			break
		}
		if queueSize <= 0 {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			chain.WaitTimedOut = true
			break
		}
		waitedQueue, drained, waitSpent, err := waitForCraftQueue(ctx, queueSize, remaining, func(ctx context.Context) (float64, error) {
			return craftQueueSize(ctx, r, input.Actor)
		})
		waited += waitSpent
		if err != nil {
			return nil, err
		}
		queueSize = waitedQueue
		if !drained {
			chain.WaitTimedOut = true
			break
		}
	}
	chain.Queued = queued
	chain.Remaining = nonNilSteps(pending)
	chain.QueueSize = &queueSize
	chain.WaitedSeconds = waited.Seconds()
	chain.Complete = len(pending) == 0 && queueSize <= 0 && !chain.WaitTimedOut
	announceMutation(r, input.Message, craftPlanSummary(chain, 0))
	return craftChainResult(chain), nil
}

// craftChainResult folds the chain outcome into the craft result: the legacy
// scalar fields describe the target recipe, chain carries the full plan.
func craftChainResult(chain *CraftPlanResult) *ActionResult {
	var started float64
	for _, step := range chain.Queued {
		if step.Recipe == chain.Recipe {
			started += step.Count
		}
	}
	complete := chain.Complete
	timedOut := chain.WaitTimedOut
	waited := chain.WaitedSeconds
	recipe := chain.Recipe
	requested := chain.Requested
	return &ActionResult{
		Actor:            chain.Actor,
		Character:        chain.Character,
		Player:           chain.Player,
		Tick:             chain.Tick,
		Surface:          chain.Surface,
		Force:            chain.Force,
		Recipe:           &recipe,
		Requested:        &requested,
		Started:          &started,
		QueueSize:        chain.QueueSize,
		CraftingComplete: &complete,
		WaitTimedOut:     &timedOut,
		WaitedSeconds:    &waited,
		Chain:            chain,
	}
}

// craftQueueAttempts runs one queue round and returns the crafts the engine
// started per attempt index plus the resulting queue size.
func craftQueueAttempts(ctx context.Context, r *Resolver, unit uint64, player string, attempts []craftPlanAttempt) ([]float64, float64, error) {
	lua := craftQueuePlanLua(unit, player, attempts)
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, 0, err
	}
	payload, err := factoryDecode[struct {
		Queued []struct {
			Index int     `json:"index"`
			Count float64 `json:"count"`
		} `json:"queued"`
		QueueSize *float64 `json:"queueSize"`
	}](response)
	if err != nil {
		return nil, 0, fmt.Errorf("decode craft queue result: %w", err)
	}
	if payload.QueueSize == nil {
		return nil, 0, errors.New("craft queue response omitted queue size")
	}
	started := make([]float64, len(attempts))
	for _, queued := range payload.Queued {
		if queued.Index < 1 || queued.Index > len(started) {
			return nil, 0, fmt.Errorf("craft queue response referenced attempt %d of %d", queued.Index, len(started))
		}
		started[queued.Index-1] = queued.Count
	}
	return started, *payload.QueueSize, nil
}

func craftPlanSummary(result *CraftPlanResult, missing int) string {
	if missing > 0 {
		return fmt.Sprintf("craft plan for %s needs %d missing ingredient types; nothing queued", result.Recipe, missing)
	}
	if result.Complete {
		return fmt.Sprintf("crafted %s x%s in %d queued steps", result.Recipe, numberValue(&result.Requested), len(result.Queued))
	}
	if result.WaitTimedOut {
		return fmt.Sprintf("queued %d steps for %s x%s; wait timed out with %d steps remaining", len(result.Queued), result.Recipe, numberValue(&result.Requested), len(result.Remaining))
	}
	return fmt.Sprintf("queued %d steps for %s x%s", len(result.Queued), result.Recipe, numberValue(&result.Requested))
}
