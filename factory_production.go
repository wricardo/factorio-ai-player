package main

import "fmt"

// factoryProduction reads item and fluid production statistics for one force and
// surface. Fluid rows use the same window and precision, so oil, steam and
// cracking bottlenecks are visible next to item flows.
func factoryProduction(r *Resolver, surface, force, window string) (string, error) {
	if surface == "" {
		return "", fmt.Errorf("surface is required")
	}
	if force == "" {
		return "", fmt.Errorf("force is required")
	}
	var precision string
	switch window {
	case "ONE_MINUTE":
		precision = "one_minute"
	case "TEN_MINUTES":
		precision = "ten_minutes"
	case "ONE_HOUR":
		precision = "one_hour"
	default:
		return "", fmt.Errorf("invalid production window %q", window)
	}

	lua := luaFail + fmt.Sprintf(`
local surface_name=%s; local force_name=%s; local window=%s
local surface=game.surfaces[surface_name]; if not surface then fail("surface not found: "..surface_name); return end
local force=game.forces[force_name]; if not force then fail("force not found: "..force_name); return end
local precision=defines.flow_precision_index.%s
local function flow(stats)
  local names={}; for name in pairs(stats.input_counts) do names[name]=true end
  for name in pairs(stats.output_counts) do names[name]=true end
  local sorted={}; for name in pairs(names) do sorted[#sorted+1]=name end; table.sort(sorted)
  local rows={}
  for _,name in ipairs(sorted) do
    rows[#rows+1]={item=name,
      produced=stats.get_flow_count{name=name,category="input",precision_index=precision,count=true},
      consumed=stats.get_flow_count{name=name,category="output",precision_index=precision,count=true},
      producedPerMinute=stats.get_flow_count{name=name,category="input",precision_index=precision},
      consumedPerMinute=stats.get_flow_count{name=name,category="output",precision_index=precision}}
  end
  return rows
end
local items=flow(force.get_item_production_statistics(surface))
local fluids=flow(force.get_fluid_production_statistics(surface))
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface_name,force=force_name,window=window,items=items,fluids=fluids}))`,
		luaString(surface), luaString(force), luaString(window), precision)
	response, _, err := executeLua(r.addr, r.password, lua)
	return response, err
}

// filterProductionNames keeps only the requested item and fluid rows. Filtering
// happens after decoding, so engine flow rates are untouched; a nil or empty
// filter keeps every row, and an unmatched name simply yields fewer rows.
func filterProductionNames(production *FactoryProduction, names []string) *FactoryProduction {
	if production == nil || len(names) == 0 {
		return production
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	keep := func(rows []*ProductionItem) []*ProductionItem {
		filtered := make([]*ProductionItem, 0, len(rows))
		for _, row := range rows {
			if row != nil && wanted[row.Item] {
				filtered = append(filtered, row)
			}
		}
		return filtered
	}
	production.Items = keep(production.Items)
	production.Fluids = keep(production.Fluids)
	return production
}
