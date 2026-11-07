package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"
)

type Resolver struct {
	addr              string
	password          string
	screenshotDir     string
	screenshotTimeout time.Duration
	screenshotPath    func() (string, error)
}

// mutationFields holds schema mutation field names as camelCase so backend
// command names, which are kebab-case, can be told apart from query commands.
var mutationFields = map[string]bool{}

// defaultSurface is the fallback surface for resolvers that have no surface
// context of their own (for example a PlacedEntity whose Lua payload omits it).
// It is configurable so multi-surface saves do not silently resolve on one
// surface: FACTORIO_DEFAULT_SURFACE overrides it, and the built-in default is
// only the engine's first surface renamed in the base game.
var defaultSurface = configuredDefaultSurface()

func configuredDefaultSurface() string {
	if surface := os.Getenv("FACTORIO_DEFAULT_SURFACE"); surface != "" {
		return surface
	}
	return "nauvis"
}

func newGraphQLSchema(addr, password string) graphql.ExecutableSchema {
	schema := NewExecutableSchema(Config{Resolvers: &Resolver{
		addr:          addr,
		password:      password,
		screenshotDir: configuredScreenshotDir(),
	}})
	if mutation := schema.Schema().Mutation; mutation != nil {
		for _, field := range mutation.Fields {
			mutationFields[field.Name] = true
		}
	}
	return schema
}

