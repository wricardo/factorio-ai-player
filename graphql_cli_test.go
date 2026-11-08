package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestQueryRejectsMutation(t *testing.T) {
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"query", `mutation { shutdown { response } }`}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "mutation") {
		t.Fatalf("query must reject mutation, got %v", err)
	}
}

func TestGraphQLRejectsInvalidCraftCount(t *testing.T) {
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"mutation", `mutation($id: UnitNumber!) { craft(input: {actor:{character: $id}, recipe: "stone-furnace", count: 0}) { started } }`, "--variables", `{"id":"4294967295"}`}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("invalid count should be rejected before RCON, got %v", err)
	}
}

func TestDomainScalarsRejectInvalidLiteralsAndVariablesBeforeRCON(t *testing.T) {
	tests := []struct {
		name      string
		document  string
		variables string
		want      string
	}{
		{"unit literal zero", `mutation { craft(input:{actor:{character:"0"},recipe:"iron-plate"}) { started } }`, "", "UnitNumber"},
		{"unit variable text", `mutation($id:UnitNumber!){ craft(input:{actor:{character:$id},recipe:"iron-plate"}) { started } }`, `{"id":"abc"}`, "UnitNumber"},
		{"finite literal nonnumber", `mutation { teleport(input:{actor:{character:"7"},position:{x:NaN,y:0}}) { tick } }`, "", "FiniteFloat"},
		{"finite variable nonnumber", `mutation($x:FiniteFloat!){ teleport(input:{actor:{character:"7"},position:{x:$x,y:0}}) { tick } }`, `{"x":"NaN"}`, "FiniteFloat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"mutation", tc.document}
			if tc.variables != "" {
				args = append(args, "--variables", tc.variables)
			}
			var out bytes.Buffer
			err := runGraphQLInProcess(args, &out, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q coercion failure", err, tc.want)
			}
		})
	}
}

func TestCraftWaitTimeoutBoundsBeforeRCON(t *testing.T) {
	var out bytes.Buffer
	mutation := `mutation { craft(input:{actor:{character:"7"},recipe:"iron-plate",waitTimeoutSeconds:0}) { started } }`
	err := runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "waitTimeoutSeconds") {
		t.Errorf("expected timeout validation for 0, got %v", err)
	}

	// Values above the 10 second cap are clamped, not rejected.
	out.Reset()
	mutation = `mutation { craft(input:{actor:{character:"7"},recipe:"iron-plate",waitTimeoutSeconds:3601}) { started } }`
	err = runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
	if err != nil && strings.Contains(err.Error(), "waitTimeoutSeconds") {
		t.Errorf("3601 should be clamped rather than rejected, got %v", err)
	}
}

func TestCraftWaitOptionsDefaultToImmediate(t *testing.T) {
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"mutation", `mutation { craft(input:{actor:{character:"7"},recipe:"iron-plate"}) { started } }`}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "FACTORIO_RCON_PASSWORD is required") {
		t.Fatalf("default craft options should proceed without waiting, got %v", err)
	}
}

func TestGraphQLRejectsEmptyResearchTechnology(t *testing.T) {
	var out bytes.Buffer
	err := runGraphQLInProcess([]string{"mutation", `mutation { startResearch(input: {technology: ""}) { started } }`}, &out, "")
	if err == nil || !strings.Contains(err.Error(), "technology") {
		t.Fatalf("empty technology should be rejected before RCON, got %v", err)
	}
}

func TestClearAreaRejectsInvalidBoundsBeforeRCON(t *testing.T) {
	tests := []string{
		`mutation { clearArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:1025,rightBottomY:1}}) { treesRemoved } }`,
		`mutation { clearArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:0,rightBottomY:1}}) { treesRemoved } }`,
		`mutation { clearArea(input:{area:{leftTopX:2,leftTopY:0,rightBottomX:1,rightBottomY:1}}) { treesRemoved } }`,
	}
	for _, mutation := range tests {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "area") {
			t.Errorf("expected invalid-area error for %s, got %v", mutation, err)
		}
	}
}

