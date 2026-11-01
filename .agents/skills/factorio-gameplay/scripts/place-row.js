// Probe, then place a straight row/column/grid of one item. DRY RUN unless apply:true.
// ALWAYS run with --strict=false: a throw hides the placeMany envelope, and a lost response is not proof of rollback.
// gqlcli script --file .agents/skills/factorio-gameplay/scripts/place-row.js \
//   --arg '{"actor":{"character":"70"},"item":"transport-belt","start":{"x":10.5,"y":4.5},"step":{"x":1,"y":0},"count":12,"direction":4}'
// Input: actor {player|character}, item, start {x,y} (entity CENTER), step {x,y} per entity,
//   count (<=1000), direction (0..15, default 0), apply (default false), surface, force,
//   underground: "INPUT"|"OUTPUT" (optional, for underground belts), message.
// Behaviour: probePlacement on the covered box for the item; with any blocked tile the script returns
//   the blockers and places nothing. Then (apply:true) checks stock and calls placeMany once.
// Footprint note: probePlacement samples tile CENTERS for a 1x1 shape; for machines larger than 1x1 use
//   nearestPlacement or probePlacement with mode SITES instead of this script.
async function run(gql, input) {
  const surface = input.surface || "nauvis"
  const force = input.force || "player"
  const dir = input.direction || 0
  const pts = []
  for (let i = 0; i < input.count; i++) {
    pts.push({ x: input.start.x + i * input.step.x, y: input.start.y + i * input.step.y })
  }
  const xs = pts.map((p) => p.x), ys = pts.map((p) => p.y)
  const area = {
    leftTopX: Math.floor(Math.min(...xs)), leftTopY: Math.floor(Math.min(...ys)),
    rightBottomX: Math.ceil(Math.max(...xs)) + 0.01, rightBottomY: Math.ceil(Math.max(...ys)) + 0.01,
  }
  const probe = await gql.query(
    `query($input: PlacementProbeInput!) {
      probePlacement(input: $input) { tick sampled placeable blocked
        blockedTiles { position { x y } blockers terrain tile } }
    }`,
    { input: { surface, force, prototype: input.item, direction: dir, area,
               mode: "BLOCKED_TILES", siteWidth: 1, siteHeight: 1, maxSites: 1 } }
  )
  if (probe.errors) return { stage: "probe", errors: probe.errors }
  const p = probe.data.probePlacement
  const blockedSet = new Set(p.blockedTiles.map((t) => `${t.position.x},${t.position.y}`))
  const hits = pts.filter((q) => blockedSet.has(`${q.x},${q.y}`))
  const result = { stage: "probe", tick: p.tick, planned: pts.length, blockedOnRoute: hits.length,
                   blocked: p.blockedTiles.filter((t) => hits.some((h) => h.x === t.position.x && h.y === t.position.y)) }
  if (hits.length) return { ...result, placed: 0, note: "route blocked; nothing placed" }
  if (!input.apply) return { ...result, placed: 0, note: "dry run; pass apply:true to place" }

  const placements = pts.map((q) => {
    const e = { position: { x: q.x, y: q.y }, direction: dir }
    if (input.underground) e.undergroundType = input.underground
    return e
  })
  const m = await gql.mutation(
    `mutation($input: PlaceManyInput!) {
      placeMany(input: $input) { placed failed errors { index code message target { position { x y } } }
        entities { name unitNumber position { x y } } }
    }`,
    { input: { actor: input.actor, surface, item: input.item, placements, message: input.message || null } }
  )
  if (m.errors) return { ...result, stage: "place", errors: m.errors, note: "outcome uncertain: inspect before retry" }
  return { ...result, stage: "place", ...m.data.placeMany }
}
