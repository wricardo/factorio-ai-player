package main

import (
	"strings"
	"testing"
)

func TestQueueResearchValidation(t *testing.T) {
	if _, err := queueResearchLua(QueueResearchInput{Force: " ", Technology: "automation"}); err == nil {
		t.Fatal("an empty force must fail")
	}
	if _, err := queueResearchLua(QueueResearchInput{Force: "player", Technology: ""}); err == nil {
		t.Fatal("an empty technology must fail")
	}
	lua, err := queueResearchLua(QueueResearchInput{Force: "player", Technology: "automation"})
	if err != nil {
		t.Fatalf("queueResearchLua: %v", err)
	}
	for _, want := range []string{
		"f.add_research(t)", "f.technologies[" + luaString("automation") + "]",
		"if t.researched then fail", "if not t.enabled then fail",
		"f.research_queue", "started=true",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("queue Lua must contain %q", want)
		}
	}
}

func TestClearResearchQueueValidation(t *testing.T) {
	if _, err := clearResearchQueueLua(ResearchQueueInput{Force: ""}); err == nil {
		t.Fatal("an empty force must fail")
	}
	lua, err := clearResearchQueueLua(ResearchQueueInput{Force: "player"})
	if err != nil {
		t.Fatalf("clearResearchQueueLua: %v", err)
	}
	for _, want := range []string{"local keep={}", "keep[1]=f.current_research.name", "pcall(function() f.research_queue=keep end)", "local remaining=f.current_research"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("clear-queue Lua must contain %q", want)
		}
	}
	if strings.Contains(lua, "f.research_queue={}") {
		t.Fatal("an empty research_queue write cancels the running research in 2.0 and must not be used")
	}
}

// TestClearResearchQueueKeepsRunningTechnology runs the generated Lua against a
// stub force whose research_queue write is observable, proving the engine write
// keeps the running technology and that the reported state is the post-write
// one.
func TestClearResearchQueueKeepsRunningTechnology(t *testing.T) {
	lua, err := clearResearchQueueLua(ResearchQueueInput{Force: "player"})
	if err != nil {
		t.Fatalf("clearResearchQueueLua: %v", err)
	}
	stubs := `
local current={name="automation"}
-- The engine reads prototypes and accepts technology names on write, so the
-- stub converts a written name back into a prototype-shaped entry.
local backing={name="player",current_research=current,research_queue={current,{name="logistics"}}}
local f=setmetatable({},{__index=backing,__newindex=function(_,key,value)
  if key=="research_queue" then
    local converted={}
    for i,entry in ipairs(value) do converted[i]=type(entry)=="table" and entry or {name=entry} end
    backing.research_queue=converted
  else
    backing[key]=value
  end
end})
game.forces={player=f}
game.tick=4321
`
	assertions := `
assert(captured.technology=="automation","technology must be the still-running research, got "..tostring(captured.technology))
assert(captured.started==true,"research must still be running")
assert(captured.current_research=="automation","currentResearch must be read after the write")
assert(captured.tick==4321,"tick must be the snapshot tick")
assert(#captured.research_queue==1 and captured.research_queue[1]=="automation","the kept queue must hold exactly the running research, got "..tostring(captured.research_queue[1]))
assert(#backing.research_queue==1,"the engine write must not leave extra queue entries")
`
	runLuaFixture(t, lua, stubs, assertions)
}

// TestClearResearchQueueWithNoResearchWritesEmptyQueue proves the idle case
// writes an empty queue instead of concatenating a nil name.
func TestClearResearchQueueWithNoResearchWritesEmptyQueue(t *testing.T) {
	lua, err := clearResearchQueueLua(ResearchQueueInput{Force: "player"})
	if err != nil {
		t.Fatalf("clearResearchQueueLua: %v", err)
	}
	stubs := `
local backing={name="player",current_research=nil,research_queue={{name="logistics"}}}
local f=setmetatable({},{__index=backing,__newindex=function(_,key,value) backing[key]=value end})
game.forces={player=f}
game.tick=9
`
	assertions := `
assert(captured.started==false and captured.technology=="","idle force reports no running research")
assert(#backing.research_queue==0,"idle force keeps an empty queue")
`
	runLuaFixture(t, lua, stubs, assertions)
}

