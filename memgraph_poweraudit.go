package main

import (
	"context"
	"fmt"
	"strings"
)

// memgraph.powerAudit: power topology faults from the exported graph.
//
// Graph facts the rules rest on (verified live): every entity the engine puts
// on an electric network has one POWERED_BY edge to that network's
// ElectricNetwork node (poles and generators included); POWER_WIRE joins poles;
// the PowerSource label covers generators, boilers, solar panels, accumulators
// and electric-energy-interfaces. Membership is therefore "has a POWERED_BY
// edge", and an entity the engine reports no network for has none.
//
// Rules:
//   - UNPOWERED (BLOCKER): an electric consumer with no POWERED_BY edge. A
//     consumer is a Machine, Inserter, MiningDrill, Beacon or Roboport, or a
//     radar / electric turret. Burner entities are not consumers: anything with
//     a BURNS edge or a fuel inventory, and the names stone-furnace,
//     steel-furnace and burner-*. Belts, chests, pipes, ghosts and characters
//     are not electric.
//   - SINGLE_POLE_NETWORK (CONCERN): an ElectricNetwork whose only member is one
//     pole (no wire, powers nothing).
//   - ISOLATED_POLE (CONCERN): a pole with no POWER_WIRE edge that is on no
//     network, or on a network with no member besides poles. A pole that is
//     the only member of its network is reported as SINGLE_POLE_NETWORK only.
//   - NO_GENERATOR (BLOCKER): an ElectricNetwork with at least one consumer
//     member (anything that is neither a pole nor a PowerSource) and no
//     PowerSource member. Impact is the number of machines on the network.
//
// OVERLOADED_NOMINAL is not implemented: the graph carries no electric
// capacity (generator output) and no per-entity energy consumption, only item
// rates, so a nominal power balance cannot be derived from it. Use
// electricNetworks for live demand and satisfaction.
//
// Topology only: these are facts about the snapshot's wiring, not a live
// brownout reading.

const powerAuditNote = " Topology at the snapshot, not a live brownout reading."

// powerAuditConsumersCypher lists electric consumers with no network edge. A
// consumer with a BURNS edge is a burner entity and is not returned.
const powerAuditConsumersCypher = `MATCH (e:Entity)
WHERE e.surface = $surface
  AND (e:Machine OR e:Inserter OR e:MiningDrill OR e:Beacon OR e:Roboport OR (e:Defense AND e.type IN ['radar', 'electric-turret']))
  AND NOT (e)-[:POWERED_BY]->()
  AND NOT (e)-[:BURNS]->()
RETURN e`

// powerAuditNetworksCypher counts the members of each ElectricNetwork.
const powerAuditNetworksCypher = `MATCH (e)-[:POWERED_BY]->(n:ElectricNetwork)
WHERE n.surface = $surface
RETURN n,
  count(e) AS members,
  sum(CASE WHEN e:Pole THEN 1 ELSE 0 END) AS poles,
  sum(CASE WHEN e:PowerSource THEN 1 ELSE 0 END) AS sources,
  sum(CASE WHEN e:Machine THEN 1 ELSE 0 END) AS machines`

// powerAuditPolesCypher lists the poles with their wire count and network id
// (null when the pole is on no network).
const powerAuditPolesCypher = `MATCH (p:Pole)
WHERE p.surface = $surface
OPTIONAL MATCH (p)-[w:POWER_WIRE]-()
OPTIONAL MATCH (p)-[:POWERED_BY]->(n:ElectricNetwork)
RETURN p, count(DISTINCT w) AS wires, n.id AS net`

// powerAuditBurnerNames are fuel-burning entities without a BURNS edge in the
// graph (an empty fuel slot has none).
var powerAuditBurnerNames = map[string]bool{"stone-furnace": true, "steel-furnace": true}

func powerAuditIsBurner(n *MemgraphTraceNode, props map[string]any) bool {
	if n.Name != nil && (powerAuditBurnerNames[*n.Name] || strings.HasPrefix(*n.Name, "burner-")) {
		return true
	}
	if fuel, ok := props["fuel"]; ok && fuel != nil {
		return true
	}
	if c, ok := props["fuelCount"]; ok && c != nil {
		return true
	}
	return false
}

func powerAuditInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

// powerAuditNetworkNode reads an ElectricNetwork node. Its labels are
// Network and ElectricNetwork, so the kind and a readable name are set here.
func powerAuditNetworkNode(v any) (*MemgraphTraceNode, error) {
	n, err := traceNodeFromAny(v)
	if err != nil {
		return nil, err
	}
	n.Kind = "ElectricNetwork"
	name := "electric-network"
	if m, ok := v.(map[string]any); ok {
		if props, ok := m["properties"].(map[string]any); ok {
			if id := powerAuditInt(props["network"]); id != 0 {
				name = fmt.Sprintf("electric-network %d", id)
			}
		}
	}
	n.Name = &name
	return n, nil
}

type powerAuditNetwork struct {
	node                              *MemgraphTraceNode
	members, poles, sources, machines int
}

type powerAuditPole struct {
	node  *MemgraphTraceNode
	wires int
	net   string
}

