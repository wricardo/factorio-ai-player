package main

import (
	"errors"
	"fmt"
)

func itemRecipesQuery(r *Resolver, input ItemRecipesInput) (*ItemRecipePage, error) {
	if input.Force == "" || input.Item == "" {
		return nil, errors.New("force and item are required")
	}
	if input.Limit < 1 || input.Limit > 1000 || input.Offset < 0 {
		return nil, errors.New("limit must be between 1 and 1000 and offset must be non-negative")
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
local item=%s; local wanted=%s; local limit=%d; local offset=%d
local matching={}
for _, recipe in pairs(force.recipes) do
  if wanted==nil or recipe.enabled==wanted then
    for _, product in pairs(recipe.products) do
      if product.name==item then matching[#matching+1]=recipe; break end
    end
  end
end
table.sort(matching, function(a,b) return a.name < b.name end)
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
local recipes={}
for i=offset+1,math.min(#matching,offset+limit) do
  local recipe=matching[i]
  recipes[#recipes+1]={name=recipe.name,enabled=recipe.enabled,category=recipe.category,energy=recipe.energy,ingredients=recipe.ingredients,products=recipe.products,craftingMachines=categories[recipe.category] or {}}
end
rcon.print(helpers.table_to_json({tick=game.tick,force=force.name,item=item,page_info={total=#matching,limit=limit,offset=offset,has_more=#matching>offset+limit},recipes=recipes}))`, luaString(input.Force), luaString(input.Item), enabled, input.Limit, input.Offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ItemRecipePage](response)
}
