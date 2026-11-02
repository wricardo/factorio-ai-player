// Compact "what needs attention" read. Read-only.
// Run with --strict=false: otherwise a GraphQL error throws and the envelope is lost.
// gqlcli script --file .agents/skills/factorio-gameplay/scripts/briefing.js \
//   --arg '{"watch":[{"item":"iron-plate","minimum":100}],"area":{"leftTopX":-128,"leftTopY":-128,"rightBottomX":128,"rightBottomY":128}}'
// Input (all optional): surface, force, window (ONE_MINUTE|TEN_MINUTES|ONE_HOUR),
//   area (<=1024 tiles/side; omit = whole surface), watch [{item,minimum}], limit.
async function run(gql, input) {
  input = input || {}
  const vars = {
    input: {
      surface: input.surface || "nauvis",
      force: input.force || "player",
      window: input.window || "ONE_MINUTE",
      attentionLimit: input.limit || 20,
      watchItems: input.watch || [],
    },
  }
  if (input.area) vars.input.area = input.area
  const r = await gql.query(
    `query($input: FactoryBriefingInput!) {
      status { tick speed paused players { name } }
      factoryBriefing(input: $input) {
        attentionTotal
        attentionCounts { kind count }
        attention { kind severity entity unitNumber position { x y } count message }
        machines { type total working noFuel noPower noIngredients outputFull idle other }
        power { productionWatts consumptionWatts networks }
        research { enabled currentTechnology progress queue }
        construction { ghosts ghostsBuildable deconstructionOrders upgradeOrders }
        production { item producedPerMinute consumedPerMinute }
        stockouts { item have minimum shortfall actorHas }
        warnings
      }
    }`,
    vars
  )
  if (r.errors) return { errors: r.errors }
  const b = r.data.factoryBriefing
  return {
    tick: r.data.status.tick,
    paused: r.data.status.paused,
    speed: r.data.status.speed,
    attentionTotal: b.attentionTotal,
    attentionCounts: b.attentionCounts,
    attention: b.attention,
    machines: b.machines,
    power: b.power,
    research: b.research,
    construction: b.construction,
    production: b.production.filter((p) => p.producedPerMinute || p.consumedPerMinute),
    stockouts: b.stockouts,
    warnings: b.warnings,
  }
}
