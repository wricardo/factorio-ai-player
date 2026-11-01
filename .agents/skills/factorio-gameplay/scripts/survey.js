// Resource patch survey with distances from a point. Read-only.
// Run with --strict=false: otherwise a GraphQL error throws and the envelope is lost.
// gqlcli script --file .agents/skills/factorio-gameplay/scripts/survey.js \
//   --arg '{"center":{"x":0,"y":0},"radius":128}'
// Input: center {x,y} (default 0,0), radius (default 128, max 128 -> 256 tile box),
//   resources [names] (default iron-ore, copper-ore, coal, stone, uranium-ore, crude-oil),
//   surface, force.
async function run(gql, input) {
  input = input || {}
  const c = input.center || { x: 0, y: 0 }
  const r = Math.min(input.radius || 128, 128)
  const names = input.resources || ["iron-ore", "copper-ore", "coal", "stone", "uranium-ore", "crude-oil"]
  const area = {
    leftTopX: c.x - r, leftTopY: c.y - r, rightBottomX: c.x + r, rightBottomY: c.y + r,
  }
  const out = []
  for (const name of names) {
    const res = await gql.query(
      `query($input: WorldResourcesInput!) {
        worldResources(input: $input) {
          tick pageInfo { total }
          patches { name totalAmount tiles centroid { x y }
            bounds { leftTopX leftTopY rightBottomX rightBottomY }
            nearbyDrills { unitNumber } }
        }
      }`,
      { input: { surface: input.surface || "nauvis", force: input.force || "player", area,
                 groupPatches: true, resourceName: name, drillRadius: 2, limit: 50, offset: 0 } }
    )
    if (res.errors) { out.push({ name, error: res.errors[0].message }); continue }
    for (const p of res.data.worldResources.patches) {
      const dx = p.centroid.x - c.x, dy = p.centroid.y - c.y
      out.push({
        name: p.name, totalAmount: p.totalAmount, tiles: p.tiles,
        centroid: p.centroid, bounds: p.bounds,
        distance: Math.round(Math.sqrt(dx * dx + dy * dy)),
        drills: p.nearbyDrills.length,
      })
    }
  }
  out.sort((a, b) => (a.distance || 1e9) - (b.distance || 1e9))
  return out
}
