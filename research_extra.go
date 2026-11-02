package main

import (
	"errors"
	"strconv"
	"strings"
)

// This file implements the extra research mutations: appending a technology to
// the force research queue, clearing the queue, and the explicit sandbox unlock
// of every technology. Every call reports the resulting force research state.

// researchStateLua appends the current research state from `f` into `state`.
// In 2.0 the first research_queue entry is the running technology, so the
// queue repeats current_research; tick is read in the same snapshot so callers
// can order the result against later research reads.
const researchStateLua = `
local research_queue={}
for _,queued in pairs(f.research_queue) do research_queue[#research_queue+1]=queued.name end
state.tick=game.tick
state.current_research=f.current_research and f.current_research.name or nil
state.research_queue=research_queue
`

// researchForceLua resolves a non-empty force name into `f`.
func researchForceLua(force string) string {
	return "local f=game.forces[" + luaString(force) + "]\nif not f then fail(\"force not found\"); return end\n"
}

// queueResearchLua validates the input and builds the queue mutation Lua.
func queueResearchLua(in QueueResearchInput) (string, error) {
	if strings.TrimSpace(in.Force) == "" {
		return "", errors.New("force must not be empty")
	}
	if strings.TrimSpace(in.Technology) == "" {
		return "", errors.New("technology must not be empty")
	}
	lua := luaFail + researchForceLua(in.Force) + "local t=f.technologies[" + luaString(in.Technology) + "]\n" +
		`if not t then fail("technology not found"); return end
if t.researched then fail("technology is already researched"); return end
if not t.enabled then fail("technology is not enabled"); return end
local queue_ok,queue_result=pcall(function() return f.add_research(t) end)
if not queue_ok then fail(tostring(queue_result)); return end
if not queue_result then
  local missing={}
  for _,prerequisite in pairs(t.prerequisites or {}) do
    if not prerequisite.researched then missing[#missing+1]=prerequisite.name end
  end
  table.sort(missing)
  if #missing>0 then fail("could not queue "..t.name..": unmet prerequisites "..table.concat(missing, ", ")); return end
  fail("could not queue "..t.name)
  return
end
local state={force=f.name,technology=t.name,started=true}
` + researchStateLua + `
rcon.print(helpers.table_to_json(state))
`
	return lua, nil
}

func queueResearch(r *Resolver, in QueueResearchInput) (*ResearchState, error) {
	lua, err := queueResearchLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ResearchState](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "queued research: "+result.Technology)
	return result, nil
}

// clearResearchQueueLua validates the input and builds the clear mutation Lua.
// In 2.0 writing an empty research_queue cancels the running technology, so the
// write keeps exactly that technology queued and reports the state read after
// the write instead of the state captured before it.
func clearResearchQueueLua(in ResearchQueueInput) (string, error) {
	if strings.TrimSpace(in.Force) == "" {
		return "", errors.New("force must not be empty")
	}
	lua := luaFail + researchForceLua(in.Force) + `local keep={}
if f.current_research then keep[1]=f.current_research.name end
local queue_ok,queue_error=pcall(function() f.research_queue=keep end)
if not queue_ok then fail("cannot clear the research queue: "..tostring(queue_error)); return end
local remaining=f.current_research
local state={force=f.name,technology=remaining and remaining.name or "",started=remaining~=nil}
` + researchStateLua + `
rcon.print(helpers.table_to_json(state))
`
	return lua, nil
}

func clearResearchQueue(r *Resolver, in ResearchQueueInput) (*ResearchState, error) {
	lua, err := clearResearchQueueLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ResearchState](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "cleared the research queue")
	return result, nil
}

// unlockAllTechnologiesLua validates the input and builds the unlock Lua.
func unlockAllTechnologiesLua(in UnlockTechnologiesInput) (string, error) {
	if strings.TrimSpace(in.Force) == "" {
		return "", errors.New("force must not be empty")
	}
	if in.Mode != TechnologyUnlockModeResearch && in.Mode != TechnologyUnlockModeEnable {
		return "", errors.New("mode must be RESEARCH or ENABLE")
	}
	call := "research_all_technologies"
	flag := "researched"
	if in.Mode == TechnologyUnlockModeEnable {
		call = "enable_all_technologies"
		flag = "enabled"
	}
	lua := luaFail + researchForceLua(in.Force) + "local unlock_ok,unlock_error=pcall(function() f." + call + "() end)\n" +
		`if not unlock_ok then fail("unlock failed: "..tostring(unlock_error)); return end
local unlocked=0
for _,technology in pairs(f.technologies) do if technology.` + flag + ` then unlocked=unlocked+1 end end
rcon.print(helpers.table_to_json({tick=game.tick,force=f.name,mode=` + luaString(string(in.Mode)) + `,technologies=unlocked}))
`
	return lua, nil
}

func unlockAllTechnologies(r *Resolver, in UnlockTechnologiesInput) (*ResearchUnlockResult, error) {
	lua, err := unlockAllTechnologiesLua(in)
	if err != nil {
		return nil, err
	}
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return nil, err
	}
	result, err := factoryDecode[ResearchUnlockResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, in.Message, "unlocked "+strconv.Itoa(result.Technologies)+" technologies")
	return result, nil
}
