package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCraftPlanRejectsInvalidInputBeforeRCON(t *testing.T) {
	tests := []struct {
		name     string
		op       string
		document string
		want     string
	}{
		{"query zero count", "query", `{ craftPlan(input:{actor:{player:"Alice"},recipe:"inserter",count:0}) { complete } }`, "count"},
		{"query fractional count", "query", `{ craftPlan(input:{actor:{player:"Alice"},recipe:"inserter",count:1.5}) { complete } }`, "count"},
		{"query count above uint32", "query", `{ craftPlan(input:{actor:{player:"Alice"},recipe:"inserter",count:4294967296}) { complete } }`, "count"},
		{"query empty recipe", "query", `{ craftPlan(input:{actor:{player:"Alice"},recipe:"  "}) { complete } }`, "recipe"},
		{"query both actors", "query", `{ craftPlan(input:{actor:{player:"Alice",character:"7"},recipe:"inserter"}) { complete } }`, "exactly one of player or character"},
		{"query no actor", "query", `{ craftPlan(input:{actor:{},recipe:"inserter"}) { complete } }`, "exactly one of player or character"},
		{"query character zero", "query", `{ craftPlan(input:{actor:{character:"0"},recipe:"inserter"}) { complete } }`, "UnitNumber"},
		{"mutation zero timeout", "mutation", `mutation { craft(input:{actor:{player:"Alice"},recipe:"inserter",expand:true,waitTimeoutSeconds:0}) { craftingComplete } }`, "waitTimeoutSeconds"},
		{"mutation unknown mode", "mutation", `mutation { craft(input:{actor:{player:"Alice"},recipe:"inserter",mode:SOMETIMES}) { started } }`, "CraftMode"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runGraphQLInProcess([]string{tc.op, tc.document}, &out, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// Valid input must reach the RCON credential check, not be rejected earlier.
func TestCraftPlanValidInputProceedsToRCON(t *testing.T) {
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"query", `{ craftPlan(input:{actor:{player:"Alice"},recipe:"inserter"}) { complete } }`}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "RCON") {
		t.Fatalf("valid craftPlan should proceed to RCON, got %v", err)
	}
}

// Queue attempts must be emitted one entry per pending step, with the actor
// prelude bound, so the engine sees the reduced counts from later rounds.
func TestCraftQueuePlanLuaEncodesAttempts(t *testing.T) {
	lua := craftQueuePlanLua(7, "", []craftPlanAttempt{{recipe: "iron-gear-wheel", count: 2}, {recipe: "transport-belt", count: 1}})
	for _, want := range []string{
		"local unit=7;",
		"{recipe=" + luaString("iron-gear-wheel") + ",count=2}",
		"{recipe=" + luaString("transport-belt") + ",count=1}",
		"begin_crafting",
		"queue_size=p.crafting_queue_size",
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("queue Lua missing %q\n%s", want, lua)
		}
	}
}
