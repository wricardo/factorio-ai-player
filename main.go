package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strings"
	"time"
)

const usage = `Usage: factorio-ai-player [--addr HOST:PORT] <command> [options]

Commands:
  query, mutation  Execute an inline GraphQL operation
  script, js       Run a JavaScript workflow over the GraphQL schema
  serve            Serve the GraphQL schema over HTTP
  queries, mutations, types, describe  Discover the GraphQL schema
  help      Show this help

Set FACTORIO_RCON_PASSWORD to the server's RCON password.
Use '<command> --help' for command options.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	return runWithCredentials(args, out, os.Getenv("FACTORIO_RCON_PASSWORD"))
}

func runWithCredentials(args []string, out io.Writer, password string) error {
	global := flag.NewFlagSet("factorio-ai-player", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	addr := global.String("addr", "127.0.0.1:27015", "RCON address")
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, usage)
			return err
		}
		return err
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" {
		_, err := io.WriteString(out, usage)
		return err
	}
	if args[0] == "serve" {
		return runServer(args[1:], out, *addr, password)
	}
	if !isGraphQLCommand(args[0]) {
		return fmt.Errorf("unknown command %q; use help", args[0])
	}
	return runGraphQL(args, out, *addr, password)
}

// executeGameOperation is the private game backend used by GraphQL resolvers.
func executeGameOperation(args []string, out io.Writer, addr, password string) error {
	var lua string
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("status takes no options")
		}
		lua = `local players = {}; for _, p in pairs(game.connected_players) do players[#players+1] = {name=p.name, surface=p.surface.name, position=p.position} end; rcon.print(helpers.table_to_json({tick=game.tick, players=players, speed=game.speed, paused=game.tick_paused}))`
	case "entity":
		var err error
		lua, err = entityLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "character-create", "character-return", "player-restore-character":
		var err error
		lua, err = characterLua(args[0], args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "character-spawn":
		var err error
		lua, err = characterSpawnLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "characters":
		var err error
		lua, err = charactersLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "player", "inventory", "queue", "recipe":
		var err error
		lua, err = playerInspectLua(args[0], args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "mine":
		var err error
		lua, err = resourceLua(args[0], args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "remove":
		var err error
		lua, err = removeLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "clear-area":
		var err error
		lua, err = clearAreaLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "harvest-area":
		var err error
		lua, err = harvestAreaLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "pickup-area":
		var err error
		lua, err = pickupAreaLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "pickup":
		var err error
		lua, err = pickupLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "rotate":
		var err error
		lua, err = rotateLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "belt-take":
		var err error
		lua, err = beltTakeLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "inspect", "transfer":
		var err error
		lua, err = productionLua(args[0], args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "set-recipe":
		var err error
		lua, err = machineRecipeLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "equip":
		var err error
		lua, err = equipLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "start-research":
		var err error
		lua, err = researchLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "cancel-research":
		var err error
		lua, err = researchCancelLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "craft":
		var err error
		lua, err = craftLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "cancel-craft":
		var err error
		lua, err = craftCancelLua(args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "place", "fuel":
		var err error
		lua, err = actionLua(args[0], args[1:], out)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if err != nil {
			return err
		}
	case "save", "shutdown":
		if len(args) != 1 {
			return fmt.Errorf("%s takes no options", args[0])
		}
		if password == "" {
			return errors.New("FACTORIO_RCON_PASSWORD is required")
		}
		command := "/server-save"
		if args[0] == "shutdown" {
			command = "/quit"
		}
		response, err := execute(addr, password, command, true)
		if err != nil {
			return err
		}
		if response != "" {
			_, err = fmt.Fprint(out, response)
		}
		return err
	case "nearby":
		fs := flag.NewFlagSet("nearby", flag.ContinueOnError)
		fs.SetOutput(out)
		surface := fs.String("surface", "nauvis", "Surface name")
		x := fs.Float64("x", 0, "Center X coordinate")
		y := fs.Float64("y", 0, "Center Y coordinate")
		radius := fs.Float64("radius", 10, "Search radius (greater than 0, at most 100)")
		name := fs.String("name", "", "Filter by entity prototype name")
		kind := fs.String("type", "", "Filter by entity type")
		order := fs.String("order", "distance", "Result order: distance or unit-number")
		offset := fs.Int("offset", 0, "Zero-based offset into the ordered matches")
		limit := fs.Int("limit", 100, "Maximum returned entities (1-1000)")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("nearby takes no positional arguments")
		}
		if *surface == "" || math.IsNaN(*x) || math.IsInf(*x, 0) || math.IsNaN(*y) || math.IsInf(*y, 0) || math.IsNaN(*radius) || math.IsInf(*radius, 0) || *radius <= 0 || *radius > 100 {
			return errors.New("invalid surface, coordinates, or radius")
		}
		ordering := strings.ToLower(*order)
		if ordering != "distance" && ordering != "unit-number" && ordering != "unit_number" {
			return errors.New("order must be distance or unit-number")
		}
		if *offset < 0 || *limit < 1 || *limit > 1000 {
			return errors.New("offset must be non-negative and limit must be between 1 and 1000")
		}
		lua = nearbyLua(*surface, *x, *y, *radius, *name, *kind, ordering, *offset, *limit)
	default:
		return fmt.Errorf("unknown command %q; use help", args[0])
	}
	lua = withSnapshotPrint(lua, args)
	if password == "" {
		return errors.New("FACTORIO_RCON_PASSWORD is required")
	}
	response, _, err := executeLua(addr, password, lua)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(strings.TrimSpace(response)), "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}

const luaFail = `local function fail(message) rcon.print(helpers.table_to_json({error=message})) end; `

func snapshotActorPrelude(args []string) string {
	var player, character string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "--player=") {
			player = strings.TrimPrefix(arg, "--player=")
		}
		if strings.HasPrefix(arg, "--character=") {
			character = strings.TrimPrefix(arg, "--character=")
		}
	}
	if player != "" {
		return fmt.Sprintf(`local snapshot_actor=game.get_player(%s); local snapshot_player=%s; local snapshot_character=nil; local snapshot_surface=snapshot_actor and snapshot_actor.surface.name or nil; local snapshot_force=snapshot_actor and snapshot_actor.force.name or nil; `, luaString(player), luaString(player))
	}
	if character != "" && character != "0" {
		return fmt.Sprintf(`local snapshot_actor=game.get_entity_by_unit_number(%s); local snapshot_player=nil; local snapshot_character=%s; local snapshot_surface=snapshot_actor and snapshot_actor.surface.name or nil; local snapshot_force=snapshot_actor and snapshot_actor.force.name or nil; `, character, character)
	}
	return `local snapshot_player=nil; local snapshot_character=nil; local snapshot_surface=nil; local snapshot_force=nil; `
}

const luaSnapshotPrint = `local function snapshot_print(encoded)
  local ok,value=pcall(helpers.json_to_table,encoded)
  if ok and type(value)=="table" and value.error==nil then
    if value.tick==nil then value.tick=game.tick end
    if value.surface==nil then value.surface=snapshot_surface end
    if value.force==nil then value.force=snapshot_force end
    if value.player==nil and value.character==nil then
      value.player=snapshot_player
      value.character=snapshot_character
    end
  end
  if ok and type(value)=="table" then encoded=helpers.table_to_json(value) end
  rcon.print(encoded)
end; `

// withSnapshotPrint guarantees observation tick and selected actor metadata on
// every backend game operation without duplicating it in each domain builder.
func withSnapshotPrint(lua string, args []string) string {
	return snapshotActorPrelude(args) + luaSnapshotPrint + strings.ReplaceAll(lua, "rcon.print(", "snapshot_print(")
}

// executeLua is the common boundary for all Lua commands.
func executeLua(addr, password, lua string) (string, map[string]json.RawMessage, error) {
	return executeLuaContext(context.Background(), addr, password, lua)
}

func executeLuaContext(ctx context.Context, addr, password, lua string) (string, map[string]json.RawMessage, error) {
	response, err := executeContext(ctx, addr, password, "/silent-command "+lua, false)
	if err != nil {
		return "", nil, err
	}
	response = strings.TrimSpace(response)
	if response == "" {
		return "", nil, errors.New("empty server response; Factorio may have requested Lua command confirmation and disabled achievements; check server log, then retry")
	}
	if strings.HasPrefix(response, "Cannot execute command. Error:") {
		return "", nil, fmt.Errorf("server Lua: %s", strings.TrimSpace(strings.TrimPrefix(response, "Cannot execute command. Error:")))
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &result); err != nil || result == nil {
		return "", nil, fmt.Errorf("unexpected server response: %q", response)
	}
	if msg, ok := result["error"]; ok {
		return "", nil, fmt.Errorf("server: %s", msg)
	}
	return response, result, nil
}

// Encode bytes as Lua decimal escapes, avoiding Lua source injection and preserving UTF-8.
func luaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range []byte(s) {
		fmt.Fprintf(&b, "\\%03d", c)
	}
	b.WriteByte('"')
	return b.String()
}

func execute(addr, password, command string, single bool) (string, error) {
	return executeContext(context.Background(), addr, password, command, single)
}

func executeContext(ctx context.Context, addr, password, command string, single bool) (string, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("connect RCON: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", err
	}
	if err := writePacket(conn, 1, 3, password); err != nil {
		return "", err
	}
	for {
		id, kind, _, err := readPacket(conn)
		if err != nil {
			return "", fmt.Errorf("authenticate RCON: %w", err)
		}
		if kind == 2 {
			if id != 1 {
				return "", errors.New("RCON authentication failed")
			}
			break
		}
		if id != 1 || kind != 0 {
			return "", errors.New("unexpected RCON authentication response")
		}
	}
	if err := writePacket(conn, 2, 2, command); err != nil {
		return "", err
	}
	if single {
		id, kind, body, err := readPacket(conn)
		if err != nil {
			return "", fmt.Errorf("read RCON response: %w", err)
		}
		if id != 2 || kind != 0 {
			return "", errors.New("unexpected RCON response")
		}
		return body, nil
	}
	// A second command marks the end of a potentially multi-packet response.
	const marker = "factorio-ai-player-response-end"
	if err := writePacket(conn, 3, 2, `/silent-command rcon.print("`+marker+`")`); err != nil {
		return "", err
	}
	var response strings.Builder
	markerFirst := false
	secondMarkerSent := false
	for {
		id, kind, body, err := readPacket(conn)
		if err != nil {
			return "", fmt.Errorf("read RCON response: %w", err)
		}
		if kind != 0 {
			return "", errors.New("unexpected RCON response type")
		}
		if id == 3 || id == 4 {
			if strings.TrimSpace(body) != marker {
				return "", fmt.Errorf("unexpected RCON completion response: %q", body)
			}
			if id == 3 && response.Len() == 0 {
				markerFirst = true
				continue
			}
			return response.String(), nil
		}
		if id != 2 {
			return "", errors.New("unexpected RCON response ID")
		}
		if response.Len()+len(body) > 4<<20 {
			return "", errors.New("RCON response exceeds 4 MiB")
		}
		response.WriteString(body)
		if markerFirst && !secondMarkerSent {
			if err := writePacket(conn, 4, 2, `/silent-command rcon.print("`+marker+`")`); err != nil {
				return "", err
			}
			secondMarkerSent = true
		}
	}
}

func writePacket(w io.Writer, id, kind int32, body string) error {
	if len(body) > 4<<20 {
		return errors.New("RCON request exceeds 4 MiB")
	}
	packet := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(packet, uint32(len(body)+10))
	binary.LittleEndian.PutUint32(packet[4:], uint32(id))
	binary.LittleEndian.PutUint32(packet[8:], uint32(kind))
	copy(packet[12:], body)
	for len(packet) > 0 {
		n, err := w.Write(packet)
		if err != nil {
			return fmt.Errorf("write RCON: %w", err)
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		packet = packet[n:]
	}
	return nil
}

func readPacket(r io.Reader) (int32, int32, string, error) {
	var sizeBytes [4]byte
	if _, err := io.ReadFull(r, sizeBytes[:]); err != nil {
		return 0, 0, "", err
	}
	size := binary.LittleEndian.Uint32(sizeBytes[:])
	if size < 10 || size > 4<<20 {
		return 0, 0, "", fmt.Errorf("invalid RCON packet size %d", size)
	}
	packet := make([]byte, size)
	if _, err := io.ReadFull(r, packet); err != nil {
		return 0, 0, "", err
	}
	if packet[size-1] != 0 || packet[size-2] != 0 {
		return 0, 0, "", errors.New("invalid RCON packet terminator")
	}
	return int32(binary.LittleEndian.Uint32(packet)), int32(binary.LittleEndian.Uint32(packet[4:])), string(packet[8 : size-2]), nil
}