// TestQueueResearchReportsUnmetPrerequisites proves the add_research refusal
// names the prerequisites that are not researched instead of a bare message.
func TestQueueResearchReportsUnmetPrerequisites(t *testing.T) {
	lua, err := queueResearchLua(QueueResearchInput{Force: "player", Technology: "automation"})
	if err != nil {
		t.Fatalf("queueResearchLua: %v", err)
	}
	stubs := `
local technology={name="automation",researched=false,enabled=true,
  prerequisites={{name="logistics",researched=false},{name="electronics",researched=false},{name="automation-science-pack",researched=true}}}
local f={name="player",technologies={automation=technology},current_research=nil,research_queue={}}
f.add_research=function() return false end
game.forces={player=f}
game.tick=77
`
	assertions := `
assert(captured.error~=nil,"unqueueable technology must fail")
assert(string.find(captured.error,"unmet prerequisites",1,true),"error must explain unmet prerequisites: "..tostring(captured.error))
assert(string.find(captured.error,"electronics, logistics",1,true),"error must list sorted unmet prerequisites: "..tostring(captured.error))
assert(not string.find(captured.error,"automation-science-pack",1,true),"researched prerequisites must not be listed")
`
	runLuaFixture(t, lua, stubs, assertions)
}

func TestUnlockAllTechnologies(t *testing.T) {
	if _, err := unlockAllTechnologiesLua(UnlockTechnologiesInput{Force: ""}); err == nil {
		t.Fatal("an empty force must fail")
	}
	if _, err := unlockAllTechnologiesLua(UnlockTechnologiesInput{Force: "player", Mode: TechnologyUnlockMode("HALF")}); err == nil {
		t.Fatal("an unknown mode must fail")
	}
	lua, err := unlockAllTechnologiesLua(UnlockTechnologiesInput{Force: "player", Mode: TechnologyUnlockModeResearch})
	if err != nil {
		t.Fatalf("unlockAllTechnologiesLua(RESEARCH): %v", err)
	}
	for _, want := range []string{"f.research_all_technologies()", "technology.researched", "mode=" + luaString("RESEARCH")} {
		if !strings.Contains(lua, want) {
			t.Fatalf("RESEARCH Lua must contain %q", want)
		}
	}
	lua, err = unlockAllTechnologiesLua(UnlockTechnologiesInput{Force: "player", Mode: TechnologyUnlockModeEnable})
	if err != nil {
		t.Fatalf("unlockAllTechnologiesLua(ENABLE): %v", err)
	}
	for _, want := range []string{"f.enable_all_technologies()", "technology.enabled", "mode=" + luaString("ENABLE")} {
		if !strings.Contains(lua, want) {
			t.Fatalf("ENABLE Lua must contain %q", want)
		}
	}
	if strings.Contains(lua, "research_all_technologies") {
		t.Fatal("ENABLE must not research technologies")
	}
}

func TestResearchStateDecode(t *testing.T) {
	result, err := factoryDecode[ResearchState](`{"tick":55,"force":"player","technology":"automation","started":true,"current_research":"automation","research_queue":["automation","logistics"]}`)
	if err != nil {
		t.Fatalf("factoryDecode: %v", err)
	}
	if result.Tick != 55 {
		t.Fatalf("tick = %v", result.Tick)
	}
	if result.CurrentResearch == nil || *result.CurrentResearch != "automation" {
		t.Fatalf("currentResearch = %v", result.CurrentResearch)
	}
	// 2.0 keeps the running research as the first queue entry.
	if len(result.ResearchQueue) != 2 || result.ResearchQueue[0] != "automation" || result.ResearchQueue[1] != "logistics" {
		t.Fatalf("researchQueue = %v", result.ResearchQueue)
	}

	unlock, err := factoryDecode[ResearchUnlockResult](`{"tick":12,"force":"player","mode":"RESEARCH","technologies":7}`)
	if err != nil {
		t.Fatalf("factoryDecode unlock: %v", err)
	}
	if unlock.Technologies != 7 || unlock.Mode != "RESEARCH" {
		t.Fatalf("unlock result = %+v", unlock)
	}
}
