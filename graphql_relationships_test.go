package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The fake models distinct world state for each subject and rejects commands
// aimed at the wrong surface, position, filter, or actor.
func relationshipResponse(lua string) string {
	contains := func(s string) bool { return strings.Contains(lua, s) }
	alice := contains("game.get_player(" + luaString("Alice") + ")")
	bob := contains("game.get_player(" + luaString("Bob") + ")")
	character := contains("game.get_entity_by_unit_number(42)")
	if contains("game.connected_players") {
		return `{"tick":1,"players":[{"name":"Alice","surface":"vulcanus","position":{"x":12,"y":-4}}]}`
	}
	if contains("p.create_character()") {
		if alice {
			return `{"tick":9,"player":"Alice","character":77,"surface":"vulcanus","position":{"x":12,"y":-4}}`
		}
		return `{"error":"player not found"}`
	}
	if contains("s.find_entities_filtered(filter)") {
		if contains("game.surfaces["+luaString("vulcanus")+"]") && contains("local cx,cy=12,-4") && contains("radius=7") && contains("local name="+luaString("electric-mining-drill")) && contains("local kind="+luaString("mining-drill")) {
			return `{"surface":"vulcanus","entities":[{"name":"electric-mining-drill","type":"mining-drill","position":{"x":13.5,"y":-4.5}}],"truncated":false,"total":1}`
		}
		if contains("game.surfaces["+luaString("vulcanus")+"]") && contains("local cx,cy=12,-4") && contains("radius=10") {
			return `{"surface":"vulcanus","entities":{},"truncated":false,"total":0}`
		}
		return `{"error":"wrong nearby target"}`
	}
	if contains("local result={name=e.name,type=e.type") {
		if !contains("local surface_name=" + luaString("vulcanus")) {
			return `{"error":"wrong inspection surface"}`
		}
		if alice && contains("local target_position={x=13.5,y=-4.5}") && contains("name="+luaString("electric-mining-drill")) {
			return `{"name":"electric-mining-drill","surface":"vulcanus","recipe":"iron-gear-wheel","mining_target":{"name":"iron-ore","position":{"x":14,"y":-5},"amount":100},"mining_resources":[{"name":"copper-ore","position":{"x":15,"y":-5},"amount":50}]}`
		}
		if character && contains("local target_position={x=14,y=-5}") && contains("name="+luaString("iron-ore")) {
			return `{"name":"iron-ore","surface":"vulcanus"}`
		}
		if character && contains("local target_position={x=15,y=-5}") && contains("name="+luaString("copper-ore")) {
			return `{"name":"copper-ore","surface":"vulcanus"}`
		}
		return `{"error":"entity not found at center on actor's force"}`
	}
	if contains("local recipe=p.force.recipes[") {
		if bob && contains("p.force.recipes["+luaString("iron-gear-wheel")+"]") {
			return `{"tick":5,"player":"Bob","name":"iron-gear-wheel","enabled":true,"ingredients":[{"name":"iron-plate","amount":2}],"products":[{"name":"iron-gear-wheel","amount":1}]}`
		}
		if character && contains("p.force.recipes["+luaString("copper-cable")+"]") {
			return `{"tick":6,"character":42,"name":"copper-cable","enabled":false,"ingredients":{},"products":{}}`
		}
		return `{"error":"recipe not found"}`
	}
	if contains("local queue=p.crafting_queue") {
		if character {
			return `{"tick":3,"character":42,"queue":[{"recipe":"iron-gear-wheel","count":2}],"queue_entries":1}`
		}
		if alice {
			return `{"tick":3,"player":"Alice","queue":{},"queue_entries":0}`
		}
	}
	if contains("local inv=p.get_main_inventory()") {
		if character {
			return `{"tick":2,"character":42,"inventory":"main","items":[{"name":"iron-plate","count":9}],"item_entries":1}`
		}
		if alice {
			return `{"tick":2,"player":"Alice","inventory":"main","items":{},"item_entries":0}`
		}
	}
	if contains("subject.has_character=") {
		if character {
			return `{"tick":1,"character":42,"surface":"vulcanus","position":{"x":12,"y":-4},"has_character":true}`
		}
		if alice {
			return `{"tick":1,"player":"Alice","surface":"vulcanus","position":{"x":12,"y":-4},"has_character":true}`
		}
	}
	return `{"error":"unexpected command or actor"}`
}

func relationshipServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				id, _, _, err := readPacket(conn)
				if err == nil {
					err = writePacket(conn, id, 2, "")
				}
				var command string
				if err == nil {
					_, _, command, err = readPacket(conn)
				}
				if err == nil {
					// A single-packet command (game.print) never sends the completion marker.
					if _, _, _, markerErr := readPacket(conn); markerErr != nil {
						return
					}
					err = writePacket(conn, 2, 0, relationshipResponse(command)+"\n")
				}
				if err == nil {
					err = writePacket(conn, 3, 0, "factorio-ai-player-response-end\n")
				}
				if err != nil {
					t.Errorf("fake RCON: %v", err)
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; workers.Wait() })
	return listener.Addr().String()
}

func relationshipQuery(t *testing.T, addr, query string) (map[string]any, error) {
	t.Helper()
	return relationshipRun(t, addr, "query", query)
}

