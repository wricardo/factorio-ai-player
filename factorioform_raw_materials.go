package main

// Raw-material expansion for Factorioform plans: expand the place-item
// materials (the items the actor must hold) through the force's enabled
// recipe catalogue down to recipe leaves, so a plan can report what raw
// stock (ores, stone, wood, fluids) the build ultimately needs.
//
// This reuses the planning snapshot types and planningNet from
// insights_planning.go. The catalogue fetch (one RCON call) mirrors the
// catalogue portion of billOfMaterialsQuery but skips stock entirely:
// raw aggregation is stock-agnostic, so no per-entity scan is needed.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// ffRawSnapshot is the recipe-catalogue portion of a planningSnapshot.
type ffRawSnapshot struct {
	Recipes                  luaList[planningRecipe]       `json:"bomRecipes"`
	LockedRecipes            luaList[planningLockedRecipe] `json:"bomLockedRecipes"`
	Mined                    luaList[planningRef]          `json:"bomMined"`
	Gathered                 luaList[planningRef]          `json:"bomGathered"`
	RecipeCatalogueTruncated bool                          `json:"recipeCatalogueTruncated"`
}

const ffRawLua = `
local force=game.forces[%s]; if not force then fail("force not found"); return end
local result={bomRecipes={},bomLockedRecipes={},bomMined={},bomGathered={},recipeCatalogueTruncated=false}
local function catalogue_recipe(recipe)
  if recipe.hidden then return false end
  local category=recipe.category
  if string.sub(category,1,9)=="recycling" then return false end
  if category=="parameters" or category=="parameter" then return false end
  return true
end
local names,locked={},{}
for name,recipe in pairs(force.recipes) do
  if catalogue_recipe(recipe) then
    if recipe.enabled then names[#names+1]=name else locked[#locked+1]=name end
  end
end
table.sort(names); table.sort(locked)
if #names>20000 or #locked>20000 then result.recipeCatalogueTruncated=true end
for i=1,math.min(#names,20000) do
  local recipe=force.recipes[names[i]]
  local entry={name=recipe.name,bomIngredients={},bomProducts={}}
  for _,p in pairs(recipe.ingredients) do
    entry.bomIngredients[#entry.bomIngredients+1]={item=p.name,materialType=p.type,amount=p.amount,
      temperatureConstrained=p.type=="fluid" and (p.minimum_temperature~=nil or p.maximum_temperature~=nil or p.temperature~=nil)}
  end
  for _,p in pairs(recipe.products) do
    local amount=p.amount or ((p.amount_min or 0)+(p.amount_max or 0))/2
    entry.bomProducts[#entry.bomProducts+1]={item=p.name,materialType=p.type,amount=amount*(p.probability or 1),
      probabilistic=(p.probability~=nil and p.probability~=1) or (p.amount_min~=nil and p.amount_min~=p.amount_max)}
  end
  result.bomRecipes[#result.bomRecipes+1]=entry
end
for i=1,math.min(#locked,20000) do
  local recipe=force.recipes[locked[i]]
  local entry={name=recipe.name,bomProducts={}}
  for _,p in pairs(recipe.products) do
    local amount=p.amount or ((p.amount_min or 0)+(p.amount_max or 0))/2
    entry.bomProducts[#entry.bomProducts+1]={item=p.name,materialType=p.type,amount=amount*(p.probability or 1)}
  end
  result.bomLockedRecipes[#result.bomLockedRecipes+1]=entry
end
local mined,gathered={},{}
for _,prototype in pairs(prototypes.entity) do
  local t=prototype.type
  local is_resource=t=="resource"
  local is_gathered=t=="tree" or t=="plant" or t=="simple-entity" or t=="fish" or t=="asteroid-chunk"
  if is_resource or is_gathered then
    local seen=is_resource and mined or gathered
    for _,p in pairs(prototype.mineable_properties.products or {}) do
      local key=p.type.."\0"..p.name
      if not seen[key] then
        seen[key]=true
        if is_resource then result.bomMined[#result.bomMined+1]={item=p.name,materialType=p.type}
        else result.bomGathered[#result.bomGathered+1]={item=p.name,materialType=p.type} end
      end
    end
  end
end
rcon.print(helpers.table_to_json(result))
`

