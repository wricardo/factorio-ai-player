# Combat, defense, and damage recovery

## Objective

Protect production, power, logistics, and expansion routes with sustainable defenses. Prefer automated threat detection, ammunition supply, power, and repair capacity over repeated player intervention. Destroy only threats relevant to factory safety or an explicit objective.

Repository schemas and live state define available operations, actor restrictions, weapon behavior, enemy state, and required items. Run `queries`, `mutations`, and `describe` before acting.

## Safety rules

- Observe before engaging. Record tick, actor, enemy positions, damaged entities, alerts, pollution, and nearby infrastructure.
- Never blind-retry `shoot`, `useCapsule`, placement, removal, or repair mutations. An error or lost response does not prove no action occurred.
- Keep combat changes bounded. Defend or clear one sector, then inspect the result.
- Avoid unrelated nest clearing or environmental destruction.
- Protect shared power, rail, fluid, and logistic routes during engagement.
- Verify actor inventory, equipment, health, and position before player combat.
- Discover exact prototype, ammunition, capsule, inventory, and equipment names. Never infer engine names from display text.

## Read operations

Use `safety` to inspect enemies, damaged entities, alerts, and pollution around the relevant area. Combine it with:

- `nearby` or `entities` for exact positions and stable entity identities;
- `inspect` for health, inventories, settings, power, and status;
- `stock` for ammunition, repair packs, walls, turrets, and replacement items;
- `worldLogistics` and `factoryGraph` for supply routes and critical connections;
- `electricNetworks` and `powerConsumers` for powered defenses;
- `playerInsight`, `player`, or actor inventory reads for player combat readiness;
- `surfaces` for pollution and evolution context when exposed by the current schema.

Treat nested relationship fields as separate snapshots. Factorio advances while reads execute.

## Defensive planning

Prioritize assets by consequence of failure:

1. generation and power distribution;
2. ammunition or fluid supply for defenses;
3. mining and smelting needed to replace losses;
4. rail intersections and logistic chokepoints;
5. science, mall, and other replaceable production.

Select a bounded defensive sector. Record enemy approach direction, protected assets, legal build area, supply route, and repair access. Leave room for overlapping coverage, future upgrades, belts or inserters, power poles, walls, and replacement access.

Do not assume one defensive design fits every threat. Discover available technologies, recipes, prototypes, ammunition, power requirements, fluid requirements, ranges, and current enemy strength.

## Automated defense

Defense should operate without repeated player transfers.

- Supply ammunition-consuming defenses from a bounded automated production and delivery path.
- Verify inserter pickup and drop positions and target inventory.
- Bound ammunition buffers so defense production does not consume all shared plates or explosives.
- Verify powered defenses belong to a supplied electric network.
- Keep restart paths for defenses whose fuel, power, or fluid supply can fail.
- Use walls and routing space to control approach only when they do not block factory expansion or repair access.
- Reserve replacement turrets, walls, ammunition, poles, inserters, and repair packs in bounded mall outputs when recurring demand proves need.
- Inspect supply and coverage after expansion; placement alone does not prove sustainable defense.

Proof: defensive inventories refill without actor transfers, powered entities remain connected, overlapping sectors cover intended approaches, and supply production remains healthy over time.

## Player combat

`shoot` and `useCapsule` require a connected player actor. Detached characters cannot substitute. Verify current schema and actor restrictions with `describe` before use.

Before engagement:

- inspect player health, armor, equipment grid, weapons, ammunition, and capsules;
- confirm target position and a safe route;
- confirm retreat route and avoid crossing active trains or hazardous factory areas;
- ensure requested action is necessary for defense, expansion, or the explicit objective.

During engagement, use short actions and re-observe. Do not issue a long combat mutation sequence against assumed enemy positions. Enemies and projectiles move while the game advances.

After engagement, query the area again. Confirm remaining enemies, player state, damaged infrastructure, dropped items, and whether the original route or build site is now safe.

## Damage recovery

Repair infrastructure in dependency order:

1. isolate immediate threat;
2. restore power generation and distribution;
3. restore defensive supply and active defenses;
4. restore critical mining, smelting, transport, and fluid links;
5. repair or replace downstream production;
6. verify automated replacement and resupply.

Use `repair` only after discovering its current input contract and confirming the actor has required repair items. Verify entity health after the mutation. If replacement is required, preserve entity identity, settings, direction, recipe, circuit connections, fluid compatibility, and inventories where applicable.

Do not hide a recurring attack or supply failure with repeated repairs. Fix the root cause: insufficient coverage, empty ammunition, disconnected power, blocked delivery, exposed pollution boundary, or an expansion route crossing hostile territory.

## Mall integration

Defense products belong in the mall only when recurring demand proves need. Common candidates include walls, turrets, ammunition, repair packs, poles, inserters, and replacement supply infrastructure. Keep every output bounded. Defense buffers must not starve science, power expansion, or core intermediates.

Preserve lower-tier products when upgraded defensive recipes consume them. Add upgrade cells downstream and verify both buffers refill.

## Verification loop

1. Capture baseline `safety`, actor, stock, entity health, defensive inventories, and network state.
2. Identify one immediate threat or one broken dependency.
3. Make the smallest complete defensive or repair change.
4. Allow simulation to advance.
5. Re-query same fields.
6. Confirm threat is removed or bounded, assets remain supplied, and no unrelated system regressed.

Success requires observed safety and sustainable supply. Mutation success, enemy disappearance from one snapshot, or a full ammunition chest alone is insufficient proof.
