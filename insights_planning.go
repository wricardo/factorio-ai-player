package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

func planningAreaLua(area *FactoryAreaInput) (string, error) {
	if area == nil {
		return "nil", nil
	}
	value, _, err := worldAreaLua(area, 1, 0)
	return value, err
}

func itemFlowQuery(r *Resolver, input ItemFlowInput) (*ItemFlow, error) {
	if input.Force == "" || input.Surface == "" || input.Item == "" {
		return nil, errors.New("force, surface and item are required")
	}
	if input.Limit < 1 || input.Limit > 1000 || input.Offset < 0 {
		return nil, errors.New("limit must be 1..1000 and offset non-negative")
	}
	area, err := planningAreaLua(input.Area)
	if err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`
local force=game.forces[%s]; if not force then fail("force not found"); return end
local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local item,area,limit,offset=%s,%s,%d,%d
if not prototypes.item[item] and not prototypes.fluid[item] then fail("item/fluid prototype not found"); return end
local statuses={}; for name,value in pairs(defines.entity_status) do statuses[value]=name end
local producers,consumers={},{}
local function yield(p)
  return (p.amount or ((p.amount_min or 0)+(p.amount_max or 0))/2)*(p.probability or 1)
end
local function endpoint(e,kind,role,rate,reason,recipe,quality,target)
  local obj={unitNumber=e.unit_number,name=e.name,type=e.type,surface=surface.name,force=force.name,
    position=e.position,status=statuses[e.status] or "unknown",recipe=recipe,recipeQuality=quality,
    miningTarget=target,materialType=kind,bufferedItems={},theoreticalRatePerSecond=rate,rateUnavailableReason=reason}
  if kind=="item" then
    local inv
    if role=="output" then inv=e.get_output_inventory()
    elseif e.type=="assembling-machine" or e.type=="furnace" or e.type=="rocket-silo" then inv=e.get_inventory(defines.inventory.crafter_input) end
    if inv then for _,s in pairs(inv.get_contents()) do if s.name==item then
      obj.bufferedItems[#obj.bufferedItems+1]={name=s.name,quality=s.quality,count=s.count}
    end end end
    table.sort(obj.bufferedItems,function(a,b) return a.quality<b.quality end)
  else
    obj.bufferedFluidAmount=0
    for i=1,#e.fluidbox do
      local f=e.fluidbox[i]
      local box=e.fluidbox.get_prototype(i)
      if f and f.name==item and (box.production_type==role or box.production_type=="input-output") then
        obj.bufferedFluidAmount=obj.bufferedFluidAmount+f.amount
      end
    end
  end
  return obj
end
local entities=surface.find_entities_filtered{force=force,area=area,type={"assembling-machine","furnace","rocket-silo","mining-drill"}}
for _,e in pairs(entities) do
  if e.type=="mining-drill" then
    local target=e.mining_target
    if target and target.valid then
      local m=target.prototype.mineable_properties
      local rate,reason
      if target.prototype.infinite_resource then reason="INFINITE_RESOURCE_YIELD_DEPENDS_ON_RICHNESS"
      elseif m.mining_time and m.mining_time>0 then rate=e.prototype.mining_speed*(1+e.speed_bonus)/m.mining_time
      else reason="MINING_TIME_UNAVAILABLE" end
      local output=0; local kind
      for _,p in pairs(m.products or {}) do if p.name==item then output=output+yield(p); kind=p.type end end
      if kind then producers[#producers+1]=endpoint(e,kind,"output",rate and rate*output,reason,nil,nil,target.name) end
      if m.required_fluid==item then
        consumers[#consumers+1]=endpoint(e,"fluid","input",rate and rate*(m.fluid_amount or 0)/10,reason,nil,nil,target.name)
      end
    end
  else
    local recipe,quality=e.get_recipe()
    if recipe then
      local rate,reason
      if recipe.energy>0 then rate=e.crafting_speed/recipe.energy else reason="RECIPE_ENERGY_NOT_POSITIVE" end
      local output,input_amount,output_kind,input_kind=0,0,nil,nil
      for _,p in pairs(recipe.products) do if p.name==item then output=output+yield(p); output_kind=p.type end end
      for _,p in pairs(recipe.ingredients) do if p.name==item then input_amount=input_amount+p.amount; input_kind=p.type end end
      if output_kind then producers[#producers+1]=endpoint(e,output_kind,"output",rate and rate*output,reason,recipe.name,quality and quality.name) end
      if input_kind then consumers[#consumers+1]=endpoint(e,input_kind,"input",rate and rate*input_amount,reason,recipe.name,quality and quality.name) end
    end
  end
end
local function less(a,b)
  if a.name~=b.name then return a.name<b.name end
  if a.position.x~=b.position.x then return a.position.x<b.position.x end
  if a.position.y~=b.position.y then return a.position.y<b.position.y end
  return (a.unitNumber or 0)<(b.unitNumber or 0)
end
table.sort(producers,less); table.sort(consumers,less)
local function page(list) local result={}; for i=offset+1,math.min(#list,offset+limit) do result[#result+1]=list[i] end; return result end
rcon.print(helpers.table_to_json{tick=game.tick,force=force.name,surface=surface.name,item=item,
  producerPageInfo={total=#producers,limit=limit,offset=offset,hasMore=#producers>offset+limit},
  consumerPageInfo={total=#consumers,limit=limit,offset=offset,hasMore=#consumers>offset+limit},
  producers=page(producers),consumers=page(consumers),
  rateAssumptions="Theoretical expected base yields at current crafting/mining speed; excludes productivity, quality upgrades, downtime, power/fuel/ingredient shortages, output blocking and depletion. Not actual throughput. Mining uses current target only; fluid mining input uses engine fluid_amount / 10."})
`, luaString(input.Force), luaString(input.Surface), luaString(input.Item), area, input.Limit, input.Offset)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	return factoryDecode[ItemFlow](response)
}