func relationshipMutation(t *testing.T, addr, mutation string) (map[string]any, error) {
	t.Helper()
	return relationshipRun(t, addr, "mutation", mutation)
}

func relationshipRun(t *testing.T, addr, command, document string) (map[string]any, error) {
	t.Helper()
	var out bytes.Buffer
	err := runWithCredentials([]string{"--addr", addr, command, "--format", "compact", document}, &out, "password")
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid GraphQL output %q: %v", out.String(), err)
	}
	return result, nil
}

func relationshipAssertJSON(t *testing.T, got map[string]any, expected string) {
	t.Helper()
	var want map[string]any
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestGraphQLNestedActorAndPlayer(t *testing.T) {
	addr := relationshipServer(t)
	query := `query {
	  player(input:{character:"42"}) {
	    character surface position { x y }
	    inventory { character items { name count } }
	    queue { character queue { recipe count recipeInfo(actor:{player:"Bob"}) { player name ingredients { name amount } } } }
	    recipe(name:"copper-cable") { character name enabled ingredients { name } products { name } }
	    nearby(radius:7,name:"electric-mining-drill",type:"mining-drill") {
	      surface entities { name surface position { x y }
	        inspection(actor:{player:"Alice"}) { name surface recipeInfo(actor:{player:"Bob"}) { player name }
	          miningTarget { name surface inspection(actor:{character:"42"}) { name surface } }
	          miningResources { name surface inspection(actor:{character:"42"}) { name surface } }
	        }
	      }
	    }
	  }
	  status { players { name actor { player inventory { player items { name } } queue { player queue { recipe } } nearby { surface entities { name } } } } }
	}`
	got, err := relationshipQuery(t, addr, query)
	if err != nil {
		t.Fatal(err)
	}
	relationshipAssertJSON(t, got, `{"data":{"player":{"character":"42","surface":"vulcanus","position":{"x":12,"y":-4},"inventory":{"character":"42","items":[{"name":"iron-plate","count":9}]},"queue":{"character":"42","queue":[{"recipe":"iron-gear-wheel","count":2,"recipeInfo":{"player":"Bob","name":"iron-gear-wheel","ingredients":[{"name":"iron-plate","amount":2}]}}]},"recipe":{"character":"42","name":"copper-cable","enabled":false,"ingredients":[],"products":[]},"nearby":{"surface":"vulcanus","entities":[{"name":"electric-mining-drill","surface":"vulcanus","position":{"x":13.5,"y":-4.5},"inspection":{"name":"electric-mining-drill","surface":"vulcanus","recipeInfo":{"player":"Bob","name":"iron-gear-wheel"},"miningTarget":{"name":"iron-ore","surface":"vulcanus","inspection":{"name":"iron-ore","surface":"vulcanus"}},"miningResources":[{"name":"copper-ore","surface":"vulcanus","inspection":{"name":"copper-ore","surface":"vulcanus"}}]}}]}},"status":{"players":[{"name":"Alice","actor":{"player":"Alice","inventory":{"player":"Alice","items":[]},"queue":{"player":"Alice","queue":[]},"nearby":{"surface":"vulcanus","entities":[]}}}]}}}`)
}

func TestGraphQLInspectionWithoutRecipe(t *testing.T) {
	addr := relationshipServer(t)
	got, err := relationshipQuery(t, addr, `query {
	  inspect(input:{character:"42",surface:"vulcanus",entity:"iron-ore",x:14,y:-5}) {
	    name recipeInfo(actor:{player:"Bob"}) { name }
	  }
	}`)
	if err != nil {
		t.Fatal(err)
	}
	relationshipAssertJSON(t, got, `{"data":{"inspect":{"name":"iron-ore","recipeInfo":null}}}`)
}

func TestGraphQLNestedRelationshipErrors(t *testing.T) {
	addr := relationshipServer(t)
	for _, tc := range []struct{ name, query, message string }{
		{"inspection failure", `query { player(input:{character:"42"}) { nearby(radius:7,name:"electric-mining-drill",type:"mining-drill") { entities { inspection(actor:{player:"Bob"}) { name } } } } }`, "entity not found"},
		{"recipe failure", `query { player(input:{character:"42"}) { queue { queue { recipeInfo(actor:{player:"Alice"}) { name } } } } }`, "recipe not found"},
		{"actor selection failure", `query { player(input:{character:"42"}) { queue { queue { recipeInfo(actor:{}) { name } } } } }`, "exactly one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := relationshipQuery(t, addr, tc.query)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("expected %q GraphQL error, got result %v, error %v", tc.message, got, err)
			}
			if !strings.Contains(fmt.Sprint(err), "GraphQL:") {
				t.Fatalf("expected GraphQL error, got %v", err)
			}
		})
	}
}

func TestGraphQLPlayerRestoreCharacter(t *testing.T) {
	addr := relationshipServer(t)
	got, err := relationshipMutation(t, addr, `mutation { playerRestoreCharacter(input:{player:"Alice"}) { character player surface position { x y } } }`)
	if err != nil {
		t.Fatal(err)
	}
	relationshipAssertJSON(t, got, `{"data":{"playerRestoreCharacter":{"character":"77","player":"Alice","surface":"vulcanus","position":{"x":12,"y":-4}}}}`)
}
