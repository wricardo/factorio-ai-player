package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

type clearAreaArgs struct {
	Surface      string  `json:"surface"`
	LeftTopX     float64 `json:"left-top-x"`
	LeftTopY     float64 `json:"left-top-y"`
	RightBottomX float64 `json:"right-bottom-x"`
	RightBottomY float64 `json:"right-bottom-y"`
	Message      *string `json:"message,omitempty"`
}

func clearArea(r *Resolver, input ClearAreaInput) (*ClearAreaResult, error) {
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	return command[ClearAreaResult](r, "clear-area", clearAreaArgs{
		Surface:      input.Surface,
		LeftTopX:     input.Area.LeftTopX,
		LeftTopY:     input.Area.LeftTopY,
		RightBottomX: input.Area.RightBottomX,
		RightBottomY: input.Area.RightBottomY,
		Message:      input.Message,
	})
}

func clearAreaLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("clear-area", flag.ContinueOnError)
	fs.SetOutput(out)
	surface := fs.String("surface", "nauvis", "Surface name")
	areaFlags := addAreaBoundFlags(fs)
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *surface == "" {
		return "", errors.New("surface and bounded area are required")
	}
	bounds := areaFlags.bounds()
	if err := bounds.validate(); err != nil {
		return "", err
	}
	return luaFail + fmt.Sprintf(`local surface_name=%s; local area=%s
local s=game.surfaces[surface_name]; if not s then fail("surface not found"); return end
local left_top_x,left_top_y=area[1][1],area[1][2]; local right_bottom_x,right_bottom_y=area[2][1],area[2][2]; local trees,rocks=0,0
for _,e in pairs(s.find_entities_filtered{area=area,type={"tree","simple-entity"}}) do
  local p=e.position
  if p.x>=left_top_x and p.y>=left_top_y and p.x<=right_bottom_x and p.y<=right_bottom_y then
    if e.type=="tree" then
      if e.destroy{raise_destroy=true} then trees=trees+1 end
    elseif string.find(e.name,"rock",1,true) then
      if e.destroy{raise_destroy=true} then rocks=rocks+1 end
    end
  end
end
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,trees_removed=trees,rocks_removed=rocks}))`, luaString(*surface), bounds.lua()), nil
}