func TestBatchAreaMutationsValidateBoundsBeforeRCON(t *testing.T) {
	invalid := []string{
		`mutation { harvestArea(input:{actor:{character:"7"},area:{leftTopX:0,leftTopY:0,rightBottomX:1025,rightBottomY:1}}) { treesMined } }`,
		`mutation { pickupArea(input:{actor:{character:"7"},area:{leftTopX:0,leftTopY:0,rightBottomX:0,rightBottomY:1}}) { stacksFound } }`,
	}
	for _, mutation := range invalid {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "area") {
			t.Errorf("expected invalid-area error for %s, got %v", mutation, err)
		}
	}

	valid := []string{
		`mutation { harvestArea(input:{actor:{character:"7"},area:{leftTopX:0,leftTopY:0,rightBottomX:256,rightBottomY:256}}) { treesMined } }`,
		`mutation { pickupArea(input:{actor:{character:"7"},area:{leftTopX:0,leftTopY:0,rightBottomX:256,rightBottomY:256}}) { stacksFound } }`,
	}
	for _, mutation := range valid {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "FACTORIO_RCON_PASSWORD is required") {
			t.Errorf("expected valid bounds to reach RCON credentials check for %s, got %v", mutation, err)
		}
	}
}

func TestMachineQueryRejectsInvalidBounds(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{`{ machines(input:{limit:0}) { pageInfo { total } } }`, "limit"},
		{`{ machines(input:{offset:-1}) { pageInfo { total } } }`, "offset"},
		{`{ machines(input:{area:{leftTopX:2,leftTopY:0,rightBottomX:1,rightBottomY:1}}) { pageInfo { total } } }`, "area"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"query", tt.query}, &out, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: expected %s error, got %v", tt.query, tt.want, err)
		}
	}
}

var obsoleteCommands = []string{
	"status", "character-create", "character-return", "player",
	"inventory", "queue", "recipe", "mine", "pickup", "rotate", "belt-take",
	"inspect", "transfer", "set-recipe", "craft", "place", "fuel", "save",
	"shutdown", "nearby",
}

func TestObsoleteCommandsRejected(t *testing.T) {
	for _, command := range obsoleteCommands {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			err := runWithCredentials([]string{"--addr", "127.0.0.1:0", command}, &out, "password")
			want := fmt.Sprintf("unknown command %q", command)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %q, got %v", want, err)
			}
		})
	}
}

func TestHelpShowsOnlyGraphQLCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			var out bytes.Buffer
			if err := runWithCredentials(args, &out, ""); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			parts := strings.SplitN(text, "Commands:\n", 2)
			if len(parts) != 2 {
				t.Fatal("help omits command list")
			}
			commands := strings.SplitN(parts[1], "\n\n", 2)[0]
			for _, command := range obsoleteCommands {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(command) + `\b`).MatchString(commands) {
					t.Errorf("help exposes obsolete command %q", command)
				}
			}
			for _, command := range []string{"serve", "graph", "factorioform", "help"} {
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(command) + `\b`).MatchString(text) {
					t.Errorf("help omits command %q", command)
				}
			}
			if !strings.Contains(text, "--addr") {
				t.Error("help omits global --addr")
			}
		})
	}
}

func TestObservationQueriesRejectInvalidBounds(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{`{ stock(input:{limit:0}) { locationPageInfo { total } } }`, "limit"},
		{`{ electricNetworks(input:{offset:-1}) { pageInfo { total } } }`, "offset"},
		{`{ worldResources(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:1025,rightBottomY:1}}) { pageInfo { total } } }`, "area"},
		{`{ safety(input:{enemyLimit:10001}) { tick } }`, "limit"},
		{`{ itemRecipes(input:{item:"iron-plate",offset:-1}) { pageInfo { total } } }`, "offset"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"query", tt.query}, &out, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: expected %s error, got %v", tt.query, tt.want, err)
		}
	}
}

func TestMutationNotificationOptionsAreAccepted(t *testing.T) {
	mutations := []string{
		`mutation { pickup(input:{actor:{character:"7"},position:{x:0,y:0},message:"took item"}) { moved } }`,
		`mutation { clearArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:1,rightBottomY:1}}) { treesRemoved } }`,
	}
	for _, mutation := range mutations {
		var out bytes.Buffer
		err := runGraphQLInProcess([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "FACTORIO_RCON_PASSWORD is required") {
			t.Errorf("notification options must be resolved before the game backend: %s, got %v", mutation, err)
		}
	}
}

func TestAnnounceMutationIsExposed(t *testing.T) {
	var out bytes.Buffer
	if err := runGraphQLInProcess([]string{"query", `{ __type(name:"Mutation") { fields { name } } }`}, &out, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"announce"`) {
		t.Errorf("Mutation type must list announce, got %q", out.String())
	}
}
