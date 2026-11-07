package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// graph export reads every entity of one force into a snapshot directory that
// graph load writes to Memgraph. The world is partitioned into disjoint leaves
// covering every non-empty chunk, each read in one RCON call (one tick), and
// the result is reconciled against a census taken before and after the scan.

const (
	graphSnapshotNodes    = "nodes.jsonl"
	graphSnapshotEdges    = "edges.jsonl"
	graphSnapshotManifest = "manifest.json"
	graphLatestFile       = "LATEST"
	graphDefaultDir       = ".factorio/graph"
	graphChunkTiles       = 32
	graphRootChunks       = 8 // 256-tile root cells
	graphLeafPad          = 3.0
	graphFormatVersion    = 1
)

type graphExportOptions struct {
	Force            string
	Surfaces         []string
	Out              string
	LeafMax          int
	Concurrency      int
	ChunkPage        int
	IncludeLineLinks bool
	Pause            bool
	AllowIncomplete  bool
	Keep             int
}

type graphCensusRow struct {
	Surface string `json:"surface"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Mobile  int    `json:"mobile"`
	Skipped int    `json:"skipped"`
	Static  int    `json:"static"`
}

type graphCensus struct {
	Tick     float64          `json:"tick"`
	Force    string           `json:"force"`
	Surfaces []graphCensusRow `json:"surfaces"`
}

type graphChunk struct {
	X int `json:"x"`
	Y int `json:"y"`
	N int `json:"n"`
}

type graphChunkPage struct {
	Tick   float64      `json:"tick"`
	Next   int          `json:"next"`
	Done   bool         `json:"done"`
	Chunks []graphChunk `json:"chunks"`
}

// graphLeaf is a square of Size chunks with its top-left chunk at (CX, CY);
// Count is the census estimate used to size it.
type graphLeaf struct {
	CX, CY, Size, Count int
}

func (l graphLeaf) tiles() (lx, ly, rx, ry float64) {
	return float64(l.CX * graphChunkTiles), float64(l.CY * graphChunkTiles),
		float64((l.CX + l.Size) * graphChunkTiles), float64((l.CY + l.Size) * graphChunkTiles)
}

type graphCircuit struct {
	Connector string `json:"connector"`
	Color     string `json:"color"`
	Network   int64  `json:"network"`
}

type graphRawNode struct {
	ID              string         `json:"id"`
	Unit            *int64         `json:"unit"`
	Name            string         `json:"name"`
	Type            string         `json:"type"`
	Surface         string         `json:"surface"`
	Force           string         `json:"force"`
	X               float64        `json:"x"`
	Y               float64        `json:"y"`
	Direction       *int           `json:"direction"`
	Quality         string         `json:"quality"`
	Status          string         `json:"status"`
	Recipe          string         `json:"recipe"`
	RecipeQuality   string         `json:"recipeQuality"`
	RecipeSource    string         `json:"recipeSource"`
	GhostName       string         `json:"ghostName"`
	GhostType       string         `json:"ghostType"`
	ElectricNetwork *int64         `json:"electricNetwork"`
	LogisticNetwork *int64         `json:"logisticNetwork"`
	Train           *int64         `json:"train"`
	Modules         []string       `json:"modules"`
	Circuit         []graphCircuit `json:"circuit"`
	InputPriority   string         `json:"inputPriority"`
	OutputPriority  string         `json:"outputPriority"`
	Filters         []graphFilter  `json:"filters"`
	// Point-in-time contents sampled at the leaf tick; a *Count is present
	// whenever the entity has that inventory, the list only when it is non-empty.
	Fuel        []string `json:"fuel"`
	FuelCount   *int64   `json:"fuelCount"`
	Input       []string `json:"input"`
	InputCount  *int64   `json:"inputCount"`
	Output      []string `json:"output"`
	OutputCount *int64   `json:"outputCount"`
	Items       []string `json:"items"`
	ItemsCount  *int64   `json:"itemsCount"`
	Left        []string `json:"left"`
	LeftCount   *int64   `json:"leftCount"`
	Right       []string `json:"right"`
	RightCount  *int64   `json:"rightCount"`
	// Every engine transport line, one "index|lineName|side|item:count+..." string
	// per non-empty line, plus per-side purity flags.
	Lanes      []string `json:"lanes"`
	MixedLeft  *bool    `json:"mixedLeft"`
	MixedRight *bool    `json:"mixedRight"`
	// BeltStretch building needs the engine's straight/left/right belt shape.
	BeltShape string `json:"beltShape"`
	// Crafting counters: products_finished is cumulative, crafting_progress is
	// the in-flight fraction, previous_recipe flags a furnace that switched.
	ProductsFinished *int64   `json:"productsFinished"`
	CraftingProgress *float64 `json:"craftingProgress"`
	// CraftingSpeed is the machine's current speed after modules and effects, so
	// a rate can be recomputed from the recipe without re-reading the save.
	CraftingSpeed *float64 `json:"craftingSpeed"`
	// GhostRecipe is the recipe a ghost building would run (only on entity ghosts
	// of crafting machines); nothing exists to sample for a ghost, so this is the
	// whole build-plan fact the graph can hold.
	GhostRecipe string `json:"ghostRecipe"`
	// Fluids is one "index|name|amount|temperature" string per non-empty fluidbox,
	// fluidTotal sums them and fluidCapacity is the summed box capacity. Pipe
	// adjacency is FLUID_LINK; these are point-in-time contents.
	Fluids        []string `json:"fluids"`
	FluidTotal    *float64 `json:"fluidTotal"`
	FluidCapacity *float64 `json:"fluidCapacity"`
	// Requests are the logistic point filters of a requester chest or buffer
	// chest ("name:count", quality as name@quality), which no wiring edge holds.
	Requests []string `json:"requests"`
	// Ammo is a turret's ammunition inventory.
	Ammo      []string `json:"ammo"`
	AmmoCount *int64   `json:"ammoCount"`
	// LogisticMode is the engine's logistic mode of a logistic or infinity
	// container (requester, passive-provider, active-provider, buffer, storage);
	// it is the chest's role and picks its role label.
	LogisticMode string `json:"logisticMode"`
	// Robots is the roboport's robot inventory (logistic and construction robots).
	Robots         []string `json:"robots"`
	RobotsCount    *int64   `json:"robotsCount"`
	PreviousRecipe string   `json:"previousRecipe"`
	// Mining area composition: a drill over two ores puts a foreign item on the
	// belt it feeds even though mining_target names one tile.
	AreaResources []string `json:"areaResources"`
	AreaAmounts   []string `json:"areaAmounts"`
	AreaTiles     *int64   `json:"areaTiles"`
	MiningLeft    *float64 `json:"miningLeft"`
	MiningTop     *float64 `json:"miningTop"`
	MiningRight   *float64 `json:"miningRight"`
	MiningBottom  *float64 `json:"miningBottom"`
	// Inserter settings: lanes it may pick from, filter, stack override, the
	// held item, and whether its drop target resolved at all.
	PickupX         *float64 `json:"pickupX"`
	PickupY         *float64 `json:"pickupY"`
	DropX           *float64 `json:"dropX"`
	DropY           *float64 `json:"dropY"`
	PickupLeftLane  *bool    `json:"pickupLeftLane"`
	PickupRightLane *bool    `json:"pickupRightLane"`
	UseFilters      *bool    `json:"useFilters"`
	FilterMode      string   `json:"filterMode"`
	StackSizeOverr  *int64   `json:"stackSizeOverride"`
	Held            string   `json:"held"`
	DropResolved    *bool    `json:"dropResolved"`
}

// graphFilter is one splitter output filter, when the engine build exposes them.
type graphFilter struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Quality string `json:"quality"`
}

type graphRawEdge struct {
	Kind          string `json:"kind"`
	From          string `json:"from"`
	To            string `json:"to"`
	FlowDirection string `json:"flowDirection"`
	FromConnector string `json:"fromConnector"`
	ToConnector   string `json:"toConnector"`
	FromPort      string `json:"fromPort"`
	ToPort        string `json:"toPort"`
	FromPortIndex string `json:"fromPortIndex"`
	ToPortIndex   string `json:"toPortIndex"`
	Side          string `json:"side"`
	// Construction marks a LOGISTIC_COVERS link whose target also lies inside the
	// roboport's construction area.
	Construction *bool `json:"construction"`
}

type graphRef struct {
	ID      string  `json:"id"`
	Unit    *int64  `json:"unit"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Surface string  `json:"surface"`
	Force   string  `json:"force"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

type graphLeafPayload struct {
	Tick    float64        `json:"tick"`
	Scanned int            `json:"scanned"`
	Nodes   []graphRawNode `json:"nodes"`
	Edges   []graphRawEdge `json:"edges"`
	Refs    []graphRef     `json:"refs"`
}

// graphNodeOut and graphEdgeOut are the snapshot file rows graph load reads.
type graphNodeOut struct {
	ID     string         `json:"id"`
	Labels []string       `json:"labels"`
	Props  map[string]any `json:"props"`
}

type graphEdgeOut struct {
	Type      string         `json:"type"`
	From      string         `json:"from"`
	FromLabel string         `json:"fromLabel"`
	To        string         `json:"to"`
	ToLabel   string         `json:"toLabel"`
	Key       string         `json:"key"`
	Props     map[string]any `json:"props"`
}

type graphLeafReport struct {
	Area    [4]float64 `json:"area"`
	Tick    float64    `json:"tick"`
	Owned   int        `json:"owned"`
	Edges   int        `json:"edges"`
	Scanned int        `json:"scanned"`
	MS      float64    `json:"ms"`
}

type graphSurfaceReport struct {
	Surface      string            `json:"surface"`
	Scope        string            `json:"scope"`
	Status       string            `json:"status"`
	Before       graphCensusRow    `json:"before"`
	After        graphCensusRow    `json:"after"`
	TickBefore   float64           `json:"tickBefore"`
	TickAfter    float64           `json:"tickAfter"`
	Static       int               `json:"staticExported"`
	Mobile       int               `json:"mobileExported"`
	Chunks       int               `json:"nonEmptyChunks"`
	Leaves       []graphLeafReport `json:"leaves"`
	Explanation  string            `json:"explanation"`
	MissingDelta int               `json:"missingDelta"`
}

type graphManifest struct {
	FormatVersion int       `json:"formatVersion"`
	Snapshot      string    `json:"snapshot"`
	CreatedAt     time.Time `json:"createdAt"`
	Force         string    `json:"force"`
	Status        string    `json:"status"`
	// Tick is the game tick of the final read of the export. A paused export
	// reads every surface at one tick; an unpaused one spans ticks, so this is
	// the last read's tick and each surface report carries its own before/after.
	Tick        float64              `json:"tick"`
	Surfaces    []graphSurfaceReport `json:"surfaces"`
	Nodes       int                  `json:"nodes"`
	Edges       int                  `json:"edges"`
	External    int                  `json:"externalNodes"`
	EdgeTypes   map[string]int       `json:"edgeTypes"`
	LabelCounts map[string]int       `json:"labelCounts"`
	DurationMS  float64              `json:"durationMs"`
}

// Status values, worst last.
const (
	graphStatusComplete   = "complete"
	graphStatusDrift      = "drift"
	graphStatusIncomplete = "incomplete"
)

func worseGraphStatus(a, b string) string {
	rank := map[string]int{graphStatusComplete: 0, graphStatusDrift: 1, graphStatusIncomplete: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// reconcileGraphSurface classifies one surface: complete when the static census
// did not move during the scan and every counted entity was exported exactly
// once; drift when the census moved (bots or players built or removed things
// while the scan ran, so no single count is authoritative); incomplete when the
// census held still but the export disagrees, which can only mean a hole.
func reconcileGraphSurface(before, after, exported int) (string, string) {
	switch {
	case before != after:
		return graphStatusDrift, fmt.Sprintf("static census moved from %d to %d during the scan; exported %d", before, after, exported)
	case exported == before:
		return graphStatusComplete, fmt.Sprintf("exported all %d static entities", exported)
	default:
		return graphStatusIncomplete, fmt.Sprintf("census held at %d but exported %d", before, exported)
	}
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// planGraphLeaves groups non-empty chunks into aligned root cells of
// graphRootChunks chunks per side and splits a cell into quadrants until its
// estimated entity count fits leafMax or it is a single chunk. Every non-empty
// chunk lands in exactly one leaf, and leaves never overlap.
func planGraphLeaves(chunks []graphChunk, leafMax int) []graphLeaf {
	if leafMax < 1 {
		leafMax = 1
	}
	type cell struct{ x, y int }
	roots := map[cell][]graphChunk{}
	for _, c := range chunks {
		k := cell{floorDiv(c.X, graphRootChunks), floorDiv(c.Y, graphRootChunks)}
		roots[k] = append(roots[k], c)
	}
	keys := make([]cell, 0, len(roots))
	for k := range roots {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].y != keys[j].y {
			return keys[i].y < keys[j].y
		}
		return keys[i].x < keys[j].x
	})
	var leaves []graphLeaf
	var split func(cx, cy, size int, members []graphChunk)
	split = func(cx, cy, size int, members []graphChunk) {
		total := 0
		for _, m := range members {
			total += m.N
		}
		if len(members) == 0 {
			return
		}
		if total <= leafMax || size == 1 {
			leaves = append(leaves, graphLeaf{CX: cx, CY: cy, Size: size, Count: total})
			return
		}
		half := size / 2
		quads := [4][]graphChunk{}
		for _, m := range members {
			q := 0
			if m.X >= cx+half {
				q++
			}
			if m.Y >= cy+half {
				q += 2
			}
			quads[q] = append(quads[q], m)
		}
		split(cx, cy, half, quads[0])
		split(cx+half, cy, half, quads[1])
		split(cx, cy+half, half, quads[2])
		split(cx+half, cy+half, half, quads[3])
	}
	for _, k := range keys {
		split(k.x*graphRootChunks, k.y*graphRootChunks, graphRootChunks, roots[k])
	}
	return leaves
}

// graphTypeLabels maps engine types to the category labels of the schema. A
// type may carry several (a generator is both a fluid entity and a power
// source); unlisted types are plain :Entity.
var graphTypeLabels = map[string][]string{
	"assembling-machine": {"Machine"}, "furnace": {"Machine"}, "lab": {"Machine"}, "rocket-silo": {"Machine"},
	"agricultural-tower": {"Machine"}, "asteroid-collector": {"Machine"},
	"mining-drill":   {"MiningDrill"},
	"transport-belt": {"Belt"}, "underground-belt": {"Belt"}, "splitter": {"Belt", "Splitter"}, "loader": {"Belt"},
	"loader-1x1": {"Belt"}, "linked-belt": {"Belt"}, "lane-splitter": {"Belt", "Splitter"},
	"inserter":  {"Inserter"},
	"container": {"Chest"}, "logistic-container": {"Chest"}, "linked-container": {"Chest"}, "infinity-container": {"Chest"},
	"pipe": {"FluidEntity"}, "pipe-to-ground": {"FluidEntity"}, "pump": {"FluidEntity"}, "offshore-pump": {"FluidEntity"},
	"storage-tank": {"FluidEntity"}, "infinity-pipe": {"FluidEntity"}, "heat-exchanger": {"FluidEntity"},
	"boiler": {"FluidEntity", "PowerSource"}, "generator": {"FluidEntity", "PowerSource"},
	"fusion-generator": {"FluidEntity", "PowerSource"}, "fusion-reactor": {"PowerSource"},
	"solar-panel": {"PowerSource"}, "accumulator": {"PowerSource"}, "reactor": {"PowerSource"},
	"burner-generator": {"PowerSource"}, "electric-energy-interface": {"PowerSource"}, "lightning-attractor": {"PowerSource"},
	"electric-pole": {"Pole"}, "power-switch": {"Pole", "Combinator"},
	"beacon":        {"Beacon"},
	"roboport":      {"Roboport"},
	"straight-rail": {"Rail"}, "curved-rail-a": {"Rail"}, "curved-rail-b": {"Rail"}, "half-diagonal-rail": {"Rail"},
	"rail-ramp": {"Rail"}, "elevated-straight-rail": {"Rail"}, "elevated-curved-rail-a": {"Rail"},
	"elevated-curved-rail-b": {"Rail"}, "elevated-half-diagonal-rail": {"Rail"}, "legacy-straight-rail": {"Rail"},
	"legacy-curved-rail": {"Rail"}, "rail-signal": {"RailSignal"}, "rail-chain-signal": {"RailSignal"},
	"train-stop":            {"TrainStop"},
	"arithmetic-combinator": {"Combinator"}, "decider-combinator": {"Combinator"}, "constant-combinator": {"Combinator"},
	"selector-combinator": {"Combinator"}, "programmable-speaker": {"Combinator"}, "display-panel": {"Combinator"},
	"lamp":   {"Combinator"},
	"turret": {"Defense"}, "ammo-turret": {"Defense"}, "electric-turret": {"Defense"}, "fluid-turret": {"Defense"},
	"artillery-turret": {"Defense"}, "wall": {"Defense"}, "gate": {"Defense"}, "land-mine": {"Defense"}, "radar": {"Defense"},
	"car": {"Vehicle"}, "spider-vehicle": {"Vehicle"}, "locomotive": {"Vehicle", "RollingStock"},
	"cargo-wagon": {"Vehicle", "RollingStock"}, "fluid-wagon": {"Vehicle", "RollingStock"}, "artillery-wagon": {"Vehicle", "RollingStock"},
	"character":    {"Character"},
	"entity-ghost": {"Ghost"}, "tile-ghost": {"Ghost"},
	"resource": {"ResourceTile"},
}

// graphLogisticRoleLabels maps a logistic container's engine mode to its role
// label. In game colours: active provider purple, passive provider red,
// storage yellow, requester blue, buffer green. Every one of them also carries
// :LogisticChest, so "any logistic chest" is one label.
var graphLogisticRoleLabels = map[string]string{
	"active-provider":  "ActiveProviderChest",
	"passive-provider": "PassiveProviderChest",
	"storage":          "StorageChest",
	"requester":        "RequesterChest",
	"buffer":           "BufferChest",
}

// graphManagedLabels is every label graph load may set or remove on :Entity.
func graphManagedLabels() []string {
	set := map[string]bool{"External": true, "LogisticChest": true}
	for _, l := range graphLogisticRoleLabels {
		set[l] = true
	}
	for _, labels := range graphTypeLabels {
		for _, l := range labels {
			set[l] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func graphLabelsFor(entityType string, external bool) []string {
	labels := []string{"Entity"}
	labels = append(labels, graphTypeLabels[entityType]...)
	if external {
		labels = append(labels, "External")
	}
	return labels
}

// graphBuilder accumulates leaf payloads and turns raw engine links into the
// typed, deduplicated relationships of the schema.
type graphBuilder struct {
	snapshot string
	force    string
	mu       sync.Mutex
	owned    map[string]*graphNodeOut
	mobile   map[string]bool
	refs     map[string]graphRef
	types    map[string]string
	raw      []graphRawEdge
	networks map[string]*graphNodeOut
	// derived holds nodes computed from the finished edges (belt stretches).
	derived map[string]*graphNodeOut
	edges   map[string]*graphEdgeOut
}

func newGraphBuilder(snapshot, force string) *graphBuilder {
	return &graphBuilder{
		snapshot: snapshot, force: force,
		owned: map[string]*graphNodeOut{}, mobile: map[string]bool{}, refs: map[string]graphRef{},
		types: map[string]string{}, networks: map[string]*graphNodeOut{}, edges: map[string]*graphEdgeOut{},
	}
}

func (b *graphBuilder) scope(surface string) string { return surface + "|" + b.force }

// add records one leaf or mobile payload. A node owned twice would mean two
// leaves overlap, which the planner rules out; it is reported, not merged.
func (b *graphBuilder) add(p *graphLeafPayload, mobile bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, n := range p.Nodes {
		if n.ID == "" {
			return errors.New("leaf returned a node without id")
		}
		if _, dup := b.owned[n.ID]; dup {
			return fmt.Errorf("entity %s owned by two leaves", n.ID)
		}
		b.owned[n.ID] = b.nodeOut(n, p.Tick, mobile)
		b.types[n.ID] = n.Type
		if mobile {
			b.mobile[n.ID] = true
		}
	}
	for _, r := range p.Refs {
		if _, ok := b.refs[r.ID]; !ok {
			b.refs[r.ID] = r
		}
		b.types[r.ID] = r.Type
	}
	b.raw = append(b.raw, p.Edges...)
	return nil
}

func (b *graphBuilder) nodeOut(n graphRawNode, tick float64, mobile bool) *graphNodeOut {
	props := map[string]any{
		"id": n.ID, "name": n.Name, "type": n.Type, "surface": n.Surface, "force": n.Force,
		"x": n.X, "y": n.Y, "tick": tick, "snapshot": b.snapshot, "scope": b.scope(n.Surface), "mobile": mobile,
	}
	if n.Unit != nil {
		props["unit"] = *n.Unit
	}
	if n.Direction != nil {
		props["direction"] = *n.Direction
	}
	for k, v := range map[string]string{"quality": n.Quality, "status": n.Status, "recipe": n.Recipe, "recipeQuality": n.RecipeQuality,
		"recipeSource": n.RecipeSource, "ghostName": n.GhostName, "ghostType": n.GhostType, "ghostRecipe": n.GhostRecipe,
		"inputPriority": n.InputPriority, "outputPriority": n.OutputPriority,
		"beltShape": n.BeltShape, "previousRecipe": n.PreviousRecipe, "filterMode": n.FilterMode, "held": n.Held} {
		if v != "" {
			props[k] = v
		}
	}
	if len(n.Filters) > 0 {
		props["filters"] = n.Filters
	}
	if n.Train != nil {
		props["train"] = *n.Train
	}
	if len(n.Modules) > 0 {
		props["modules"] = n.Modules
	}
	for k, v := range map[string][]string{"fuel": n.Fuel, "input": n.Input, "output": n.Output, "items": n.Items, "left": n.Left, "right": n.Right,
		"lanes": n.Lanes, "areaResources": n.AreaResources, "areaAmounts": n.AreaAmounts, "ammo": n.Ammo,
		"fluids": n.Fluids, "requests": n.Requests, "robots": n.Robots} {
		if len(v) > 0 {
			props[k] = v
		}
	}
	for k, v := range map[string]*int64{"fuelCount": n.FuelCount, "inputCount": n.InputCount, "outputCount": n.OutputCount,
		"itemsCount": n.ItemsCount, "leftCount": n.LeftCount, "rightCount": n.RightCount,
		"ammoCount": n.AmmoCount, "robotsCount": n.RobotsCount,
		"productsFinished": n.ProductsFinished, "areaTiles": n.AreaTiles, "stackSizeOverride": n.StackSizeOverr} {
		if v != nil {
			props[k] = *v
		}
	}
	for k, v := range map[string]*float64{"craftingProgress": n.CraftingProgress, "craftingSpeed": n.CraftingSpeed,
		"fluidTotal": n.FluidTotal, "fluidCapacity": n.FluidCapacity,
		"pickupX": n.PickupX, "pickupY": n.PickupY,
		"dropX": n.DropX, "dropY": n.DropY, "miningLeft": n.MiningLeft, "miningTop": n.MiningTop,
		"miningRight": n.MiningRight, "miningBottom": n.MiningBottom} {
		if v != nil {
			props[k] = *v
		}
	}
	// A false boolean is information here (useFilters off, a drop that resolved),
	// so these are written whenever the engine reported a value at all.
	for k, v := range map[string]*bool{"mixedLeft": n.MixedLeft, "mixedRight": n.MixedRight,
		"pickupLeftLane": n.PickupLeftLane, "pickupRightLane": n.PickupRightLane,
		"useFilters": n.UseFilters, "dropResolved": n.DropResolved} {
		if v != nil {
			props[k] = *v
		}
	}
	labels := graphLabelsFor(n.Type, false)
	if role, ok := graphLogisticRoleLabels[n.LogisticMode]; ok {
		labels = append(labels, "LogisticChest", role)
		props["logisticMode"] = n.LogisticMode
	}
	out := &graphNodeOut{ID: n.ID, Labels: labels, Props: props}
	if n.ElectricNetwork != nil {
		props["electricNetwork"] = *n.ElectricNetwork
		netID := fmt.Sprintf("%s:electric:%d", n.Surface, *n.ElectricNetwork)
		b.network(netID, "ElectricNetwork", n.Surface, *n.ElectricNetwork, "")
		b.edge("POWERED_BY", n.ID, "Entity", netID, "Network", "", nil, n.Surface)
	}
	if n.LogisticNetwork != nil {
		props["logisticNetwork"] = *n.LogisticNetwork
		netID := fmt.Sprintf("%s:%s:logistic:%d", n.Surface, n.Force, *n.LogisticNetwork)
		b.network(netID, "LogisticNetwork", n.Surface, *n.LogisticNetwork, "")
		b.edge("IN_LOGISTIC_NETWORK", n.ID, "Entity", netID, "Network", "", nil, n.Surface)
	}
	for _, c := range n.Circuit {
		netID := fmt.Sprintf("%s:circuit:%d", n.Surface, c.Network)
		b.network(netID, "CircuitNetwork", n.Surface, c.Network, c.Color)
		b.edge("ON_CIRCUIT", n.ID, "Entity", netID, "Network", c.Connector,
			map[string]any{"connector": c.Connector, "color": c.Color}, n.Surface)
	}
	return out
}

func (b *graphBuilder) network(id, label, surface string, network int64, color string) {
	if _, ok := b.networks[id]; ok {
		return
	}
	props := map[string]any{"id": id, "network": network, "surface": surface, "force": b.force,
		"snapshot": b.snapshot, "scope": b.scope(surface)}
	if color != "" {
		props["color"] = color
	}
	b.networks[id] = &graphNodeOut{ID: id, Labels: []string{"Network", label}, Props: props}
}

func (b *graphBuilder) edge(typ, from, fromLabel, to, toLabel, discriminator string, props map[string]any, surface string) *graphEdgeOut {
	key := typ + "|" + from + "|" + to + "|" + discriminator
	if e, ok := b.edges[key]; ok {
		return e
	}
	p := map[string]any{"snapshot": b.snapshot, "scope": b.scope(surface)}
	for k, v := range props {
		p[k] = v
	}
	e := &graphEdgeOut{Type: typ, From: from, FromLabel: fromLabel, To: to, ToLabel: toLabel, Key: key, Props: p}
	b.edges[key] = e
	return e
}

func (b *graphBuilder) surfaceOf(id string) string {
	if n, ok := b.owned[id]; ok {
		return n.Props["surface"].(string)
	}
	return b.refs[id].Surface
}

// finish routes every raw link by kind into its relationship type. An unknown
// kind is an error: it means the Lua contract moved and connectivity would
// otherwise vanish silently.
func (b *graphBuilder) finish() error {
	for _, r := range b.raw {
		if r.From == "" || r.To == "" {
			return fmt.Errorf("%s edge without endpoints", r.Kind)
		}
		surface := b.surfaceOf(r.From)
		switch r.Kind {
		case "belt", "underground", "linked", "line", "loader":
			// A splitter reports its own two ports and each neighbouring belt
			// reports the same link back without them, so the ports are merged
			// onto the one edge instead of creating a second.
			e := b.edge("BELT_OUT", r.From, "Entity", r.To, "Entity", r.Kind, map[string]any{"kind": r.Kind}, surface)
			if r.FromPort != "" {
				e.Props["fromPort"] = r.FromPort
			}
			if r.ToPort != "" {
				e.Props["toPort"] = r.ToPort
			}
			// The engine's neighbour keys are ordering only, so they are kept
			// apart from the resolved side: one property, one vocabulary.
			if r.FromPortIndex != "" {
				e.Props["fromPortIndex"] = r.FromPortIndex
			}
			if r.ToPortIndex != "" {
				e.Props["toPortIndex"] = r.ToPortIndex
			}
		case "pickup":
			// Lua reports pickups as source -> inserter; the schema hangs them on the arm.
			// side is the belt side the inserter stands on, not a lane-reach claim.
			b.edge("PICKS_FROM", r.To, "Entity", r.From, "Entity", "", graphSideProps(r.Side), surface)
		case "drop":
			// side is the belt lane the dropped item lands on.
			if b.types[r.From] == "inserter" {
				b.edge("DROPS_TO", r.From, "Entity", r.To, "Entity", "", graphSideProps(r.Side), surface)
			} else {
				b.edge("DROPS_ON", r.From, "Entity", r.To, "Entity", "", nil, surface)
			}
		case "logistic_cover":
			// roboport -> logistic container inside its logistic area; construction
			// says the container is also inside the construction area.
			b.edge("LOGISTIC_COVERS", r.From, "Entity", r.To, "Entity", "",
				map[string]any{"construction": r.Construction != nil && *r.Construction}, surface)
		case "mines":
			b.edge("MINES", r.From, "Entity", r.To, "Entity", "", nil, surface)
		case "beacon":
			b.edge("AFFECTS", r.From, "Entity", r.To, "Entity", "", nil, surface)
		case "rail":
			a, c := canonicalPair(r.From, r.To)
			b.edge("RAIL_LINK", a, "Entity", c, "Entity", "", nil, surface)
		case "fluid":
			// Each port reports its own connection, so one physical link arrives
			// twice with mirrored endpoints. One undirected edge keeps both raw
			// per-port declarations and never claims a direction of flow.
			a, c := canonicalPair(r.From, r.To)
			e := b.edge("FLUID_LINK", a, "Entity", c, "Entity", "", nil, surface)
			field := "dirA"
			if r.From != a {
				field = "dirB"
			}
			if _, seen := e.Props[field]; !seen && r.FlowDirection != "" {
				e.Props[field] = r.FlowDirection
			}
			ports, _ := e.Props["ports"].(int)
			if _, counted := e.Props["seen:"+r.From]; !counted {
				e.Props["seen:"+r.From] = true
				e.Props["ports"] = ports + 1
			}
		case "copper", "red", "green":
			a, c := canonicalPair(r.From, r.To)
			ca, cc := r.FromConnector, r.ToConnector
			if r.From != a {
				ca, cc = cc, ca
			}
			typ := "CIRCUIT_WIRE"
			props := map[string]any{"color": r.Kind, "connectorA": ca, "connectorB": cc}
			if r.Kind == "copper" {
				typ = "POWER_WIRE"
				props = map[string]any{"connectorA": ca, "connectorB": cc}
			}
			b.edge(typ, a, "Entity", c, "Entity", r.Kind+":"+ca+":"+cc, props, surface)
		default:
			return fmt.Errorf("unknown graph link kind %q from %s to %s", r.Kind, r.From, r.To)
		}
	}
	for _, e := range b.edges {
		for k := range e.Props {
			if strings.HasPrefix(k, "seen:") {
				delete(e.Props, k)
			}
		}
	}
	b.raw = nil
	// Stretches are derived from the finished belt edges, so they run last.
	if err := b.buildStretches(); err != nil {
		return err
	}
	return nil
}

func canonicalPair(a, b string) (string, string) {
	if b < a {
		return b, a
	}
	return a, b
}

// externalNodes are edge endpoints no leaf owns: entities of another force,
// neutral resources a drill mines, or entities outside the requested surfaces.
func (b *graphBuilder) externalNodes() []*graphNodeOut {
	var out []*graphNodeOut
	for id, r := range b.refs {
		if _, ok := b.owned[id]; ok {
			continue
		}
		props := map[string]any{"id": r.ID, "name": r.Name, "type": r.Type, "surface": r.Surface, "force": r.Force,
			"x": r.X, "y": r.Y, "snapshot": b.snapshot, "scope": b.scope(r.Surface), "external": true}
		if r.Unit != nil {
			props["unit"] = *r.Unit
		}
		out = append(out, &graphNodeOut{ID: id, Labels: graphLabelsFor(r.Type, true), Props: props})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *graphBuilder) staticCount(surface string) (static, mobile int) {
	for id, n := range b.owned {
		if n.Props["surface"] != surface {
			continue
		}
		if b.mobile[id] {
			mobile++
		} else {
			static++
		}
	}
	return
}

// graphExporter runs one export against a live server.
type graphExporter struct {
	r    *Resolver
	opts graphExportOptions
	log  io.Writer
}

func (x *graphExporter) call(ctx context.Context, lua string, target any) error {
	var lastErr error
	for attempt := range 3 {
		response, _, err := executeLuaContext(ctx, x.r.addr, x.r.password, lua)
		if err == nil {
			if err := json.Unmarshal([]byte(response), target); err != nil {
				return fmt.Errorf("decode export response: %w", err)
			}
			return nil
		}
		lastErr = err
		// Reads are harmless to repeat, but a server-side Lua error will not
		// change on retry.
		if strings.HasPrefix(err.Error(), "server") || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
	}
	return lastErr
}

func (x *graphExporter) census(ctx context.Context, surfaces []string) (*graphCensus, error) {
	var c graphCensus
	if err := x.call(ctx, graphCensusLua(x.opts.Force, surfaces), &c); err != nil {
		return nil, fmt.Errorf("census: %w", err)
	}
	return &c, nil
}

func (x *graphExporter) chunks(ctx context.Context, surface string) ([]graphChunk, error) {
	var all []graphChunk
	offset := 0
	for {
		var page graphChunkPage
		if err := x.call(ctx, graphChunksLua(surface, x.opts.Force, offset, x.opts.ChunkPage), &page); err != nil {
			return nil, fmt.Errorf("chunk census %s: %w", surface, err)
		}
		all = append(all, page.Chunks...)
		if page.Done {
			return all, nil
		}
		if page.Next <= offset {
			return nil, fmt.Errorf("chunk census %s made no progress at offset %d", surface, offset)
		}
		offset = page.Next
	}
}

// exportLeaf reads one leaf; a leaf whose payload breaks the RCON deadline is
// split into quadrants down to a single chunk.
func (x *graphExporter) exportLeaf(ctx context.Context, b *graphBuilder, surface string, leaf graphLeaf) ([]graphLeafReport, error) {
	lx, ly, rx, ry := leaf.tiles()
	started := time.Now()
	var p graphLeafPayload
	err := x.call(ctx, graphLeafLua(x.r, surface, x.opts.Force, lx, ly, rx, ry, graphLeafPad, x.opts.IncludeLineLinks), &p)
	if err != nil {
		if leaf.Size > 1 && !strings.HasPrefix(err.Error(), "server") && ctx.Err() == nil {
			half := leaf.Size / 2
			var reports []graphLeafReport
			for _, q := range []graphLeaf{
				{leaf.CX, leaf.CY, half, 0}, {leaf.CX + half, leaf.CY, half, 0},
				{leaf.CX, leaf.CY + half, half, 0}, {leaf.CX + half, leaf.CY + half, half, 0},
			} {
				r, err := x.exportLeaf(ctx, b, surface, q)
				if err != nil {
					return nil, err
				}
				reports = append(reports, r...)
			}
			return reports, nil
		}
		return nil, fmt.Errorf("leaf %s [%g,%g)-[%g,%g): %w", surface, lx, ly, rx, ry, err)
	}
	if err := b.add(&p, false); err != nil {
		return nil, err
	}
	return []graphLeafReport{{Area: [4]float64{lx, ly, rx, ry}, Tick: p.Tick, Owned: len(p.Nodes), Edges: len(p.Edges),
		Scanned: p.Scanned, MS: float64(time.Since(started).Microseconds()) / 1000}}, nil
}

func (x *graphExporter) run(ctx context.Context) (*graphManifest, []*graphNodeOut, []*graphEdgeOut, error) {
	started := time.Now()
	snapshot := "snap-" + started.UTC().Format("20060102T150405.000Z")
	before, err := x.census(ctx, x.opts.Surfaces)
	if err != nil {
		return nil, nil, nil, err
	}
	var surfaces []graphCensusRow
	for _, row := range before.Surfaces {
		// Without an explicit list, surfaces where the force owns nothing are
		// not part of the factory.
		if len(x.opts.Surfaces) > 0 || row.Total > 0 {
			surfaces = append(surfaces, row)
		}
	}
	if len(surfaces) == 0 {
		return nil, nil, nil, fmt.Errorf("force %q owns no entities on any surface", x.opts.Force)
	}
	b := newGraphBuilder(snapshot, before.Force)
	reports := make([]graphSurfaceReport, len(surfaces))
	for i, row := range surfaces {
		chunks, err := x.chunks(ctx, row.Surface)
		if err != nil {
			return nil, nil, nil, err
		}
		leaves := planGraphLeaves(chunks, x.opts.LeafMax)
		fmt.Fprintf(x.log, "%s: %d static, %d mobile entities in %d chunks -> %d leaves\n", row.Surface, row.Static, row.Mobile, len(chunks), len(leaves))
		leafReports, err := x.exportLeaves(ctx, b, row.Surface, leaves)
		if err != nil {
			return nil, nil, nil, err
		}
		var mobile graphLeafPayload
		if err := x.call(ctx, graphMobileLua(x.r, row.Surface, x.opts.Force), &mobile); err != nil {
			return nil, nil, nil, fmt.Errorf("mobile pass %s: %w", row.Surface, err)
		}
		if err := b.add(&mobile, true); err != nil {
			return nil, nil, nil, err
		}
		reports[i] = graphSurfaceReport{Surface: row.Surface, Scope: b.scope(row.Surface), Before: row, TickBefore: before.Tick,
			Chunks: len(chunks), Leaves: leafReports}
	}
	names := make([]string, len(surfaces))
	for i, r := range surfaces {
		names[i] = r.Surface
	}
	after, err := x.census(ctx, names)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := b.finish(); err != nil {
		return nil, nil, nil, err
	}
	// The production catalog needs the finished graph: only prototypes the
	// snapshot references are read, so the call stays small.
	recipes, resources, machines := b.catalogRequest()
	var catalog graphCatalog
	if err := x.call(ctx, graphCatalogLua(x.opts.Force, names, recipes, resources, machines), &catalog); err != nil {
		return nil, nil, nil, fmt.Errorf("catalog: %w", err)
	}
	if err := b.buildProduction(&catalog); err != nil {
		return nil, nil, nil, err
	}
	status := graphStatusComplete
	for i := range reports {
		for _, row := range after.Surfaces {
			if row.Surface == reports[i].Surface {
				reports[i].After = row
			}
		}
		reports[i].TickAfter = after.Tick
		reports[i].Static, reports[i].Mobile = b.staticCount(reports[i].Surface)
		reports[i].Status, reports[i].Explanation = reconcileGraphSurface(reports[i].Before.Static, reports[i].After.Static, reports[i].Static)
		reports[i].MissingDelta = reports[i].After.Static - reports[i].Static
		status = worseGraphStatus(status, reports[i].Status)
	}

	nodes := make([]*graphNodeOut, 0, len(b.owned)+len(b.networks))
	for _, n := range b.owned {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	external := b.externalNodes()
	nodes = append(nodes, external...)
	nets := make([]*graphNodeOut, 0, len(b.networks)+len(b.derived))
	for _, n := range b.networks {
		nets = append(nets, n)
	}
	for _, n := range b.derived {
		nets = append(nets, n)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].ID < nets[j].ID })
	nodes = append(nodes, nets...)
	edges := make([]*graphEdgeOut, 0, len(b.edges))
	for _, e := range b.edges {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].Key < edges[j].Key })

	m := &graphManifest{FormatVersion: graphFormatVersion, Snapshot: snapshot, CreatedAt: started.UTC(), Force: before.Force,
		Status: status, Tick: after.Tick, Surfaces: reports, Nodes: len(nodes), Edges: len(edges), External: len(external),
		EdgeTypes: map[string]int{}, LabelCounts: map[string]int{}, DurationMS: float64(time.Since(started).Milliseconds())}
	for _, e := range edges {
		m.EdgeTypes[e.Type]++
	}
	for _, n := range nodes {
		for _, l := range n.Labels {
			m.LabelCounts[l]++
		}
	}
	return m, nodes, edges, nil
}

func (x *graphExporter) exportLeaves(ctx context.Context, b *graphBuilder, surface string, leaves []graphLeaf) ([]graphLeafReport, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([][]graphLeafReport, len(leaves))
	errs := make(chan error, len(leaves))
	sem := make(chan struct{}, x.opts.Concurrency)
	var wg sync.WaitGroup
	for i, leaf := range leaves {
		wg.Add(1)
		go func(i int, leaf graphLeaf) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			r, err := x.exportLeaf(ctx, b, surface, leaf)
			if err != nil {
				errs <- err
				cancel()
				return
			}
			results[i] = r
		}(i, leaf)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return nil, err
	}
	var out []graphLeafReport
	for _, r := range results {
		out = append(out, r...)
	}
	return out, nil
}

// writeGraphSnapshot writes the snapshot into a sibling temp directory and
// renames it into place, so a reader never sees a half-written snapshot.
func writeGraphSnapshot(dir string, m *graphManifest, nodes []*graphNodeOut, edges []*graphEdgeOut) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, graphSnapshotManifest)); err != nil {
			return fmt.Errorf("%s exists and is not a graph snapshot; refusing to replace it", dir)
		}
	}
	tmp, err := os.MkdirTemp(parent, ".graph-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	writeLines := func(name string, n int, row func(int) any) error {
		f, err := os.Create(filepath.Join(tmp, name))
		if err != nil {
			return err
		}
		w := bufio.NewWriterSize(f, 1<<20)
		enc := json.NewEncoder(w)
		for i := range n {
			if err := enc.Encode(row(i)); err != nil {
				f.Close()
				return err
			}
		}
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if err := writeLines(graphSnapshotNodes, len(nodes), func(i int) any { return nodes[i] }); err != nil {
		return err
	}
	if err := writeLines(graphSnapshotEdges, len(edges), func(i int) any { return edges[i] }); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, graphSnapshotManifest), append(manifest, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("value must not be empty")
	}
	*s = append(*s, v)
	return nil
}

const graphUsage = `Usage: factorio-ai-player [--addr HOST:PORT] graph <command> [options]

Commands:
  export   Read every entity of one force into a snapshot directory
  load     Write a snapshot directory into Memgraph over Bolt
  schema   Apply the Memgraph constraints and indexes
  sync     export, then load the new snapshot; always leaves the game unpaused

Use 'graph <command> --help' for options.
`

func runGraph(args []string, out io.Writer, addr, password string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, graphUsage)
		return err
	}
	switch args[0] {
	case "export":
		_, err := runGraphExport(args[1:], out, addr, password)
		return err
	case "load":
		return runGraphLoad(args[1:], out)
	case "schema":
		return runGraphSchema(args[1:], out)
	case "sync":
		return runGraphSync(args[1:], out, addr, password)
	}
	return fmt.Errorf("unknown graph command %q; use graph help", args[0])
}

func graphExportFlags(fs *flag.FlagSet, opts *graphExportOptions, surfaces *stringList) {
	fs.StringVar(&opts.Force, "force", "player", "force whose entities are exported")
	fs.Var(surfaces, "surface", "surface to export (repeatable; default every surface the force owns entities on)")
	fs.StringVar(&opts.Out, "out", "", "snapshot directory (default .factorio/graph/<snapshot>)")
	fs.IntVar(&opts.LeafMax, "leaf-max", 2000, "estimated entities per leaf read; each leaf runs on the game thread in one RCON call")
	fs.IntVar(&opts.Concurrency, "concurrency", 4, "parallel leaf reads (1-6)")
	fs.IntVar(&opts.ChunkPage, "chunk-page", 20000, "generated chunks visited per census call")
	fs.BoolVar(&opts.IncludeLineLinks, "line-links", false, "also export transport-line links (BELT_OUT kind line)")
	fs.BoolVar(&opts.Pause, "pause", true, "pause simulation during game reads, then restore its prior pause state")
	fs.BoolVar(&opts.AllowIncomplete, "allow-incomplete", false, "write and exit zero even when reconciliation finds a hole")
	fs.IntVar(&opts.Keep, "keep", 5, "snapshots kept under .factorio/graph when --out is not given")
}

func validateGraphExportOptions(opts *graphExportOptions, surfaces stringList) error {
	opts.Surfaces = surfaces
	if strings.TrimSpace(opts.Force) == "" {
		return errors.New("--force must not be empty")
	}
	if opts.LeafMax < 1 || opts.LeafMax > 20000 {
		return errors.New("--leaf-max must be between 1 and 20000")
	}
	if opts.Concurrency < 1 || opts.Concurrency > rconConcurrency {
		return fmt.Errorf("--concurrency must be between 1 and %d", rconConcurrency)
	}
	if opts.Keep < 1 {
		return errors.New("--keep must be at least 1")
	}
	if opts.ChunkPage < 1 || opts.ChunkPage > 200000 {
		return errors.New("--chunk-page must be between 1 and 200000")
	}
	return nil
}

func runGraphExport(args []string, out io.Writer, addr, password string) (string, error) {
	fs := flag.NewFlagSet("graph export", flag.ContinueOnError)
	fs.SetOutput(out)
	var opts graphExportOptions
	var surfaces stringList
	graphExportFlags(fs, &opts, &surfaces)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", nil
		}
		return "", err
	}
	if fs.NArg() != 0 {
		return "", errors.New("graph export takes no positional arguments")
	}
	if err := validateGraphExportOptions(&opts, surfaces); err != nil {
		return "", err
	}
	return exportGraph(opts, out, addr, password)
}

func exportGraph(opts graphExportOptions, out io.Writer, addr, password string) (string, error) {
	dir, _, err := exportGraphContext(context.Background(), opts, out, addr, password)
	return dir, err
}

// exportGraphContext is exportGraph with a cancellable context; it also
// returns the manifest so callers can report counts and per-surface status.
func exportGraphContext(ctx context.Context, opts graphExportOptions, out io.Writer, addr, password string) (string, *graphManifest, error) {
	if password == "" {
		return "", nil, errors.New("FACTORIO_RCON_PASSWORD is required")
	}
	x := &graphExporter{r: &Resolver{addr: addr, password: password}, opts: opts, log: out}
	started := time.Now()
	m, nodes, edges, err := x.snapshot(ctx)
	logGraphExport(opts, m, started, err)
	if err != nil {
		return "", nil, err
	}
	dir := opts.Out
	if dir == "" {
		dir = filepath.Join(graphDefaultDir, m.Snapshot)
	}
	if err := writeGraphSnapshot(dir, m, nodes, edges); err != nil {
		return "", nil, err
	}
	if opts.Out == "" {
		abs, _ := filepath.Abs(dir)
		if err := os.WriteFile(filepath.Join(graphDefaultDir, graphLatestFile), []byte(abs+"\n"), 0o644); err != nil {
			return "", nil, err
		}
		if err := pruneGraphSnapshots(graphDefaultDir, opts.Keep); err != nil {
			return "", nil, err
		}
	}
	for _, s := range m.Surfaces {
		fmt.Fprintf(out, "%s: %s - %s (%d leaves, ticks %.0f..%.0f)\n", s.Surface, s.Status, s.Explanation, len(s.Leaves), s.TickBefore, s.TickAfter)
	}
	fmt.Fprintf(out, "snapshot %s: %s, %d nodes (%d external), %d edges in %.1fs -> %s\n",
		m.Snapshot, m.Status, m.Nodes, m.External, m.Edges, m.DurationMS/1000, dir)
	if m.Status == graphStatusIncomplete && !opts.AllowIncomplete {
		return dir, m, fmt.Errorf("snapshot is incomplete: a static entity was counted but not exported; see %s", filepath.Join(dir, graphSnapshotManifest))
	}
	return dir, m, nil
}

// pruneGraphSnapshots keeps the newest keep snapshot directories under root.
// Snapshot ids sort by creation time, and only snap-* directories holding a
// manifest are touched.
func pruneGraphSnapshots(root string, keep int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var snaps []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "snap-") {
			if _, err := os.Stat(filepath.Join(root, e.Name(), graphSnapshotManifest)); err == nil {
				snaps = append(snaps, e.Name())
			}
		}
	}
	sort.Strings(snaps)
	for len(snaps) > keep {
		if err := os.RemoveAll(filepath.Join(root, snaps[0])); err != nil {
			return err
		}
		snaps = snaps[1:]
	}
	return nil
}

// logGraphExport appends one summary line to the GraphQL operation log, so an
// export shows up in the same audit trail as every other read of the save.
func logGraphExport(opts graphExportOptions, m *graphManifest, started time.Time, runErr error) {
	entry := graphQLOperationLog{Time: started, Source: "cli", Command: "graph export",
		Document:   fmt.Sprintf("graph export force=%s surfaces=%s leafMax=%d", opts.Force, strings.Join(opts.Surfaces, ","), opts.LeafMax),
		DurationMS: float64(time.Since(started).Microseconds()) / 1000}
	if runErr != nil {
		entry.Error = runErr.Error()
	}
	if m != nil {
		summary := map[string]any{"snapshot": m.Snapshot, "status": m.Status, "nodes": m.Nodes, "edges": m.Edges}
		var rows []map[string]any
		for _, s := range m.Surfaces {
			rows = append(rows, map[string]any{"surface": s.Surface, "status": s.Status, "before": s.Before.Static,
				"after": s.After.Static, "exported": s.Static, "tickBefore": s.TickBefore, "tickAfter": s.TickAfter})
		}
		summary["surfaces"] = rows
		entry.Response, _ = json.Marshal(summary)
	}
	appendGraphQLOperationLog(entry)
}

// graphSideProps is the edge property set for an inserter end on a belt, or nil
// when the end is not a belt.
func graphSideProps(side string) map[string]any {
	if side == "" {
		return nil
	}
	return map[string]any{"side": side}
}