func powerAudit(ctx context.Context, read memgraphReader, in MemgraphAuditInput) (*MemgraphAudit, error) {
	if err := validateAuditInput(in); err != nil {
		return nil, err
	}
	params := map[string]any{"surface": in.Surface}
	var snap *MemgraphSnapshotInfo
	truncated := false
	run := func(cypher string) (*MemgraphQueryResult, error) {
		res, err := read(ctx, MemgraphQueryInput{Cypher: cypher, Params: params, Limit: auditReadRows, TimeoutMs: auditReadTimeout})
		if err != nil {
			return nil, err
		}
		if res.Truncated {
			truncated = true
		}
		if res.Snapshot != nil {
			snap = res.Snapshot
		}
		return res, nil
	}

	consumers, err := run(powerAuditConsumersCypher)
	if err != nil {
		return nil, fmt.Errorf("unpowered consumers: %w", err)
	}
	networks, err := run(powerAuditNetworksCypher)
	if err != nil {
		return nil, fmt.Errorf("electric networks: %w", err)
	}
	poles, err := run(powerAuditPolesCypher)
	if err != nil {
		return nil, fmt.Errorf("poles: %w", err)
	}

	var findings []*MemgraphAuditFinding
	unpowered, err := powerUnpoweredFindings(consumers)
	if err != nil {
		return nil, err
	}
	findings = append(findings, unpowered...)

	nets, err := powerDecodeNetworks(networks)
	if err != nil {
		return nil, err
	}
	poleList, err := powerDecodePoles(poles)
	if err != nil {
		return nil, err
	}
	findings = append(findings, powerNetworkFindings(nets, poleList)...)
	return finishAudit(findings, in.Limit, truncated, snap), nil
}

func powerUnpoweredFindings(res *MemgraphQueryResult) ([]*MemgraphAuditFinding, error) {
	var out []*MemgraphAuditFinding
	for _, row := range res.Rows {
		if len(row) < 1 {
			return nil, fmt.Errorf("consumer row has %d columns, want 1", len(row))
		}
		n, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		props, _ := row[0].(map[string]any)["properties"].(map[string]any)
		if powerAuditIsBurner(n, props) {
			continue
		}
		out = append(out, &MemgraphAuditFinding{
			Code: "UNPOWERED", Severity: MemgraphAuditSeverityBlocker, Node: n,
			Message: fmt.Sprintf("%s is on no electric network (no POWERED_BY edge): no pole's supply area reaches it, or the pole serving it is not connected to anything.%s",
				traceLabel(n, false), powerAuditNote),
			Related: []*MemgraphTraceNode{},
		})
	}
	return out, nil
}

func powerDecodeNetworks(res *MemgraphQueryResult) ([]powerAuditNetwork, error) {
	out := make([]powerAuditNetwork, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) < 5 {
			return nil, fmt.Errorf("network row has %d columns, want 5", len(row))
		}
		node, err := powerAuditNetworkNode(row[0])
		if err != nil {
			return nil, err
		}
		out = append(out, powerAuditNetwork{
			node: node, members: powerAuditInt(row[1]), poles: powerAuditInt(row[2]),
			sources: powerAuditInt(row[3]), machines: powerAuditInt(row[4]),
		})
	}
	return out, nil
}

func powerDecodePoles(res *MemgraphQueryResult) ([]powerAuditPole, error) {
	out := make([]powerAuditPole, 0, len(res.Rows))
	for _, row := range res.Rows {
		if len(row) < 3 {
			return nil, fmt.Errorf("pole row has %d columns, want 3", len(row))
		}
		node, err := traceNodeFromAny(row[0])
		if err != nil {
			return nil, err
		}
		net, _ := row[2].(string)
		out = append(out, powerAuditPole{node: node, wires: powerAuditInt(row[1]), net: net})
	}
	return out, nil
}

// powerNetworkFindings applies the network and pole rules to decoded rows.
func powerNetworkFindings(nets []powerAuditNetwork, poles []powerAuditPole) []*MemgraphAuditFinding {
	byID := map[string]powerAuditNetwork{}
	polesOf := map[string][]*MemgraphTraceNode{}
	for _, n := range nets {
		byID[n.node.ID] = n
	}
	for _, p := range poles {
		polesOf[p.net] = append(polesOf[p.net], p.node)
	}

	var out []*MemgraphAuditFinding
	single := map[string]bool{}
	for _, n := range nets {
		related := polesOf[n.node.ID]
		if related == nil {
			related = []*MemgraphTraceNode{}
		}
		if n.members == 1 && n.poles == 1 {
			single[n.node.ID] = true
			out = append(out, &MemgraphAuditFinding{
				Code: "SINGLE_POLE_NETWORK", Severity: MemgraphAuditSeverityConcern, Node: n.node,
				Message: "The network's only member is one pole: it has no wire to another pole and powers no entity, so it is a stray pole." + powerAuditNote,
				Related: related,
			})
		}
		if consumers := n.members - n.poles - n.sources; consumers > 0 && n.sources == 0 {
			impact := n.machines
			out = append(out, &MemgraphAuditFinding{
				Code: "NO_GENERATOR", Severity: MemgraphAuditSeverityBlocker, Node: n.node,
				Message: fmt.Sprintf("%d consumers (%d machines) are on this network but no generator, solar panel, accumulator or other power source is: nothing supplies it.%s",
					consumers, n.machines, powerAuditNote),
				Related: related, Impact: &impact,
			})
		}
	}
	for _, p := range poles {
		if p.wires > 0 || (p.net != "" && single[p.net]) {
			continue
		}
		if n, ok := byID[p.net]; ok && n.members-n.poles > 0 {
			continue
		}
		msg := "The pole has no wire and is on no electric network."
		if p.net != "" {
			msg = "The pole has no wire and its network has no member besides poles, so it powers nothing."
		}
		out = append(out, &MemgraphAuditFinding{
			Code: "ISOLATED_POLE", Severity: MemgraphAuditSeverityConcern, Node: p.node,
			Message: msg + powerAuditNote, Related: []*MemgraphTraceNode{},
		})
	}
	return out
}
