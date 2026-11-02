package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The engine exposes shooting as a continuous control state, so one RCON call
// cannot fire a burst. The navigation mod's factorio_ai_actions interface keeps
// the state set for a bounded number of ticks; this mutation starts that job and
// polls until it finishes, then reports the ammo the character actually spent.

type actionJobStatus struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Character      string    `json:"character"`
	Status         string    `json:"status"`
	Reason         *string   `json:"reason"`
	StartedTick    float64   `json:"startedTick"`
	UpdatedTick    float64   `json:"updatedTick"`
	Ticks          float64   `json:"ticks"`
	RemainingTicks float64   `json:"remainingTicks"`
	Position       *Position `json:"position"`
}

type shootStartResponse struct {
	Tick      float64          `json:"tick"`
	Player    string           `json:"player"`
	Character string           `json:"character"`
	Surface   string           `json:"surface"`
	Force     string           `json:"force"`
	Target    *PlacedEntity    `json:"target"`
	Position  *Position        `json:"position"`
	Ammo      float64          `json:"ammo"`
	Job       *actionJobStatus `json:"job"`
}

// Firing spends rounds from the magazine held in the stack, and the item count
// only drops when a magazine empties. Counting rounds keeps short bursts
// visible: (count-1)*magazine_size + ammo.
const shootAmmoLua = `local function shoot_ammo_count(c)
  local inv=c.get_inventory(defines.inventory.character_ammo)
  if not inv then return 0 end
  local total=0
  for i=1,#inv do
    local stack=inv[i]
    if stack and stack.valid_for_read then
      local proto=prototypes.item[stack.name]
      local size=proto and proto.magazine_size
      if size then
        total=total+(stack.count-1)*size+(stack.is_ammo and stack.ammo or size)
      else
        total=total+stack.count
      end
    end
  end
  return total
end
`

// shootAction validates the request, starts a shooting job in the mod, waits for
// it to finish, and reports the ammo consumed. A lost response does not prove the
// job did not run; inspect the character before retrying.
func shootAction(ctx context.Context, r *Resolver, in ShootInput) (*ShootResult, error) {
	if in.Actor == nil || in.Actor.Player == nil || strings.TrimSpace(*in.Actor.Player) == "" || in.Actor.Character != nil {
		return nil, errors.New("shoot actor must select one player")
	}
	if in.Count < 1 || in.Count > maxBatchTargets {
		return nil, fmt.Errorf("count must be between 1 and %d shooting ticks", maxBatchTargets)
	}
	if in.Target == nil {
		return nil, errors.New("target is required")
	}
	hasUnit := in.Target.UnitNumber != nil
	hasPosition := in.Target.Position != nil
	if !hasUnit && !hasPosition {
		return nil, errors.New("target requires unitNumber or position")
	}
	var unit uint64
	if hasUnit {
		parsed, err := strconv.ParseUint(*in.Target.UnitNumber, 10, 32)
		if err != nil || parsed == 0 {
			return nil, errors.New("target unitNumber must be from 1 to 4294967295")
		}
		unit = parsed
	}

	if lua, err := shootStartLua(in, unit); err == nil {
		response, _, err := executeLua(r.addr, r.password, lua)
		if err != nil {
			return nil, err
		}
		start, err := factoryDecode[shootStartResponse](response)
		if err != nil {
			return nil, err
		}
		if start.Job == nil || start.Job.ID == "" {
			return nil, errors.New("shooting job response omitted the job id")
		}
		job, err := waitForActionJob(ctx, r, start.Job.ID, time.Duration(in.Count)*time.Second/60+10*time.Second)
		if err != nil {
			return nil, err
		}
		if job.Status != "COMPLETE" {
			reason := ""
			if job.Reason != nil {
				reason = ": " + *job.Reason
			}
			return nil, fmt.Errorf("shooting job %s ended with %s%s", job.ID, job.Status, reason)
		}
		after, err := shootAmmoAfter(r, start.Character)
		if err != nil {
			return nil, err
		}
		used := int(math.Max(0, start.Ammo-after))
		result := &ShootResult{
			Tick:      job.UpdatedTick,
			Player:    *in.Actor.Player,
			Force:     start.Force,
			Character: &start.Character,
			AmmoUsed:  used,
			Target:    start.Target,
			Position:  start.Position,
		}
		announceMutation(r, in.Message, fmt.Sprintf("fired %d ammo at %s", used, targetText(start.Target)))
		return result, nil
	} else {
		return nil, err
	}
}

