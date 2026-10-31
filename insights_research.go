package main

import (
	"errors"
	"fmt"
)

// researchInsightsQuery reports force research state without changing it.
func researchInsightsQuery(r *Resolver, input *ResearchInsightsInput) (*ResearchInsights, error) {
	if input == nil {
		input = &ResearchInsightsInput{Force: "player", Status: ResearchInsightsStatusAll, Limit: 100}
	}
	if input.Force == "" {
		return nil, errors.New("force is required")
	}
	if input.Limit < 1 || input.Limit > 1000 {
		return nil, errors.New("limit must be between 1 and 1000")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be non-negative")
	}
	switch input.Status {
	case ResearchInsightsStatusAll, ResearchInsightsStatusResearched, ResearchInsightsStatusAvailable, ResearchInsightsStatusLocked, ResearchInsightsStatusInProgress:
	default:
		return nil, errors.New("invalid research status")
	}
	technology, recipe := "", ""
	if input.Technology != nil {
		technology = *input.Technology
		if technology == "" {
			return nil, errors.New("technology must not be empty")
		}
	}
	if input.UnlocksRecipe != nil {
		recipe = *input.UnlocksRecipe
		if recipe == "" {
			return nil, errors.New("unlocksRecipe must not be empty")
		}
	}
	lua := luaFail + fmt.Sprintf(`
local force_name, technology_name, recipe_name, status = %s, %s, %s, %s
local limit, offset = %d, %d
local force = game.forces[force_name]
if not force then fail("force not found: "..force_name); return end
if technology_name ~= "" and not force.technologies[technology_name] then fail("technology not found: "..technology_name); return end
if recipe_name ~= "" and not force.recipes[recipe_name] then fail("recipe not found: "..recipe_name); return end
local current = force.current_research
local current_name = current and current.name or nil
local queue = {}
for _, tech in ipairs(force.research_queue) do
  queue[#queue+1] = type(tech) == "string" and tech or tech.name
end
local matches = {}
for _, tech in pairs(force.technologies) do
  local prerequisites, ready = {}, true
  for _, prereq in pairs(tech.prerequisites) do
    prerequisites[#prerequisites+1] = {name=prereq.name, researched=prereq.researched}
    if not prereq.researched then ready = false end
  end
  local available = tech.enabled and not tech.researched and ready
  local in_progress = current_name == tech.name
  local status_match = status == "ALL" or
    (status == "RESEARCHED" and tech.researched) or
    (status == "AVAILABLE" and available) or
    (status == "LOCKED" and not tech.researched and not available) or
    (status == "IN_PROGRESS" and in_progress)
  if status_match and (technology_name == "" or technology_name == tech.name) then
    local effects, recipe_match = {}, recipe_name == ""
    for _, effect in ipairs(tech.prototype.effects) do
      if effect.type == "unlock-recipe" and effect.recipe == recipe_name then recipe_match = true end
      effects[#effects+1] = {type=effect.type, recipe=effect.recipe, dataJSON=helpers.table_to_json(effect)}
    end
    if recipe_match then
      table.sort(prerequisites, function(a,b) return a.name < b.name end)
      local ingredients = {}
      for _, ingredient in ipairs(tech.research_unit_ingredients) do
        ingredients[#ingredients+1] = {name=ingredient.name, amount=ingredient.amount}
      end
      table.sort(ingredients, function(a,b) return a.name < b.name end)
      local trigger = tech.prototype.research_trigger
      matches[#matches+1] = {name=tech.name, enabled=tech.enabled, researched=tech.researched,
        inProgress=in_progress, available=available, level=tech.level, savedProgress=tech.saved_progress,
        prerequisites=prerequisites, scienceIngredients=ingredients, researchUnitCount=tech.research_unit_count,
        researchUnitEnergy=tech.research_unit_energy, researchTriggerJSON=trigger and helpers.table_to_json(trigger) or nil,
        effects=effects}
    end
  end
end
table.sort(matches, function(a,b) return a.name < b.name end)
local page = {}
for i=offset+1, math.min(#matches, offset+limit) do page[#page+1] = matches[i] end
rcon.print(helpers.table_to_json({tick=game.tick, force=force.name, researchEnabled=force.research_enabled,
  currentTechnology=current_name, progress=current and force.research_progress or nil,
  researchQueue=queue, pageInfo={total=#matches,limit=limit,offset=offset,hasMore=#matches>offset+limit}, technologies=page}))`,
		luaString(input.Force), luaString(technology), luaString(recipe), luaString(string(input.Status)), input.Limit, input.Offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ResearchInsights](response)
}