// planningAmountLimit bounds both user quantities and intermediate arithmetic.
const planningAmountLimit = 1e15

type planningRef struct {
	Item         string `json:"item"`
	MaterialType string `json:"materialType"`
}

func (r planningRef) key() string { return r.MaterialType + "\x00" + r.Item }

type planningQuantity struct {
	planningRef
	Amount                 float64 `json:"amount"`
	Probabilistic          bool    `json:"probabilistic"`
	TemperatureConstrained bool    `json:"temperatureConstrained"`
}

type planningRecipe struct {
	Name        string             `json:"name"`
	Ingredients []planningQuantity `json:"bomIngredients"`
	Products    []planningQuantity `json:"bomProducts"`
}

type planningStock struct {
	planningRef
	Quality string  `json:"quality"`
	Amount  float64 `json:"amount"`
}

type planningSnapshot struct {
	Tick                     float64          `json:"tick"`
	Force                    string           `json:"force"`
	Surface                  string           `json:"surface"`
	Recipes                  []planningRecipe `json:"bomRecipes"`
	Stock                    []planningStock  `json:"bomStock"`
	Mined                    []planningRef    `json:"bomMined"`
	StockTruncated           bool             `json:"stockTruncated"`
	RecipeCatalogueTruncated bool             `json:"recipeCatalogueTruncated"`
}

type planningMaterial struct {
	planningRef
	Quality        string  `json:"quality"`
	Required       float64 `json:"required"`
	AvailableStock float64 `json:"availableStock"`
	AllocatedStock float64 `json:"allocatedStock"`
	MissingStock   float64 `json:"missingStock"`
	ExpectedYield  float64 `json:"expectedYield"`
	Surplus        float64 `json:"surplus"`
}

type planningStep struct {
	Recipe string `json:"recipe"`
	planningRef
	Quality        string   `json:"quality"`
	Crafts         float64  `json:"crafts"`
	ExpectedOutput float64  `json:"expectedOutput"`
	Alternatives   []string `json:"alternatives"`
	Overridden     bool     `json:"overridden"`
}

type planningRequirement struct {
	planningRef
	Quality string  `json:"quality"`
	Amount  float64 `json:"amount"`
	Reason  string  `json:"reason"`
}

