// Measure real item rates as stock deltas between two game ticks. Read-only.
// Run with --strict=false: otherwise a GraphQL error throws and the envelope is lost.
// gqlcli script --file .agents/skills/factorio-gameplay/scripts/measure-rate.js \
//   --arg '{"items":["iron-plate","copper-plate"],"ticks":1800,"area":{"leftTopX":-8,"leftTopY":-40,"rightBottomX":8,"rightBottomY":-30}}'
// Input: items [names] (required), ticks (default 1800 = 30 s game time; waits wall-clock
//   ticks/60/speed seconds, so keep <= 14000 at speed 1), area (stock scope; omit = whole surface),
//   surface, force.
// Rate = delta stock / delta ticks * 3600 per minute. Stock is net of consumption: a consumed item
// shows lower rate than produced. Use production() for produced-vs-consumed in the same window.
async function readStock(gql, input) {
  const stockInput = {
    surface: input.surface || "nauvis", force: input.force || "player",
    itemsLimit: 500, itemsOffset: 0, limit: 1, offset: 0, totalsLimit: 1, totalsOffset: 0,
  }
  if (input.area) stockInput.area = input.area
  const r = await gql.query(
    `query($input: StockInput) { status { tick } stock(input: $input) { tick items { item quality count } } }`,
    { input: stockInput }
  )
  if (r.errors) throw new Error(r.errors[0].message)
  const byItem = {}
  for (const it of r.data.stock.items) byItem[it.item] = (byItem[it.item] || 0) + it.count
  return { tick: r.data.stock.tick, byItem }
}

async function run(gql, input) {
  const items = input.items || []
  const ticks = input.ticks || 1800
  const a = await readStock(gql, input)
  const w = await gql.mutation(
    `mutation($in: WaitUntilInput!) { waitUntil(input: $in) { satisfied timedOut tick } }`,
    { in: { timeoutSeconds: 300, pollSeconds: 1, condition: { tick: a.tick + ticks } } }
  )
  if (w.errors) return { errors: w.errors }
  const b = await readStock(gql, input)
  const dt = b.tick - a.tick
  const rates = {}
  for (const item of items) {
    const d = (b.byItem[item] || 0) - (a.byItem[item] || 0)
    rates[item] = { before: a.byItem[item] || 0, after: b.byItem[item] || 0, delta: d,
                    perMinute: dt > 0 ? +(d / dt * 3600).toFixed(2) : null }
  }
  return { fromTick: a.tick, toTick: b.tick, ticks: dt, timedOut: w.data.waitUntil.timedOut, rates }
}