// ffReadRawCatalogue fetches the enabled recipe catalogue, locked outputs,
// mined/gathered sets in ONE RCON call.
func ffReadRawCatalogue(ctx context.Context, r *Resolver, force string) (*ffRawSnapshot, error) {
	lua := luaFail + fmt.Sprintf(ffRawLua, luaString(force))
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	var snap ffRawSnapshot
	if err := json.Unmarshal([]byte(response), &snap); err != nil {
		return nil, fmt.Errorf("decode recipe catalogue read: %w", err)
	}
	return &snap, nil
}

// ffRawDemand is one aggregate place-item demand to expand.
type ffRawDemand struct {
	Name    string
	Quality string
	Count   int
}

// ffExpandRaw expands sorted demands through one shared catalogue and
// returns sorted recipe-leaf rows. Unresearched items and recipe-less
// non-raw items are omitted (they are still in plan.Materials); fluids
// inside recipes are leaves whose materialType is "fluid", attributed to
// the root item's quality, matching the detail rows' quality semantics.
func ffExpandRaw(snap *ffRawSnapshot, demands []ffRawDemand) []*FactorioformPlanRawMaterial {
	if snap == nil || len(demands) == 0 {
		return []*FactorioformPlanRawMaterial{}
	}
	recipes := map[string][]*planningRecipe{}
	byName := map[string]*planningRecipe{}
	for i := range snap.Recipes {
		recipe := &snap.Recipes[i]
		byName[recipe.Name] = recipe
		_, outputs, _ := planningNet(recipe)
		for _, p := range outputs {
			recipes[p.key()] = append(recipes[p.key()], recipe)
		}
	}
	for key, list := range recipes {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		recipes[key] = list
	}
	mined := map[string]bool{}
	for _, ref := range snap.Mined {
		mined[ref.key()] = true
	}
	gathered := map[string]bool{}
	for _, ref := range snap.Gathered {
		gathered[ref.key()] = true
	}
	locked := map[string]bool{}
	for _, recipe := range snap.LockedRecipes {
		for _, product := range recipe.Products {
			if product.Amount > 0 {
				locked[product.key()] = true
			}
		}
	}

	leaves := map[[2]string]float64{}
	expansions := 0
	active := map[string]bool{}
	var demand func(ref planningRef, amount float64, depth int, quality string)
	demand = func(ref planningRef, amount float64, depth int, quality string) {
		if amount <= 1e-9 {
			return
		}
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount > planningAmountLimit {
			return
		}
		if active[ref.key()] || depth > 512 || expansions >= maxPlanExpansions {
			return
		}
		// Resolve the recipe first, then fall back to mining. Space Age lists
		// mined and gathered refs for items a normal base crafts too
		// (iron-gear-wheel is "gathered" from crash-site wrecks, iron-ore is
		// "mined" but also producible by asteroid crushing), so testing
		// mining first would report a bare "iron-gear-wheel" requirement and
		// never expand it down to ore.
		expansions++
		list := recipes[ref.key()]
		var chosen *planningRecipe
		for _, recipe := range list {
			if recipe.Name == ref.Item {
				chosen = recipe
				break
			}
		}
		// Then a mined or gathered item is the raw leaf: how a normal base
		// actually obtains ore, stone and coal. This must come BEFORE any
		// fallback recipe, because Space Age also offers recipes producing
		// those items from asteroid chunks or lava, and recursing into one
		// reports a chunk/lava chain instead of the ore the build really needs.
		if chosen == nil && (mined[ref.key()] || gathered[ref.key()]) {
			leaves[[2]string{ref.Item, quality}] += amount
			return
		}
		// Then any recipe whose inputs are all leaves or themselves
		// craftable, preferring the plain route: Space Age adds alternatives
		// through lava or scrap (casting-iron, scrap-recycling) which would
		// otherwise report lava as the raw material for iron plates.
		if chosen == nil {
			for _, recipe := range list {
				inputs, _, _ := planningNet(recipe)
				simple := true
				for _, in := range inputs {
					if !mined[in.key()] && !gathered[in.key()] && in.MaterialType != "fluid" && len(recipes[in.key()]) == 0 {
						simple = false
						break
					}
				}
				if simple {
					chosen = recipe
					break
				}
			}
		}
		if chosen == nil {
			// A mined or gathered item with no usable recipe is the raw leaf.
			// Unresearched and recipe-less non-raw items are omitted: they are
			// still listed in plan.Materials.
			if mined[ref.key()] || gathered[ref.key()] || ref.MaterialType == "fluid" {
				leaves[[2]string{ref.Item, quality}] += amount
			}
			return
		}
		// Net inputs, mirroring planBillOfMaterials.
		inputs, outputs, _ := planningNet(chosen)
		yield := 0.0
		for _, p := range outputs {
			if p.key() == ref.key() {
				yield = p.Amount
			}
		}
		if yield <= 0 || math.IsNaN(yield) || math.IsInf(yield, 0) {
			return
		}
		crafts := math.Ceil(amount/yield - 1e-12)
		if crafts < 1 {
			crafts = 1
		}
		if crafts > planningAmountLimit {
			return
		}
		for _, q := range append(append([]planningQuantity{}, inputs...), outputs...) {
			if q.Amount < 0 || math.IsNaN(q.Amount) || math.IsInf(q.Amount, 0) || q.Amount > planningAmountLimit/crafts {
				return
			}
		}
		active[ref.key()] = true
		for _, q := range inputs {
			demand(q.planningRef, q.Amount*crafts, depth+1, quality)
		}
		delete(active, ref.key())
		// Coproduct outputs are added to the leaf pool indirectly via
		// yield only for the requested item; other outputs are ignored
		// (no co-product sharing across roots in this aggregation).
	}

	// Deterministic demand order: input slice is already sorted by caller,
	// but re-sort defensively so output is stable regardless of call site.
	sort.Slice(demands, func(i, j int) bool {
		if demands[i].Name != demands[j].Name {
			return demands[i].Name < demands[j].Name
		}
		return demands[i].Quality < demands[j].Quality
	})
	for _, d := range demands {
		if d.Count <= 0 {
			continue
		}
		root := planningRef{Item: d.Name, MaterialType: "item"}
		// A fluid placed directly (e.g. a fluid item never appears here —
		// materials are place items, always items) stays an item root.
		demand(root, float64(d.Count), 0, d.Quality)
	}
	keys := make([][2]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := make([]*FactorioformPlanRawMaterial, 0, len(keys))
	for _, k := range keys {
		total := leaves[k]
		if total <= 1e-9 {
			continue
		}
		out = append(out, &FactorioformPlanRawMaterial{Name: k[0], Quality: k[1], Count: int(math.Round(total))})
	}
	return out
}

// ffAttachRawMaterials expands plan.Materials into plan.RawMaterials in
// place. Empty materials short-circuit without an RCON read. A catalogue
// fetch error is fatal to the plan: silently returning empty rawMaterials
// would be indistinguishable from a plan that genuinely needs no raw stock.
func ffAttachRawMaterials(ctx context.Context, r *Resolver, force string, plan *FactorioformPlan) error {
	if plan == nil || len(plan.Materials) == 0 {
		if plan != nil {
			plan.RawMaterials = []*FactorioformPlanRawMaterial{}
		}
		return nil
	}
	demands := make([]ffRawDemand, 0, len(plan.Materials))
	for _, m := range plan.Materials {
		demands = append(demands, ffRawDemand{Name: m.Name, Quality: m.Quality, Count: m.Count})
	}
	snap, err := ffReadRawCatalogue(ctx, r, force)
	if err != nil {
		return fmt.Errorf("expand raw materials for plan %q: %w", plan.Manifest, err)
	}
	plan.RawMaterials = ffExpandRaw(snap, demands)
	if plan.RawMaterials == nil {
		plan.RawMaterials = []*FactorioformPlanRawMaterial{}
	}
	return nil
}