type planningResult struct {
	Tick                     float64                `json:"tick"`
	Force                    string                 `json:"force"`
	Surface                  string                 `json:"surface"`
	Item                     string                 `json:"item"`
	Amount                   float64                `json:"amount"`
	Quality                  string                 `json:"quality"`
	StockSource              string                 `json:"stockSource"`
	Complete                 bool                   `json:"complete"`
	StockTruncated           bool                   `json:"stockTruncated"`
	RecipeCatalogueTruncated bool                   `json:"recipeCatalogueTruncated"`
	Probabilistic            bool                   `json:"probabilistic"`
	Expansions               int                    `json:"expansions"`
	Materials                []*planningMaterial    `json:"materials"`
	Steps                    []*planningStep        `json:"steps"`
	RawRequirements          []*planningRequirement `json:"rawRequirements"`
	SeedRequirements         []*planningRequirement `json:"seedRequirements"`
	Unresolved               []*planningRequirement `json:"unresolved"`
	Warnings                 []string               `json:"warnings"`
	Assumptions              string                 `json:"assumptions"`
}

type planningOptions struct {
	Item          string
	Amount        float64
	Quality       string
	StockSource   string
	MaxDepth      int
	MaxExpansions int
	Overrides     map[string]string
}

func validatePlanningOptions(options planningOptions) error {
	if options.Item == "" || options.Quality == "" {
		return errors.New("item and quality are required")
	}
	if math.IsNaN(options.Amount) || math.IsInf(options.Amount, 0) || options.Amount <= 0 || options.Amount > 1e12 {
		return errors.New("amount must be finite, positive and no greater than 1e12")
	}
	if options.MaxDepth < 1 || options.MaxDepth > 128 || options.MaxExpansions < 1 || options.MaxExpansions > 10000 {
		return errors.New("maxDepth must be 1..128 and maxExpansions 1..10000")
	}
	return nil
}

// planningNet cancels returned ingredients before expanding a recipe. Returned
// catalysts need one craft's seed, not a seed multiplied by the batch size.
func planningNet(recipe *planningRecipe) (inputs, outputs, seeds []planningQuantity) {
	in, out := map[string]planningQuantity{}, map[string]planningQuantity{}
	for _, q := range recipe.Ingredients {
		prior := in[q.key()]
		prior.planningRef = q.planningRef
		prior.Amount += q.Amount
		prior.TemperatureConstrained = prior.TemperatureConstrained || q.TemperatureConstrained
		in[q.key()] = prior
	}
	for _, q := range recipe.Products {
		prior := out[q.key()]
		prior.planningRef = q.planningRef
		prior.Amount += q.Amount
		prior.Probabilistic = prior.Probabilistic || q.Probabilistic
		out[q.key()] = prior
	}
	for key, q := range in {
		p, ok := out[key]
		if ok {
			cancel := math.Min(q.Amount, p.Amount)
			if cancel > 0 {
				seed := q
				seed.Amount = cancel
				seeds = append(seeds, seed)
				q.Amount -= cancel
				p.Amount -= cancel
				out[key] = p
			}
		}
		if q.Amount > 0 {
			inputs = append(inputs, q)
		}
	}
	for _, p := range out {
		if p.Amount > 0 {
			outputs = append(outputs, p)
		}
	}
	less := func(list []planningQuantity) {
		sort.Slice(list, func(i, j int) bool { return list[i].key() < list[j].key() })
	}
	less(inputs)
	less(outputs)
	less(seeds)
	return
}

