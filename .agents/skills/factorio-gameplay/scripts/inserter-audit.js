// Verify every inserter in an area has a real pickup AND drop target. Read-only.
// Run with --strict=false: otherwise a GraphQL error throws and the envelope is lost.
// gqlcli script --file .agents/skills/factorio-gameplay/scripts/inserter-audit.js \
//   --arg '{"area":{"leftTopX":-120,"leftTopY":-45,"rightBottomX":-90,"rightBottomY":-34}}'
// Input: area (<=256 tiles/side), surface, force. Uses factoryGraph, which returns each inserter with
// its actual pickup/drop endpoint (machines, belts, chests). Returns only suspicious inserters.
// Flags: NO_PICKUP / NO_DROP (empty tile or unsupported entity), SAME_ENDPOINT, and non-working status.
async function run(gql, input) {
  const r = await gql.query(
    `query($area: FactoryAreaInput!, $force: String!, $surface: String!) {
      factoryGraph(area: $area, force: $force, surface: $surface) {
        tick
        inserters {
          inserter { name unitNumber position { x y } direction status pickupPosition { x y } dropPosition { x y } }
          pickup { name type unitNumber position { x y } }
          drop { name type unitNumber position { x y } }
        }
      }
    }`,
    { area: input.area, force: input.force || "player", surface: input.surface || "nauvis" }
  )
  if (r.errors) return { errors: r.errors }
  const g = r.data.factoryGraph
  const issues = []
  for (const c of g.inserters) {
    const flags = []
    if (!c.pickup) flags.push("NO_PICKUP")
    if (!c.drop) flags.push("NO_DROP")
    if (c.pickup && c.drop && c.pickup.unitNumber && c.pickup.unitNumber === c.drop.unitNumber) flags.push("SAME_ENDPOINT")
    const st = c.inserter.status
    if (st && st !== "working" && st !== "normal" && st !== "waiting_for_source_items" && st !== "waiting_for_space_in_destination") flags.push("STATUS_" + st)
    if (flags.length) issues.push({ unitNumber: c.inserter.unitNumber, name: c.inserter.name, position: c.inserter.position,
      direction: c.inserter.direction, pickupPosition: c.inserter.pickupPosition, dropPosition: c.inserter.dropPosition,
      pickup: c.pickup && `${c.pickup.name}@${c.pickup.position.x},${c.pickup.position.y}`,
      drop: c.drop && `${c.drop.name}@${c.drop.position.x},${c.drop.position.y}`, flags })
  }
  return { tick: g.tick, inserters: g.inserters.length, suspicious: issues.length, issues }
}
