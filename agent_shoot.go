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
	Target    *PlacedEntity    `json:"target"`
	Position  *Position        `json:"position"`
	Ammo      float64          `json:"ammo"`
	Job       *actionJobStatus `json:"job"`
}

const shootAmmoLua = `local function shoot_ammo_count(c)
  local inv=c.get_inventory(defines.inventory.character_ammo)
  if not inv then return 0 end
  local total=0
  for _,count in pairs(inv.get_contents()) do total=total+count end
  return total
end
`

// shootAction validates the request, starts a shooting job in the mod, waits for
// it to finish, and reports the ammo consumed. A lost response does not prove the
// job did not run; inspect the character before retrying.
func shootAction(ctx context.Context, r *Resolver, in ShootInput) (*ShootResult, error) {
	if strings.TrimSpace(in.Player) == "" {
		return nil, errors.New("player must not be empty")
	}
	if in.Count < 1 || in.Count > 100 {
		return nil, errors.New("count must be between 1 and 100 shooting ticks")
	}
	hasUnit := in.TargetUnitNumber != nil && *in.TargetUnitNumber != ""
	hasCoords := in.X != nil || in.Y != nil
	if hasUnit == hasCoords {
		return nil, errors.New("address the target by targetUnitNumber or by x and y, not both")
	}
	var unit uint64
	if hasUnit {
		parsed, err := strconv.ParseUint(*in.TargetUnitNumber, 10, 32)
		if err != nil || parsed == 0 {
			return nil, errors.New("targetUnitNumber must be an entity unit number from 1 to 4294967295")
		}
		unit = parsed
	} else {
		if in.X == nil || in.Y == nil || math.IsNaN(*in.X) || math.IsInf(*in.X, 0) || math.IsNaN(*in.Y) || math.IsInf(*in.Y, 0) {
			return nil, errors.New("target x and y must both be finite")
		}
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
			Player:    in.Player,
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
	if in.X != nil {
		x = *in.X
	}
	if in.Y != nil {
		y = *in.Y
	}
	return luaFail + fmt.Sprintf(`local p=game.players[%s]; if not p then fail("player not found"); return end
local c=p.character; if not c then fail("player has no character"); return end
local target_unit=%d
local target_name=%s
local target_position={x=%s,y=%s}
local surface_name=p.surface.name
`, luaString(in.Player), unit, luaString(""),
		strconv.FormatFloat(x, 'g', -1, 64), strconv.FormatFloat(y, 'g', -1, 64)) +
		luaEntityResolve + shootAmmoLua + fmt.Sprintf(`local e=resolve_entity(target_unit,target_name,target_position,surface_name,p); if not e then return end
if e.surface~=c.surface then fail("target is on another surface"); return end
local before=shoot_ammo_count(c)
local ok,job=pcall(function() return remote.call("factorio_ai_actions","start_shoot",c.unit_number,e.position.x,e.position.y,%d) end)
if not ok then fail(tostring(job)); return end
if type(job)~="table" or job.error then fail(type(job)=="table" and tostring(job.error) or "shooting interface returned no job"); return end
rcon.print(helpers.table_to_json({tick=game.tick,player=p.name,character=c.unit_number,surface=c.surface.name,position=e.position,ammo=before,job=job,target={name=e.name,type=e.type,position=e.position,unit_number=e.unit_number}}))`, in.Count), nil
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
