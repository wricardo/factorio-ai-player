package main

import (
	"errors"
	"fmt"
)

func itemRecipesQuery(r *Resolver, input ItemRecipesInput) (*ItemRecipePage, error) {
	if input.Force == "" || input.Item == "" {
		return nil, errors.New("force and item are required")
	}
	if input.Limit < 1 || input.Limit > maxPageSize || input.Offset < 0 {
		return nil, fmt.Errorf("limit must be between 1 and %d and offset must be non-negative", maxPageSize)
	}
	enabled := "nil"
	if input.Enabled != nil {
		if *input.Enabled {
			enabled = "true"
		} else {
			enabled = "false"
		}
	}
	lua := luaFail + fmt.Sprintf(`
local force=game.forces[%s]; if not force then fail("force not found"); return end
local item=%s; local wanted=%s; local include_hidden=%t; local limit=%d; local offset=%d
local matching={}
for _, recipe in pairs(force.recipes) do
  if (wanted==nil or recipe.enabled==wanted) and (include_hidden or not recipe.hidden) then
    for _, product in pairs(recipe.products) do
      if product.name==item then matching[#matching+1]=recipe; break end
    end
  end
end
table.sort(matching, function(a,b) return a.name < b.name end)
-- Crafting machines are matched on the union of category and additional_categories,
-- and hand crafting is decided from the character crafting categories.
local categories={}
for _,prototype in pairs(prototypes.entity) do
  if prototype.type=="assembling-machine" or prototype.type=="furnace" or prototype.type=="rocket-silo" then
    for category in pairs(prototype.crafting_categories or {}) do
      if not categories[category] then categories[category]={} end
      categories[category][#categories[category]+1]=prototype.name
    end
  end
end
for _,names in pairs(categories) do table.sort(names) end
local hand_categories={}
for name in pairs(prototypes.entity["character"].crafting_categories or {}) do hand_categories[name]=true end
local recipes={}
for i=offset+1,math.min(#matching,offset+limit) do
  local recipe=matching[i]
  local recipe_categories={recipe.category}
  local additional=recipe.additional_categories or {}
  for _,category in pairs(additional) do recipe_categories[#recipe_categories+1]=category end
  local machines, seen = {}, {}
  local hand_craftable = false
  for _,category in pairs(recipe_categories) do
    for _,name in pairs(categories[category] or {}) do
      if not seen[name] then seen[name]=true; machines[#machines+1]=name end
    end
    if hand_categories[category] then hand_craftable = true end
  end
  table.sort(machines)
  recipes[#recipes+1]={name=recipe.name,enabled=recipe.enabled,hidden=recipe.hidden,category=recipe.category,
    additionalCategories=additional,energy=recipe.energy,handCraftable=hand_craftable,
    ingredients=recipe.ingredients,products=recipe.products,craftingMachines=machines}
end
rcon.print(helpers.table_to_json({tick=game.tick,force=force.name,item=item,pageInfo={total=#matching,limit=limit,offset=offset,hasMore=#matching>offset+limit},recipes=recipes}))`, luaString(input.Force), luaString(input.Item), enabled, input.IncludeHidden, input.Limit, input.Offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ItemRecipePage](response)
}