// planBillOfMaterials allocates each physical stock unit once. A single global
// surplus pool shares intermediates and coproducts across deterministic DFS.
// Missing leaves describe hypothetical supply; outputs are never called craftable.
func planBillOfMaterials(snapshot planningSnapshot, options planningOptions) (*planningResult, error) {
	if err := validatePlanningOptions(options); err != nil {
		return nil, err
	}
	result := &planningResult{Tick: snapshot.Tick, Force: snapshot.Force, Surface: snapshot.Surface,
		Item: options.Item, Amount: options.Amount, Quality: options.Quality, StockSource: options.StockSource,
		Complete:       !snapshot.StockTruncated && !snapshot.RecipeCatalogueTruncated,
		StockTruncated: snapshot.StockTruncated, RecipeCatalogueTruncated: snapshot.RecipeCatalogueTruncated,
		Materials: []*planningMaterial{}, Steps: []*planningStep{}, RawRequirements: []*planningRequirement{},
		SeedRequirements: []*planningRequirement{}, Unresolved: []*planningRequirement{}, Warnings: []string{},
		Assumptions: "Enabled recipes only; exact-name recipe preferred, then lexicographic name. Deterministic depth-first allocation, stock and coproduct surplus shared once; not a globally optimal recipe/stock schedule. Integer crafts, expected base yields, no productivity or quality upgrades. Item ingredients/products use requested quality; fluids use normal. Raw resources are not mined by this plan. No machine, energy, transport, temperature feasibility or immediate craftability claim. Factory stock excludes players, ground items, fuel, installed modules and equipment; player stock is main inventory only. Returned catalysts require initial seed stock and are not consumed twice."}
	if snapshot.StockTruncated {
		result.Warnings = append(result.Warnings, "Factory stock scan truncated; available stock is a lower bound.")
	}
	if snapshot.RecipeCatalogueTruncated {
		result.Warnings = append(result.Warnings, "Enabled recipe catalogue truncated; alternatives may be missing.")
	}
	materials := map[string]*planningMaterial{}
	remaining := map[string]float64{}
	pool := map[string]float64{}
	quality := func(ref planningRef) string {
		if ref.MaterialType == "fluid" {
			return "normal"
		}
		return options.Quality
	}
	material := func(ref planningRef) *planningMaterial {
		m := materials[ref.key()]
		if m == nil {
			m = &planningMaterial{planningRef: ref, Quality: quality(ref)}
			materials[ref.key()] = m
		}
		return m
	}
	for _, stock := range snapshot.Stock {
		if stock.Quality != quality(stock.planningRef) {
			continue
		}
		if stock.Amount < 0 || math.IsNaN(stock.Amount) || math.IsInf(stock.Amount, 0) || stock.Amount > planningAmountLimit {
			return nil, errors.New("invalid stock amount in snapshot")
		}
		m := material(stock.planningRef)
		m.AvailableStock += stock.Amount
		remaining[stock.key()] += stock.Amount
	}
	recipes := map[string][]*planningRecipe{}
	byName := map[string]*planningRecipe{}
	for i := range snapshot.Recipes {
		recipe := &snapshot.Recipes[i]
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
	for item, name := range options.Overrides {
		recipe := byName[name]
		if recipe == nil {
			return nil, fmt.Errorf("override recipe %q for %q is not enabled or outside recipe snapshot", name, item)
		}
		_, outputs, _ := planningNet(recipe)
		found := false
		for _, p := range outputs {
			if p.Item == item {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("override recipe %q has no positive net yield of %q", name, item)
		}
	}
	mined := map[string]bool{}
	for _, ref := range snapshot.Mined {
		mined[ref.key()] = true
	}
	active := map[string]bool{}
	raw, unresolved, seeds := map[string]*planningRequirement{}, map[string]*planningRequirement{}, map[string]*planningRequirement{}
	addRequirement := func(dest map[string]*planningRequirement, ref planningRef, amount float64, reason string) {
		key := ref.key() + "\x00" + reason
		entry := dest[key]
		if entry == nil {
			entry = &planningRequirement{planningRef: ref, Quality: quality(ref), Reason: reason}
			dest[key] = entry
		}
		entry.Amount += amount
	}
	steps := map[string]*planningStep{}
	var demand func(planningRef, float64, int)
	demand = func(ref planningRef, amount float64, depth int) {
		m := material(ref)
		if math.IsNaN(amount) || math.IsInf(amount, 0) || amount > planningAmountLimit || m.Required > planningAmountLimit-amount {
			result.Complete = false
			addRequirement(unresolved, ref, 0, "NUMERIC_BOUND_EXCEEDED")
			return
		}
		m.Required += amount
		use := math.Min(amount, remaining[ref.key()])
		remaining[ref.key()] -= use
		m.AllocatedStock += use
		amount -= use
		use = math.Min(amount, pool[ref.key()])
		pool[ref.key()] -= use
		amount -= use
		if amount <= 1e-9 {
			return
		}
		if active[ref.key()] {
			result.Complete = false
			addRequirement(unresolved, ref, amount, "RECIPE_CYCLE")
			return
		}
		if depth > options.MaxDepth || result.Expansions >= options.MaxExpansions {
			result.Complete = false
			addRequirement(unresolved, ref, amount, "EXPANSION_LIMIT")
			return
		}
		result.Expansions++
		list := recipes[ref.key()]
		var chosen *planningRecipe
		if name, ok := options.Overrides[ref.Item]; ok {
			chosen = byName[name]
		} else {
			for _, recipe := range list {
				if recipe.Name == ref.Item {
					chosen = recipe
					break
				}
			}
			if chosen == nil && len(list) > 0 {
				chosen = list[0]
			}
		}
		if chosen == nil {
			reason := "NO_ENABLED_RECIPE"
			if mined[ref.key()] {
				reason = "MINED_RAW"
			} else if ref.MaterialType == "fluid" {
				reason = "RAW_FLUID"
			}
			addRequirement(raw, ref, amount, reason)
			if reason == "NO_ENABLED_RECIPE" {
				result.Complete = false
				addRequirement(unresolved, ref, amount, reason)
			}
			return
		}
		inputs, outputs, catalysts := planningNet(chosen)
		yield := 0.0
		for _, p := range outputs {
			if p.key() == ref.key() {
				yield = p.Amount
			}
		}
		if yield <= 0 || math.IsNaN(yield) || math.IsInf(yield, 0) {
			result.Complete = false
			addRequirement(unresolved, ref, amount, "INVALID_RECIPE_YIELD")
			return
		}
		crafts := math.Ceil(amount/yield - 1e-12)
		if crafts < 1 {
			crafts = 1
		}
		if crafts > planningAmountLimit {
			result.Complete = false
			addRequirement(unresolved, ref, amount, "NUMERIC_BOUND_EXCEEDED")
			return
		}
		for _, q := range append(append([]planningQuantity{}, inputs...), outputs...) {
			if q.Amount < 0 || math.IsNaN(q.Amount) || math.IsInf(q.Amount, 0) || q.Amount > planningAmountLimit/crafts {
				result.Complete = false
				addRequirement(unresolved, ref, amount, "NUMERIC_BOUND_EXCEEDED")
				return
			}
		}
		active[ref.key()] = true
		for _, q := range catalysts {
			key := q.key()
			prior := seeds[key]
			if prior == nil || prior.Amount < q.Amount {
				seeds[key] = &planningRequirement{planningRef: q.planningRef, Quality: quality(q.planningRef), Amount: q.Amount, Reason: "CATALYST_SEED"}
			}
			material(q.planningRef)
			if q.TemperatureConstrained {
				result.Complete = false
				addRequirement(unresolved, q.planningRef, q.Amount, "FLUID_TEMPERATURE_NOT_PLANNED")
			}
		}
		for _, q := range inputs {
			if q.TemperatureConstrained {
				result.Complete = false
				addRequirement(unresolved, q.planningRef, q.Amount*crafts, "FLUID_TEMPERATURE_NOT_PLANNED")
			}
			demand(q.planningRef, q.Amount*crafts, depth+1)
		}
		delete(active, ref.key())
		for _, q := range chosen.Products {
			if q.Probabilistic {
				result.Probabilistic = true
			}
		}
		for _, q := range outputs {
			pool[q.key()] += q.Amount * crafts
			material(q.planningRef).ExpectedYield += q.Amount * crafts
		}
		pool[ref.key()] -= amount
		stepKey := chosen.Name + "\x00" + ref.key()
		step := steps[stepKey]
		if step == nil {
			alternatives := make([]string, 0, len(list))
			for _, recipe := range list {
				alternatives = append(alternatives, recipe.Name)
			}
			_, overridden := options.Overrides[ref.Item]
			step = &planningStep{Recipe: chosen.Name, planningRef: ref, Quality: quality(ref), Alternatives: alternatives, Overridden: overridden}
			steps[stepKey] = step
			result.Steps = append(result.Steps, step)
		}
		step.Crafts += crafts
		step.ExpectedOutput += yield * crafts
	}
	root := planningRef{Item: options.Item, MaterialType: "item"}
	// The runtime rejects ambiguous item/fluid names; only the requested prototype
	// kind present in the coherent snapshot can determine a fluid root here.
	for _, stock := range snapshot.Stock {
		if stock.Item == options.Item && stock.MaterialType == "fluid" {
			root.MaterialType = "fluid"
			break
		}
	}
	if len(recipes[root.key()]) == 0 {
		if len(recipes[(planningRef{Item: options.Item, MaterialType: "fluid"}).key()]) > 0 || mined[(planningRef{Item: options.Item, MaterialType: "fluid"}).key()] {
			root.MaterialType = "fluid"
		}
	}
	demand(root, options.Amount, 0)
	for key, seed := range seeds {
		result.SeedRequirements = append(result.SeedRequirements, seed)
		available := material(seed.planningRef).AvailableStock
		if available+1e-9 < seed.Amount {
			result.Complete = false
			addRequirement(unresolved, seed.planningRef, seed.Amount-available, "CATALYST_SEED_MISSING")
		}
		_ = key
	}
	for key, m := range materials {
		if m.Required == 0 && m.ExpectedYield == 0 {
			continue
		}
		m.MissingStock = math.Max(0, m.Required-m.AllocatedStock)
		m.Surplus = math.Max(0, pool[key])
		result.Materials = append(result.Materials, m)
	}
	for _, entry := range raw {
		result.RawRequirements = append(result.RawRequirements, entry)
	}
	for _, entry := range unresolved {
		result.Unresolved = append(result.Unresolved, entry)
	}
	sort.Slice(result.Materials, func(i, j int) bool { return result.Materials[i].key() < result.Materials[j].key() })
	lessRequirement := func(list []*planningRequirement) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].key() != list[j].key() {
				return list[i].key() < list[j].key()
			}
			return list[i].Reason < list[j].Reason
		})
	}
	lessRequirement(result.RawRequirements)
	lessRequirement(result.Unresolved)
	lessRequirement(result.SeedRequirements)
	if result.Probabilistic {
		result.Warnings = append(result.Warnings, "Probabilistic recipe yields are expected values, not guaranteed quantities; catalyst returns may also fail.")
	}
	return result, nil
}

