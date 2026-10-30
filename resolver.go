package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/graphql"
)

type Resolver struct{ addr, password string }

// mutationFields holds schema mutation field names as camelCase so backend
// command names, which are kebab-case, can be told apart from query commands.
var mutationFields = map[string]bool{}

func newGraphQLSchema(addr, password string) graphql.ExecutableSchema {
	schema := NewExecutableSchema(Config{Resolvers: &Resolver{addr: addr, password: password}})
	if mutation := schema.Schema().Mutation; mutation != nil {
		for _, field := range mutation.Fields {
			mutationFields[field.Name] = true
		}
	}
	return schema
}

func isMutationCommand(name string) bool {
	var b strings.Builder
	upper := false
	for _, r := range name {
		if r == '-' {
			upper = true
			continue
		}
		if upper {
			b.WriteString(strings.ToUpper(string(r)))
			upper = false
			continue
		}
		b.WriteRune(r)
	}
	return mutationFields[b.String()]
}

func nearby(r *Resolver, input *NearbyInput) (*Nearby, error) {
	result, err := command[Nearby](r, "nearby", input)
	if err != nil {
		return nil, err
	}
	for _, entity := range result.Entities {
		entity.Surface = &result.Surface
	}
	return result, nil
}

func inspect(r *Resolver, input InspectInput) (*EntityInspection, error) {
	result, err := command[EntityInspection](r, "inspect", input)
	if err != nil {
		return nil, err
	}
	if result.MiningTarget != nil {
		result.MiningTarget.Surface = result.Surface
	}
	for _, entity := range result.MiningResources {
		entity.Surface = result.Surface
	}
	return result, nil
}
func factoryDecode[T any](response string) (*T, error) {
	var raw any
	dec := json.NewDecoder(strings.NewReader(response))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode factory response: %w", err)
	}
	data, err := json.Marshal(normalizeGraphQL(raw, "", "factory"))
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode factory response: %w", err)
	}
	return &result, nil
}

func factoryProductionQuery(r *Resolver, input *FactoryOverviewInput) (*FactoryProduction, error) {
	if input == nil {
		input = &FactoryOverviewInput{Surface: "nauvis", Force: "player", Window: ProductionWindowOneMinute}
	}
	response, err := factoryProduction(r, input.Surface, input.Force, string(input.Window))
	if err != nil {
		return nil, err
	}
	return factoryDecode[FactoryProduction](response)
}

func factoryMachinesQuery(r *Resolver, input *MachinesInput) (*MachinePage, error) {
	if input == nil {
		input = &MachinesInput{Surface: "nauvis", Force: "player", Limit: 100}
	}
	var status, entityType string
	if input.Status != nil {
		status = *input.Status
	}
	if input.Type != nil {
		entityType = *input.Type
	}
	var area *FactoryArea
	if input.Area != nil {
		area = &FactoryArea{LeftTopX: input.Area.LeftTopX, LeftTopY: input.Area.LeftTopY, RightBottomX: input.Area.RightBottomX, RightBottomY: input.Area.RightBottomY}
	}
	response, err := factoryMachines(r, input.Surface, input.Force, status, entityType, area, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	return factoryDecode[MachinePage](response)
}

func recipeInfo(r *Resolver, name *string, actor ActorInput) (*Recipe, error) {
	if name == nil {
		return nil, nil
	}
	return command[Recipe](r, "recipe", RecipeInput{Player: actor.Player, Character: actor.Character, Recipe: *name})
}

// command invokes the private game backend for GraphQL resolvers and announces mutations.
func command[T any](r *Resolver, name string, input any) (*T, error) {
	result, message, err := runCommand[T](r, name, input)
	if err != nil {
		return nil, err
	}
	if isMutationCommand(name) {
		announceMutation(r, message, mutationSummary(name, result))
	}
	return result, nil
}

// commandQuiet invokes the backend without announcing; callers announce the final result themselves.
func commandQuiet[T any](r *Resolver, name string, input any) (*T, error) {
	result, _, err := runCommand[T](r, name, input)
	return result, err
}

func runCommand[T any](r *Resolver, name string, input any) (*T, *string, error) {
	args := []string{name}
	var message *string
	if input != nil {
		v := reflect.ValueOf(input)
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v = reflect.Value{}
			} else {
				v = v.Elem()
			}
		}
		if v.IsValid() {
			typ := v.Type()
			for i := range v.NumField() {
				f := v.Field(i)
				if f.Kind() == reflect.Pointer {
					if f.IsNil() {
						continue
					}
					f = f.Elem()
				}
				key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				// Notification options are resolved here, not by the game backend.
				switch key {
				case "message":
					value := f.String()
					message = &value
					continue
				}
				var value string
				switch f.Kind() {
				case reflect.String:
					value = f.String()
				case reflect.Bool:
					value = strconv.FormatBool(f.Bool())
				case reflect.Int:
					value = strconv.FormatInt(f.Int(), 10)
				case reflect.Float64:
					value = strconv.FormatFloat(f.Float(), 'f', -1, 64)
				default:
					return nil, nil, fmt.Errorf("unsupported %s argument", key)
				}
				args = append(args, "--"+key+"="+value)
			}
		}
	}
	var out bytes.Buffer
	if err := executeGameOperation(args, &out, r.addr, r.password); err != nil {
		return nil, nil, err
	}
	if name == "save" || name == "shutdown" {
		result := any(&ServerResult{Response: out.String()})
		return result.(*T), message, nil
	}
	dec := json.NewDecoder(&out)
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("decode %s response: %w", name, err)
	}
	raw = normalizeGraphQL(raw, "", name)
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}
	var result T
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, nil, fmt.Errorf("decode %s response: %w", name, err)
	}
	return &result, message, nil
}

