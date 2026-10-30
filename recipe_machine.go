package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

func machineRecipeLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("set-recipe", flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number (required)")
	entity := fs.String("entity", "", "Assembling machine entity name (required unless a unit number is given)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Assembling machine unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Assembling machine unit number, alternative to entity and x/y")
	x := fs.Float64("x", math.NaN(), "Entity center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required unless a unit number is given)")
	recipe := fs.String("recipe", "", "Enabled recipe name (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *character == 0 || *character > math.MaxUint32 || *recipe == "" {
		return "", errors.New("character and recipe are required")
	}
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	prefix := luaFail + fmt.Sprintf(`local c=game.get_entity_by_unit_number(%d); if not c or c.type~="character" then fail("character not found"); return end
local p=c
`, *character) + targetLocals(targetUnit, *entity, *x, *y, "") + "surface_name=c.surface.name\n" + luaEntityResolve + resolveTargetLua() + `if e.type~="assembling-machine" then fail("assembling machine not found"); return end
if not c.can_reach_entity(e) then fail("machine out of reach"); return end
`
	return prefix + fmt.Sprintf(`local recipe=c.force.recipes[%s]; if not recipe or not recipe.enabled then fail("recipe not found or not enabled"); return end
if not e.prototype.crafting_categories[recipe.category] then fail("machine cannot use recipe category"); return end
if e.is_crafting() then fail("machine has an active craft"); return end
for _,id in ipairs({defines.inventory.crafter_input,defines.inventory.crafter_output}) do local inv=e.get_inventory(id); if inv and not inv.is_empty() then fail("empty machine input and output before changing recipe"); return end end
for i=1,#e.fluidbox do if e.fluidbox[i] then fail("empty machine fluids before changing recipe"); return end end
local removed=e.set_recipe(recipe.name)
for _,stack in pairs(removed) do local count=c.get_main_inventory().insert(stack); if count<stack.count then stack.count=stack.count-count; c.surface.spill_item_stack{position=e.position,stack=stack,enable_looted=true,force=c.force} end end
rcon.print(helpers.table_to_json({entity=e.name,position=e.position,surface=s.name,recipe=recipe.name,returned=removed,unit_number=e.unit_number}))`, luaString(*recipe)), nil
}