func billOfMaterialsQuery(r *Resolver, input BillOfMaterialsInput) (*BillOfMaterials, error) {
	if input.Force == "" || input.Surface == "" || input.Stock == nil {
		return nil, errors.New("force, surface and explicit stock source are required")
	}
	options := planningOptions{Item: input.Item, Amount: input.Amount, Quality: input.Quality, StockSource: string(input.Stock.Source), MaxDepth: input.MaxDepth, MaxExpansions: input.MaxExpansions, Overrides: map[string]string{}}
	if err := validatePlanningOptions(options); err != nil {
		return nil, err
	}
	for _, override := range input.RecipeOverrides {
		if override == nil || override.Item == "" || override.Recipe == "" {
			return nil, errors.New("recipe overrides require nonempty item and recipe")
		}
		if _, exists := options.Overrides[override.Item]; exists {
			return nil, fmt.Errorf("duplicate recipe override for %q", override.Item)
		}
		options.Overrides[override.Item] = override.Recipe
	}
	if len(options.Overrides) > 1000 {
		return nil, errors.New("at most 1000 recipe overrides allowed")
	}
	player := ""
	if input.Stock.Player != nil {
		player = *input.Stock.Player
	}
	if options.StockSource == "PLAYER" {
		if player == "" || input.Stock.Area != nil {
			return nil, errors.New("PLAYER stock requires player and forbids area")
		}
	} else if options.StockSource == "FACTORY" {
		if input.Stock.Player != nil {
			return nil, errors.New("FACTORY stock forbids player")
		}
	} else {
		return nil, errors.New("stock source must be FACTORY or PLAYER")
	}
	area, err := planningAreaLua(input.Stock.Area)
	if err != nil {
		return nil, err
	}
	lua := luaFail + fmt.Sprintf(`
local force=game.forces[%s]; if not force then fail("force not found"); return end
local surface=game.surfaces[%s]; if not surface then fail("surface not found"); return end
local item,quality,source,player_name,area=%s,%s,%s,%s,%s
if not prototypes.quality[quality] then fail("quality not found"); return end
if not prototypes.item[item] and not prototypes.fluid[item] then fail("item/fluid prototype not found"); return end
if prototypes.item[item] and prototypes.fluid[item] then fail("ambiguous item/fluid prototype name"); return end
local result={tick=game.tick,force=force.name,surface=surface.name,bomRecipes={},bomStock={},bomMined={},stockTruncated=false,recipeCatalogueTruncated=false}
local names={}; for name,recipe in pairs(force.recipes) do if recipe.enabled then names[#names+1]=name end end
table.sort(names)
if #names>5000 then result.recipeCatalogueTruncated=true end
for i=1,math.min(#names,5000) do
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
local mined={}
for _,prototype in pairs(prototypes.entity) do if prototype.type=="resource" then
  for _,p in pairs(prototype.mineable_properties.products or {}) do
    local key=p.type.."\0"..p.name
    if not mined[key] then mined[key]=true;result.bomMined[#result.bomMined+1]={item=p.name,materialType=p.type} end
  end
end end
local totals={}
local function record(name,kind,q,amount)
  if kind=="item" and q~=quality then return end
  local key=kind.."\0"..name
  local prior=totals[key]
  if not prior then prior={item=name,materialType=kind,quality=kind=="fluid" and "normal" or q,amount=0};totals[key]=prior end
  prior.amount=prior.amount+amount
end
local function contents(inv)
  if inv then for _,s in pairs(inv.get_contents()) do record(s.name,"item",s.quality,s.count) end end
end
if source=="PLAYER" then
  local p=game.get_player(player_name)
  if not p then fail("stock player not found");return end
  if p.force~=force or p.surface~=surface then fail("stock player must belong to requested force and surface");return end
  local inv=p.get_main_inventory();if not inv then fail("stock player has no main inventory");return end
  contents(inv)
else
  local entities=surface.find_entities_filtered{force=force,area=area}
  table.sort(entities,function(a,b)
    if a.position.x~=b.position.x then return a.position.x<b.position.x end
    if a.position.y~=b.position.y then return a.position.y<b.position.y end
    return (a.unit_number or 0)<(b.unit_number or 0)
  end)
  if #entities>10000 then result.stockTruncated=true end
  local belts={['transport-belt']=true,['underground-belt']=true,splitter=true,loader=true,['loader-1x1']=true,['linked-belt']=true}
  local lines,comparisons,belt_limit={},0,false
  for i=1,math.min(#entities,10000) do
    local e=entities[i];local seen={}
    local function inventory(inv)
      if inv then local index=inv.index;if index and seen[index] then return end;if index then seen[index]=true end;contents(inv) end
    end
    if e.type=="container" or e.type=="logistic-container" or e.type=="infinity-container" then inventory(e.get_inventory(defines.inventory.chest))
    elseif e.type=="assembling-machine" or e.type=="furnace" or e.type=="rocket-silo" then inventory(e.get_inventory(defines.inventory.crafter_input))
    elseif e.type=="lab" then inventory(e.get_inventory(defines.inventory.lab_input))
    elseif e.type=="cargo-wagon" then inventory(e.get_inventory(defines.inventory.cargo_wagon))
    elseif e.type=="car" then inventory(e.get_inventory(defines.inventory.car_trunk))
    elseif e.type=="spider-vehicle" then inventory(e.get_inventory(defines.inventory.spider_trunk)) end
    inventory(e.get_output_inventory())
    if e.type=="inserter" then local s=e.held_stack;if s and s.valid_for_read then record(s.name,"item",s.quality.name,s.count) end end
    if belts[e.type] and not belt_limit then
      for j=1,e.get_max_transport_line_index() do
        local line=e.get_transport_line(j);local duplicate=false
        for _,prior in ipairs(lines) do
          comparisons=comparisons+1
          if comparisons>1000000 then belt_limit=true;result.stockTruncated=true;break end
          if line.line_equals(prior) then duplicate=true;break end
        end
        if belt_limit then break end
        if not duplicate then lines[#lines+1]=line;contents(line) end
      end
    end
    for j=1,#e.fluidbox do local f=e.fluidbox[j];if f then record(f.name,"fluid","normal",f.amount) end end
  end
end
for _,entry in pairs(totals) do result.bomStock[#result.bomStock+1]=entry end
-- Preserve a zero-count root marker so even unstocked, unrecipeable fluids retain their kind.
if prototypes.fluid[item] then result.bomStock[#result.bomStock+1]={item=item,materialType="fluid",quality="normal",amount=0} end
rcon.print(helpers.table_to_json(result))
`, luaString(input.Force), luaString(input.Surface), luaString(input.Item), luaString(input.Quality), luaString(options.StockSource), luaString(player), area)
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	snapshot, err := factoryDecode[planningSnapshot](response)
	if err != nil {
		return nil, err
	}
	plan, err := planBillOfMaterials(*snapshot, options)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var result BillOfMaterials
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