func shootStartLua(in ShootInput, unit uint64) (string, error) {
	x, y := 0.0, 0.0
	if in.Target.Position != nil {
		x, y = in.Target.Position.X, in.Target.Position.Y
	}
	name := stringValueOr(in.Target.Entity, "")
	surface := stringValueOr(in.Target.Surface, "nauvis")
	return luaFail + fmt.Sprintf(`local p=game.players[%s]; if not p then fail("player not found"); return end
local c=p.character; if not c then fail("player has no character"); return end
local target_unit=%d
local target_name=%s
local target_position={x=%s,y=%s}
local surface_name=%s
`, luaString(*in.Actor.Player), unit, luaString(name),
		strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64), luaString(surface)) +
		shootAmmoLua + fmt.Sprintf(`local e=nil
if target_unit~=0 then
  e=game.get_entity_by_unit_number(target_unit)
  if not e then
    if target_name=="" then fail("prototype is not resolvable by unit number alone; pass entity too"); return end
    local ts=game.surfaces[surface_name]; if not ts then fail("surface not found"); return end
    for _,candidate in pairs(ts.find_entities_filtered{name=target_name}) do
      if candidate.unit_number==target_unit then e=candidate; break end
    end
    if not e then fail("entity not found by unit number on that surface"); return end
  end
  if target_name~="" and e.name~=target_name then fail("entity name does not match unit number"); return end
elseif target_name~="" then
  local ts=game.surfaces[surface_name]; if not ts then fail("surface not found"); return end
  for _,candidate in pairs(ts.find_entities_filtered{name=target_name,position=target_position,radius=0.01}) do
    if math.abs(candidate.position.x-target_position.x)<0.001 and math.abs(candidate.position.y-target_position.y)<0.001 then
      if e then fail("multiple matching entities at center"); return end
      e=candidate
    end
  end
  if not e then fail("entity not found at center"); return end
elseif surface_name~=c.surface.name then
  fail("target surface differs from the character's surface"); return
end
if e and e.surface~=c.surface then fail("target is on another surface"); return end
local aim=e and e.position or target_position
local before=shoot_ammo_count(c)
local ok,job=pcall(function() return remote.call("factorio_ai_actions","start_shoot",c.unit_number,aim.x,aim.y,%d) end)
if not ok then fail(tostring(job)); return end
if type(job)~="table" or job.error then fail(type(job)=="table" and tostring(job.error) or "shooting interface returned no job"); return end
local target=nil
if e then target={name=e.name,type=e.type,position=e.position,unit_number=e.unit_number} end
rcon.print(helpers.table_to_json({tick=game.tick,player=p.name,character=c.unit_number,surface=c.surface.name,force=c.force.name,position={x=aim.x,y=aim.y},ammo=before,job=job,target=target}))`, in.Count), nil
}

func waitForActionJob(ctx context.Context, r *Resolver, id string, timeout time.Duration) (*actionJobStatus, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lua := luaFail + fmt.Sprintf(`local job=remote.call("factorio_ai_actions","status",%s); if type(job)~="table" then fail("shooting interface returned no status"); return end; if job.error then fail(tostring(job.error)); return end; rcon.print(helpers.table_to_json(job))`, luaString(id))
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			_, _, _ = executeLua(r.addr, r.password, luaFail+fmt.Sprintf(`pcall(function() remote.call("factorio_ai_actions","cancel",%s) end)`, luaString(id)))
			if ctx.Err() != nil {
				return nil, fmt.Errorf("shooting job %s may still be active: %w", id, ctx.Err())
			}
			return nil, fmt.Errorf("shooting job %s did not finish in %s; it may still be active", id, timeout)
		case <-ticker.C:
			response, _, err := executeLua(r.addr, r.password, lua)
			if err != nil {
				return nil, err
			}
			job, err := factoryDecode[actionJobStatus](response)
			if err != nil {
				return nil, err
			}
			if job.Status != "SHOOTING" {
				return job, nil
			}
		}
	}
}

func shootAmmoAfter(r *Resolver, character string) (float64, error) {
	lua := luaFail + fmt.Sprintf(`local c=game.get_entity_by_unit_number(%s); if not c or c.type~="character" then fail("character no longer exists"); return end`, character) + shootAmmoLua + `rcon.print(helpers.table_to_json({ammo=shoot_ammo_count(c)}))`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return 0, err
	}
	decoded, err := factoryDecode[struct {
		Ammo float64 `json:"ammo"`
	}](response)
	if err != nil {
		return 0, err
	}
	return decoded.Ammo, nil
}

func targetText(target *PlacedEntity) string {
	if target == nil {
		return "the target"
	}
	return target.Name + positionText(target.Position)
}