// Factorio JSON uses snake_case, numeric unit IDs, and empty Lua tables for lists.
func normalizeGraphQL(value any, key, command string) any {
	switch v := value.(type) {
	case json.Number:
		if key == "character" || key == "unitNumber" || (key == "direction" && (command == "place" || command == "rotate")) {
			return v.String()
		}
		return v
	case []any:
		if key == "inventories" && len(v) == 0 {
			return map[string]any{}
		}
		if key == "transportLines" {
			lines := make([]any, len(v))
			for i, line := range v {
				lines[i] = map[string]any{"items": normalizeGraphQL(line, "items", command)}
			}
			return lines
		}
		for i := range v {
			v[i] = normalizeGraphQL(v[i], "", command)
		}
		return v
	case map[string]any:
		if key == "produced" || key == "consumed" {
			names := make([]string, 0, len(v))
			for name := range v {
				names = append(names, name)
			}
			sort.Strings(names)
			counts := make([]any, 0, len(names))
			for _, name := range names {
				counts = append(counts, map[string]any{"name": name, "count": v[name]})
			}
			return counts
		}
		if isListField(key) && len(v) == 0 {
			return []any{}
		}
		result := make(map[string]any, len(v))
		for k, item := range v {
			parts := strings.Split(k, "_")
			for i := 1; i < len(parts); i++ {
				if len(parts[i]) > 0 {
					parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
				}
			}
			k = strings.Join(parts, "")
			result[k] = normalizeGraphQL(item, k, command)
		}
		return result
	default:
		return value
	}
}

func isListField(key string) bool {
	switch key {
	case "players", "entities", "items", "itemsPickedUp", "itemsRemaining", "queue", "ingredients", "products", "input", "output", "fuel", "chest", "modules", "turretAmmo", "main", "guns", "ammo", "armor", "trash", "fluids", "errors", "inserters", "belts", "returned", "miningResources", "transportLines", "machines", "summaries", "recipes", "craftingMachines", "researchQueue", "technologies", "prerequisites", "scienceIngredients", "effects", "machineFluids", "recipeIngredients", "locations", "totals", "networks", "powerEntities", "powerFluids", "logisticsEntities", "lanes", "worldInventories", "worldEntries", "nearbyDrills", "alerts":
		return true
	case "nodes", "edges", "boundaryEdges", "sources", "sinks", "chests", "beltRuns", "pairs",
		"filterSlots", "producers", "consumers", "bufferedItems", "materials", "steps",
		"rawRequirements", "unresolved", "warnings", "alternatives", "bomRecipes", "bomStock",
		"bomMined", "bomIngredients", "bomProducts", "sciencePacks", "missingSciencePacks",
		"ghostEntries", "placementAlternatives", "requestedMaterials", "rootBoxIds",
		"fluidEntities", "fluidBoxes", "fluidConnections", "fluidSegments", "segmentFluids", "crossedItems", "characters", "blockedTiles", "blockers", "rows", "sites", "patches",
		"sections", "slots":
		return true
	}
	return false
}
