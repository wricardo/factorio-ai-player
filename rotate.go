package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
)

// rotateCommandInput is the backend flag payload for the rotate command. The
// actor is already resolved to a character unit number by rotateMutation.
type rotateCommandInput struct {
	Character  *string  `json:"character,omitempty"`
	Entity     *string  `json:"entity,omitempty"`
	X          *float64 `json:"x,omitempty"`
	Y          *float64 `json:"y,omitempty"`
	UnitNumber *string  `json:"unitNumber,omitempty"`
	Turns      *int     `json:"turns,omitempty"`
	Message    *string  `json:"message,omitempty"`
}

// rotateMutation resolves the acting character and rotates a reachable entity.
func rotateMutation(r *Resolver, input RotateInput) (*ActionResult, error) {
	character, err := resolveActorCharacterInput(r, input.Actor)
	if err != nil {
		return nil, err
	}
	_, entity, unitNumber, x, y := entityRefFields(input.Target)
	return command[ActionResult](r, "rotate", rotateCommandInput{
		Character:  &character,
		Entity:     entity,
		X:          x,
		Y:          y,
		UnitNumber: unitNumber,
		Turns:      input.Turns,
		Message:    input.Message,
	})
}

// rotateLua rotates an existing reachable entity using Factorio's player rotation.
func rotateLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(out)
	character := fs.Uint64("character", 0, "Character unit number (required)")
	entity := fs.String("entity", "", "Entity prototype name (required unless a unit number is given)")
	var unitNumber string
	fs.StringVar(&unitNumber, "unit-number", "", "Entity unit number, alternative to entity and x/y")
	fs.StringVar(&unitNumber, "unitNumber", "", "Entity unit number, alternative to entity and x/y")
	x := fs.Float64("x", math.NaN(), "Entity center X (required unless a unit number is given)")
	y := fs.Float64("y", math.NaN(), "Entity center Y (required unless a unit number is given)")
	turns := fs.Int("turns", 1, "Clockwise quarter turns (1-3)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *character == 0 || *character > math.MaxUint32 || *turns < 1 || *turns > 3 {
		return "", errors.New("character and turns between 1 and 3 are required")
	}
	targetUnit, err := entityTarget(*entity, unitNumber, *x, *y)
	if err != nil {
		return "", err
	}
	return luaFail + fmt.Sprintf(`local p=game.get_entity_by_unit_number(%d); if not p or p.type~="character" then fail("character not found"); return end
`, *character) + targetLocals(targetUnit, *entity, *x, *y, "") + `surface_name=p.surface.name
` + luaEntityResolve + resolveTargetLua() + fmt.Sprintf(`if %s then fail("entity is out of reach"); return end
local before=e.direction; local completed=0; for i=1,%d do if not e.rotate{} then fail("rotation failed after "..completed.." turns"); return end; completed=completed+1 end
rcon.print(helpers.table_to_json({name=e.name,position=e.position,surface=s.name,before=before,direction=e.direction,turns=completed,unit_number=e.unit_number}))`, reachViolation("p", "e"), *turns), nil
}
