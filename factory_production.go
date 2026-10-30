package main

import "fmt"

// factoryProduction reads item production statistics for one force and surface.
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
local stats=force.get_item_production_statistics(surface)
local precision=defines.flow_precision_index.%s
local names={}; for name in pairs(stats.input_counts) do names[name]=true end
for name in pairs(stats.output_counts) do names[name]=true end
local sorted={}; for name in pairs(names) do sorted[#sorted+1]=name end; table.sort(sorted)
local items={}
for _,name in ipairs(sorted) do
  items[#items+1]={item=name,
    produced=stats.get_flow_count{name=name,category="input",precision_index=precision,count=true},
    consumed=stats.get_flow_count{name=name,category="output",precision_index=precision,count=true},
    producedPerMinute=stats.get_flow_count{name=name,category="input",precision_index=precision},
    consumedPerMinute=stats.get_flow_count{name=name,category="output",precision_index=precision}}
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=surface_name,force=force_name,window=window,items=items}))`,
		luaString(surface), luaString(force), luaString(window), precision)
	response, _, err := executeLua(r.addr, r.password, lua)
	return response, err
}
