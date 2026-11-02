package main

import (
	"strings"
	"testing"
)

// Hidden recipes (which is where every recycling and parameter recipe lives) are
// excluded by default, and each row still reports its own hidden flag.
func TestItemRecipesExcludesHiddenByDefault(t *testing.T) {
	response := `{"tick":4,"force":"player","item":"iron-gear-wheel",
	  "pageInfo":{"total":1,"limit":100,"offset":0,"hasMore":false},
	  "recipes":[{"name":"iron-gear-wheel","enabled":true,"hidden":false,"category":"crafting",
	    "additionalCategories":[],"energy":0.5,"handCraftable":true,
	    "ingredients":[{"name":"iron-plate","type":"item","amount":2}],"products":[{"name":"iron-gear-wheel","type":"item","amount":1}],
	    "craftingMachines":["assembling-machine-1","character"]}]}`
	r, commands := serveLuaOnce(t, response)
	page, err := itemRecipesQuery(r, ItemRecipesInput{Force: "player", Item: "iron-gear-wheel", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	recipe := page.Recipes[0]
	if recipe.Hidden || !recipe.HandCraftable || len(recipe.AdditionalCategories) != 0 ||
		len(recipe.CraftingMachines) != 2 || recipe.CraftingMachines[0] != "assembling-machine-1" {
		t.Fatalf("recipe: %+v", recipe)
	}
	lua := <-commands
	for _, want := range []string{
		`local include_hidden=false`,
		`(include_hidden or not recipe.hidden)`,
		`local recipe_categories={recipe.category}`,
		`for _,category in pairs(additional) do recipe_categories[#recipe_categories+1]=category end`,
		`for name in pairs(prototypes.entity["character"].crafting_categories or {}) do hand_categories[name]=true end`,
		`handCraftable=hand_craftable`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("itemRecipes Lua missing %q\n%s", want, lua)
		}
	}
}

// includeHidden must reach Lua so a caller can still inspect recycling recipes.
func TestItemRecipesIncludeHiddenFlag(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":4,"force":"player","item":"iron-gear-wheel","pageInfo":{"total":0,"limit":10,"offset":0,"hasMore":false},"recipes":[]}`)
	if _, err := itemRecipesQuery(r, ItemRecipesInput{Force: "player", Item: "iron-gear-wheel", IncludeHidden: true, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if lua := <-commands; !strings.Contains(lua, `local include_hidden=true`) {
		t.Fatalf("includeHidden not forwarded:\n%s", lua)
	}
}

// A recipe whose only crafting category is an additional category must list the
// machines of that category.
func TestItemRecipesCraftsThroughAdditionalCategories(t *testing.T) {
	response := `{"tick":4,"force":"player","item":"molten-iron",
	  "pageInfo":{"total":1,"limit":10,"offset":0,"hasMore":false},
	  "recipes":[{"name":"molten-iron","enabled":true,"hidden":false,"category":"metallurgy",
	    "additionalCategories":["metallurgy-or-assembling"],"energy":3.2,"handCraftable":false,
	    "ingredients":[],"products":[],"craftingMachines":["assembling-machine-2","foundry"]}]}`
	r, _ := serveLuaOnce(t, response)
	page, err := itemRecipesQuery(r, ItemRecipesInput{Force: "player", Item: "molten-iron", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	recipe := page.Recipes[0]
	if len(recipe.AdditionalCategories) != 1 || recipe.AdditionalCategories[0] != "metallurgy-or-assembling" ||
		len(recipe.CraftingMachines) != 2 || recipe.HandCraftable {
		t.Fatalf("recipe: %+v", recipe)
	}
}
