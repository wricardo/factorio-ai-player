package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const graphSnapshotResumeTimeout = 15 * time.Second

// snapshot keeps the pause lease around game reads only. Its deferred release
// finishes before the caller writes files or starts a Memgraph load.
func (x *graphExporter) snapshot(ctx context.Context) (m *graphManifest, nodes []*graphNodeOut, edges []*graphEdgeOut, err error) {
	if !x.opts.Pause {
		return x.run(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	token := rand.Text()
	acquired := false
	// Register cleanup before sending: a lost response does not prove that the
	// engine failed to pause. The token lets cleanup find the original state
	// without relying on any part of the acquisition response.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), graphSnapshotResumeTimeout)
		defer cancel()
		if releaseErr := graphSnapshotPauseCall(cleanupCtx, x.r, graphSnapshotReleaseLua(token, !acquired)); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("restore graph snapshot pause state: %w", releaseErr))
		}
	}()
	response, _, acquireErr := executeLuaContext(ctx, x.r.addr, x.r.password, graphSnapshotAcquireLua(token))
	if acquireErr != nil {
		return nil, nil, nil, fmt.Errorf("pause graph snapshot: %w", acquireErr)
	}
	var result struct {
		Acquired bool `json:"acquired"`
	}
	if decodeErr := json.Unmarshal([]byte(response), &result); decodeErr != nil {
		return nil, nil, nil, fmt.Errorf("decode graph snapshot pause response: %w", decodeErr)
	}
	if !result.Acquired {
		return nil, nil, nil, errors.New("graph snapshot pause lease was not acquired")
	}
	acquired = true
	return x.run(ctx)
}

// Pause mutations deliberately bypass graphExporter.call, whose read retries
// cannot establish whether an earlier mutation reached the engine.
func graphSnapshotPauseCall(ctx context.Context, r *Resolver, lua string) error {
	response, _, err := executeLuaContext(ctx, r.addr, r.password, lua)
	if err != nil {
		return err
	}
	var result struct {
		Released bool `json:"released"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return err
	}
	if !result.Released {
		return errors.New("graph snapshot pause lease was not released")
	}
	return nil
}

// RCON commands are atomic on the game thread. Engine-side holders coordinate
// CLI and API exports even when they run in separate processes. Only the first
// holder captures the original pause state; only the last release restores it.
const graphSnapshotPausePrelude = `local state=storage.factorio_ai_player_graph_snapshot_pause
if not state then
  state={holders={},cancelled={}}
  storage.factorio_ai_player_graph_snapshot_pause=state
end
`

// graphSnapshotUnpauseLua drops any pause lease, including one an interrupted
// export left behind, and resumes the simulation.
const graphSnapshotUnpauseLua = `storage.factorio_ai_player_graph_snapshot_pause=nil
game.tick_paused=false
rcon.print(helpers.table_to_json({paused=game.tick_paused}))`

// unpauseGraphSnapshot resumes the simulation and verifies the read-back state.
// It is used by the CLI sync after the export lease has been released, so a
// crashed earlier export cannot leave the shared save paused.
func unpauseGraphSnapshot(ctx context.Context, r *Resolver) error {
	response, _, err := executeLuaContext(ctx, r.addr, r.password, graphSnapshotUnpauseLua)
	if err != nil {
		return err
	}
	var result struct {
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return err
	}
	if result.Paused {
		return errors.New("graph snapshot unpause did not resume the simulation")
	}
	return nil
}

func graphSnapshotAcquireLua(token string) string {
	return graphSnapshotPausePrelude + fmt.Sprintf(`local token=%s
if state.cancelled[token] then
  state.cancelled[token]=nil
  rcon.print(helpers.table_to_json({acquired=false}))
  return
end
if next(state.holders)==nil then state.original=game.tick_paused end
state.holders[token]=true
game.tick_paused=true
rcon.print(helpers.table_to_json({acquired=true}))`, luaString(token))
}

func graphSnapshotReleaseLua(token string, uncertain bool) string {
	// An uncertain acquire may still be queued on a different RCON connection.
	// Keep its cancellation token until that acquire arrives, so it cannot
	// pause after cleanup. If it never arrives, one boolean token remains in
	// storage. Successful acquisitions leave no cancellation records. Expiring
	// these rare records by tick/count would make a late acquire unsafe again.
	return graphSnapshotPausePrelude + fmt.Sprintf(`local token=%s
if %t then state.cancelled[token]=true end
state.holders[token]=nil
if next(state.holders)==nil and state.original~=nil then
  game.tick_paused=state.original
  state.original=nil
end
rcon.print(helpers.table_to_json({released=true}))`, luaString(token), uncertain)
}