func configuredScreenshotDir() string {
	if dir := os.Getenv("FACTORIO_SCREENSHOT_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("factorio-client", "script-output")
	}
	return filepath.Join(home, "factorio-client", "script-output")
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
		result.MiningTarget.Surface = &result.Surface
	}
	for _, entity := range result.MiningResources {
		entity.Surface = &result.Surface
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
	production, err := factoryDecode[FactoryProduction](response)
	if err != nil {
		return nil, err
	}
	return filterProductionNames(production, input.ItemNames), nil
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

func recipeInfo(r *Resolver, name *string, actor *ActorInput, force string) (*Recipe, error) {
	if name == nil {
		return nil, nil
	}
	return command[Recipe](r, "recipe", RecipeInput{Actor: actor, Force: force, Recipe: *name})
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
	var actorIdentity *ActorIdentity
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
				if key == "message" {
					value := f.String()
					message = &value
					continue
				}
				if key == "actor" && f.Type() == reflect.TypeFor[ActorInput]() {
					actor := f.Interface().(ActorInput)
					actorIdentity = newActorIdentity(&actor)
				}
				var err error
				args, err = appendCommandArgument(args, key, f)
				if err != nil {
					return nil, nil, err
				}
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
	attachActorIdentity(&result, actorIdentity)
	return &result, message, nil
}

func newActorIdentity(actor *ActorInput) *ActorIdentity {
	if actor == nil {
		return nil
	}
	return &ActorIdentity{Player: actor.Player, Character: actor.Character}
}

func attachActorIdentity(result any, actor *ActorIdentity) {
	if result == nil {
		return
	}
	if actor == nil {
		switch value := result.(type) {
		case *ActionResult:
			if value.Player != nil || value.Character != nil {
				actor = &ActorIdentity{Player: value.Player, Character: value.Character}
			}
		case *CraftQueue:
			if value.Player != nil || value.Character != nil {
				actor = &ActorIdentity{Player: value.Player, Character: value.Character}
			}
		}
	}
	if actor == nil {
		return
	}
	switch value := result.(type) {
	case *ActionResult:
		value.Actor = actor
	case *ActorLogisticResult:
		value.Actor = actor
	case *BuildGhostsResult:
		value.Actor = actor
	case *CapsuleResult:
		value.Actor = actor
	case *ConnectEntitiesResult:
		value.Actor = actor
	case *CraftQueue:
		value.Actor = actor
	case *DropItemResult:
		value.Actor = actor
	case *EntityMetaResult:
		value.Actor = actor
	case *EquipmentGridResult:
		value.Actor = actor
	case *FluidWriteResult:
		value.Actor = actor
	case *FuelManyResult:
		value.Actor = actor
	case *HarvestAreaResult:
		value.Actor = actor
	case *PickupAreaResult:
		value.Actor = actor
	case *PlaceManyResult:
		value.Actor = actor
	case *PlaceNextToResult:
		value.Actor = actor
	case *PlaceTileResult:
		value.Actor = actor
	case *RemoveManyResult:
		value.Actor = actor
	case *RemoveResult:
		value.Actor = actor
	case *RepairResult:
		value.Actor = actor
	case *SettingsResult:
		value.Actor = actor
	case *ShootResult:
		value.Actor = actor
	case *TrainInfo:
		value.Actor = actor
	case *TransferManyResult:
		value.Actor = actor
	case *WireResult:
		value.Actor = actor
	}
}

func setActorResult[T any](result *T, actor *ActorInput, err error) (*T, error) {
	if err == nil {
		attachActorIdentity(result, newActorIdentity(actor))
	}
	return result, err
}
func appendCommandArgument(args []string, key string, value reflect.Value) ([]string, error) {
	if value.Kind() == reflect.Struct {
		switch key {
		case "actor", "position", "target":
			typ := value.Type()
			for i := range value.NumField() {
				field := value.Field(i)
				if field.Kind() == reflect.Pointer {
					if field.IsNil() {
						continue
					}
					field = field.Elem()
				}
				nestedKey := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
				var err error
				args, err = appendCommandArgument(args, nestedKey, field)
				if err != nil {
					return nil, err
				}
			}
			return args, nil
		}
	}

	var text string
	switch typed := value.Interface().(type) {
	case InventoryKind:
		text = inventoryKindKeys[typed.String()]
		if text == "" {
			text = strings.ToLower(typed.String())
		}
	case TransferDirection:
		switch typed {
		case TransferDirectionToActor:
			text = "to-player"
		case TransferDirectionToEntity:
			text = "to-entity"
		default:
			text = typed.String()
		}
	case EquipmentTransferDirection:
		switch typed {
		case EquipmentTransferDirectionToMain:
			text = "to-main"
		case EquipmentTransferDirectionToEquipment:
			text = "to-equipment"
		default:
			text = typed.String()
		}
	case UndergroundBeltType:
		text = strings.ToLower(typed.String())
	default:
		switch value.Kind() {
		case reflect.String:
			text = value.String()
		case reflect.Bool:
			text = strconv.FormatBool(value.Bool())
		case reflect.Int:
			text = strconv.FormatInt(value.Int(), 10)
		case reflect.Float64:
			text = strconv.FormatFloat(value.Float(), 'f', -1, 64)
		default:
			return nil, fmt.Errorf("unsupported %s argument", key)
		}
	}
	return append(args, "--"+key+"="+text), nil
}

var directionNames = map[string]int{
	"north": 0, "north_north_east": 1, "north_east": 2, "east_north_east": 3,
	"east": 4, "east_south_east": 5, "south_east": 6, "south_south_east": 7,
	"south": 8, "south_south_west": 9, "south_west": 10, "west_south_west": 11,
	"west": 12, "west_north_west": 13, "north_west": 14, "north_north_west": 15,
}

// directionValue accepts the engine's numeric direction or a DirectionString
// name. table_to_json may return either representation.
func directionValue(text string) (int, bool) {
	if value, err := strconv.Atoi(text); err == nil && value >= 0 && value <= 15 {
		return value, true
	}
	value, ok := directionNames[strings.ToLower(strings.ReplaceAll(text, "-", "_"))]
	return value, ok
}

// Factorio JSON uses snake_case, numeric unit IDs, and empty Lua tables for lists.
func normalizeGraphQL(value any, key, command string) any {
	switch v := value.(type) {
	case json.Number:
		if key == "character" || key == "unitNumber" || key == "relativeTo" || key == "pairedUnitNumber" {
			return v.String()
		}
		return v
	case string:
		// Factorio's table_to_json can encode defines.direction as a
		// DirectionString (for example "north") while the GraphQL schema
		// exposes direction fields as Int. Normalize those names back to the
		// engine's 0..15 values so ActionResult and Entity decode.
		if key == "direction" {
			if direction, ok := directionValue(v); ok {
				return json.Number(strconv.Itoa(direction))
			}
		}
		// WireColor is an uppercase GraphQL enum, but Lua reports the
		// defines.wire_color name in lowercase.
		if key == "color" {
			switch strings.ToLower(v) {
			case "red":
				return "RED"
			case "green":
				return "GREEN"
			}
		}
		return v
	case []any:
		// Factorio Vector is a two-element array; the schema exposes
		// inserterPickup/inserterDrop as Position objects.
		if (key == "inserterPickup" || key == "inserterDrop") && len(v) == 2 {
			return map[string]any{"x": v[0], "y": v[1]}
		}
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

// conveyorPlacementError keeps direct placement from bypassing the routed
// belt/pipe mutations. Underground halves, splitters and loaders stay allowed
// because the run mutations cannot place those one-tile forms directly.
func conveyorPlacementError(item string) error {
	name := strings.ToLower(item)
	switch {
	case strings.HasSuffix(name, "transport-belt"):
		return fmt.Errorf("%q must be placed with placeBelts(...), which routes the run and can tunnel under crossings instead of place/placeMany", item)
	case name == "pipe":
		return fmt.Errorf("%q must be placed with placePipe(...), which routes the run and can tunnel under crossings instead of place/placeMany", item)
	}
	return nil
}

func isListField(key string) bool {
	switch key {
	case "players", "entities", "items", "itemsPickedUp", "itemsRemaining", "queue", "ingredients", "products", "input", "output", "fuel", "chest", "modules", "turretAmmo", "main", "guns", "ammo", "armor", "trash", "fluids", "errors", "inserters", "belts", "returned", "miningResources", "transportLines", "machines", "summaries", "recipes", "craftingMachines", "researchQueue", "technologies", "prerequisites", "scienceIngredients", "effects", "machineFluids", "recipeIngredients", "locations", "totals", "networks", "powerEntities", "powerFluids", "logisticsEntities", "lanes", "worldInventories", "worldEntries", "nearbyDrills", "alerts", "queued", "remaining":
		return true
	case "nodes", "edges", "boundaryEdges", "sources", "sinks", "chests", "beltRuns", "pairs",
		"filterSlots", "filters", "producers", "consumers", "bufferedItems", "materials", "steps",
		"rawRequirements", "unresolved", "warnings", "alternatives", "bomRecipes", "bomStock",
		"bomMined", "bomIngredients", "bomProducts", "sciencePacks", "missingSciencePacks",
		"ghostEntries", "placementAlternatives", "requestedMaterials", "rootBoxIds",
		"fluidEntities", "fluidBoxes", "fluidConnections", "fluidSegments", "segmentFluids", "crossedItems", "characters", "blockedTiles", "blockers", "rows", "sites", "patches", "conflicts",
		"sections", "slots", "trains", "schedule", "contents", "grid", "signals", "craftingCategories", "resourceCategories",
		"conditions", "outputs", "killCounts", "missing", "created", "cargo", "equipmentGrid", "packs", "legend", "attention", "attentionCounts", "stockouts", "existing", "openTunnel", "results",
		"roboportRobot", "roboportMaterial", "cargoWagon", "carTrunk", "spiderTrunk", "artilleryWagonAmmo", "orders",
		"beltNeighbours", "fluidboxNeighbours", "produced", "consumed", "production", "seedRequirements", "platforms", "rocket",
		"shortfall", "surfaces", "tags", "theoreticalBaseProducts", "ends", "targets",
		"components", "stallEnds", "starveHeads", "ghosts", "blueprintBooks", "entries", "terrain",
		"starvedConsumers", "byCategory", "additionalCategories", "fuelItems",
		"compactEdges", "compactBoundaryEdges", "compactInserters", "compactNodes", "groundPositions",
		"members", "segments", "problems", "trainStops", "carriages", "waitConditions", "collidedItems", "groundItems",
		"entityCount", "defects", "counts", "mixedTiles", "feeders", "tiles", "fluidFilters", "fluidPorts", "labelCounts", "edgeTypes", "columns",
		"labels", "relationships", "properties", "paths", "origins", "kindCounts", "feedingArms", "gaps", "affected", "causes", "deficits", "limiting", "notes", "route", "routes", "stages", "boundary", "nodeIds", "kinds", "stretches", "left", "right", "findings", "related", "cells", "links", "statusCounts":
		return true
	}
	return false
}
