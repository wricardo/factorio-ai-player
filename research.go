package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// researchLua starts research on a force. The engine queues the technology
// when another one is already being researched.
func researchLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("start-research", flag.ContinueOnError)
	fs.SetOutput(out)
	force := fs.String("force", "player", "Force name (required)")
	technology := fs.String("technology", "", "Technology name (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *force == "" || *technology == "" {
		return "", errors.New("force and technology are required; no positional arguments allowed")
	}
	return fmt.Sprintf(luaFail+`local f=game.forces[%s]; if not f then fail("force not found"); return end
local t=f.technologies[%s]; if not t then fail("technology not found"); return end
if t.researched then fail("technology is already researched"); return end
if not t.enabled then fail("technology is not enabled"); return end
-- Trigger technologies (for example automation-science-pack) are never
-- researched; add_research returns false for them, so name the trigger instead
-- of reporting an opaque failure.
if t.prototype.research_trigger then
  fail("technology is a trigger technology ("..helpers.table_to_json(t.prototype.research_trigger)..") and cannot be researched; it unlocks when its trigger fires")
  return
end
for _,prerequisite in pairs(t.prerequisites) do if not prerequisite.researched then fail("technology prerequisite is not researched"); return end end
if not f.add_research(t) then fail("could not start research"); return end
local queue={}
for _,queued in pairs(f.research_queue) do queue[#queue+1]=queued.name end
local current=f.current_research and f.current_research.name or nil
rcon.print(helpers.table_to_json({force=f.name,technology=t.name,started=true,current_research=current,research_queue=queue}))`, luaString(*force), luaString(*technology)), nil
}

// researchCancelLua stops the force's current research. The engine also
// removes dependent technologies from the research queue.
func researchCancelLua(args []string, out io.Writer) (string, error) {
	fs := flag.NewFlagSet("cancel-research", flag.ContinueOnError)
	fs.SetOutput(out)
	force := fs.String("force", "player", "Force name (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 || *force == "" {
		return "", errors.New("force is required; no positional arguments allowed")
	}
	return fmt.Sprintf(luaFail+`local f=game.forces[%s]; if not f then fail("force not found"); return end
local current=f.current_research; if not current then fail("no research in progress"); return end
local canceled=current.name
f.cancel_current_research()
local queue={}
for _,queued in pairs(f.research_queue) do queue[#queue+1]=queued.name end
local next=f.current_research and f.current_research.name or nil
rcon.print(helpers.table_to_json({force=f.name,technology=canceled,started=false,current_research=next,research_queue=queue}))`, luaString(*force)), nil
}
