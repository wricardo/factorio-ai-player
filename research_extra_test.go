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
	for _, want := range []string{"f.research_queue={}", "pcall(function() f.research_queue", "f.current_research"} {
		if !strings.Contains(lua, want) {
			t.Fatalf("clear-queue Lua must contain %q", want)
		}
	}
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
	result, err := factoryDecode[ResearchState](`{"force":"player","technology":"automation","started":true,"current_research":"automation","research_queue":["logistics"]}`)
	if err != nil {
		t.Fatalf("factoryDecode: %v", err)
	}
	if result.CurrentResearch == nil || *result.CurrentResearch != "automation" {
		t.Fatalf("currentResearch = %v", result.CurrentResearch)
	}
	if len(result.ResearchQueue) != 1 || result.ResearchQueue[0] != "logistics" {
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
