// Unresearched prerequisite closure of a technology, in a valid research order, with pack totals. Read-only.
// Run with --strict=false: otherwise a GraphQL error throws and the envelope is lost.
// gqlcli script --strict=false --file .agents/skills/factorio-gameplay/scripts/tech-path.js --arg '{"technology":"rocket-silo"}'
// Input: technology (required), force (default player), labs (optional, estimates wall time at speed 1,
//   ignoring lab-speed bonuses), unresearchedOnly (default true).
// Output: order[] (name, units, unitSeconds, packs, trigger?, available), totals{units,labSeconds,packs},
//   triggerTechs[] (must be satisfied by an action such as crafting/mining/building, not lab research).
async function run(gql, input) {
  const force = input.force || "player"
  const all = {}
  for (let off = 0; ; off += 100) {
    const r = await gql.query(
      `query($f:String!,$o:Int!){ researchInsights(input:{force:$f,status:ALL,limit:100,offset:$o}){
        pageInfo{ hasMore }
        technologies{ name available researched enabled researchUnitCount researchUnitEnergy researchTriggerJSON
          prerequisites{ name researched } scienceIngredients{ name amount } } } }`,
      { f: force, o: off }
    )
    if (r.errors) return { errors: r.errors }
    for (const t of r.data.researchInsights.technologies) all[t.name] = t
    if (!r.data.researchInsights.pageInfo.hasMore) break
  }
  const target = all[input.technology]
  if (!target) return { error: "unknown technology " + input.technology }
  const seen = new Set(), order = []
  const visit = (name) => {
    if (seen.has(name)) return
    seen.add(name)
    const t = all[name]
    if (!t) return
    for (const p of t.prerequisites) if (!p.researched) visit(p.name)
    if (!t.researched) order.push(t)
  }
  visit(input.technology)
  const totals = { units: 0, labSeconds: 0, packs: {} }
  const triggerTechs = []
  const out = order.map((t) => {
    const unitSeconds = t.researchUnitEnergy / 60
    const packs = {}
    for (const i of t.scienceIngredients) {
      packs[i.name.replace("-science-pack", "")] = i.amount * t.researchUnitCount
      totals.packs[i.name] = (totals.packs[i.name] || 0) + i.amount * t.researchUnitCount
    }
    totals.units += t.researchUnitCount
    totals.labSeconds += t.researchUnitCount * unitSeconds
    const trig = t.researchTriggerJSON || null
    if (trig) triggerTechs.push({ name: t.name, trigger: JSON.parse(trig) })
    return { name: t.name, available: t.available, units: t.researchUnitCount, unitSeconds, packs, trigger: trig && JSON.parse(trig) }
  })
  const res = { target: input.technology, count: out.length, totals, triggerTechs, order: out }
  if (input.labs) res.estimatedMinutesAtSpeed1 = +(totals.labSeconds / input.labs / 60).toFixed(1)
  return res
}
