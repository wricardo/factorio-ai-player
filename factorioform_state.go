package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Factorioform state: the manifest address -> live unit number mapping, held in
// the save's script storage so it travels with the world it describes (unit
// numbers are save-specific). Layout, per the epic contract:
//
//	storage.factorio_ai_player_factorioform = {manifests = {[name] = {surface,
//	  force, updated=tick, resources = {[address] = {unit, entity, x, y,
//	  direction, quality}}}}}
//
// Every call builds the new manifest value completely before writing it back,
// so a failed call (caps, duplicate units) leaves the stored state untouched.
const (
	maxFactorioformManifests         = 200
	maxFactorioformResources         = 20000
	maxFactorioformNameCharacters    = 64
	maxFactorioformAddressCharacters = 256
)

const factorioformStateLua = `
local ff=storage.factorio_ai_player_factorioform
if type(ff)~="table" then ff={manifests={}}; storage.factorio_ai_player_factorioform=ff end
if type(ff.manifests)~="table" then ff.manifests={} end
local function check(condition,message) if not condition then error(message,0) end end
local function sorted_keys(t)
  local keys={}
  for key in pairs(t) do keys[#keys+1]=key end
  table.sort(keys)
  return keys
end
local function count_keys(t) local n=0; for _ in pairs(t) do n=n+1 end; return n end
-- table_to_json encodes an empty Lua table as {}, which cannot decode into a Go slice; omit it instead.
local function arr(t) if next(t)==nil then return nil end return t end
local function resource_json(address,r)
  return {address=address,unitNumber=r.unit,entity=r.entity,position={x=r.x,y=r.y},direction=r.direction,quality=r.quality}
end
local function manifest_json(name,m,with_resources)
  local resources={}
  if with_resources then
    for _,address in ipairs(sorted_keys(m.resources)) do resources[#resources+1]=resource_json(address,m.resources[address]) end
  end
  return {name=name,surface=m.surface,force=m.force,updatedTick=m.updated or 0,resourceCount=count_keys(m.resources),resources=arr(resources)}
end
local function ff_get(name,with_resources)
  local m=ff.manifests[name]
  check(m~=nil,"factorioform manifest "..name.." has no state")
  return manifest_json(name,m,with_resources)
end
local function ff_list()
  local list={}
  for _,name in ipairs(sorted_keys(ff.manifests)) do list[#list+1]=manifest_json(name,ff.manifests[name],false) end
  return list
end
-- ff_put atomically sets entries and drops removals in one manifest, creating
-- it when absent. Returns the replaced unit numbers keyed by address.
local function ff_put(name,surface,force,entries,removals)
  local old=ff.manifests[name]
  if old==nil then
    check(count_keys(ff.manifests)<MAX_FACTORIOFORM_MANIFESTS,"factorioform state holds at most MAX_FACTORIOFORM_MANIFESTS manifests")
  else
    check(old.surface==surface and old.force==force,"factorioform manifest "..name.." is bound to surface "..tostring(old.surface).." force "..tostring(old.force)..", not surface "..surface.." force "..force)
  end
  local resources={}
  if old then for address,r in pairs(old.resources) do resources[address]=r end end
  local replaced={}
  for _,address in ipairs(removals) do resources[address]=nil end
  for _,e in ipairs(entries) do
    local before=resources[e.address]
    if before and before.unit~=e.unit then replaced[e.address]=before.unit end
    resources[e.address]={unit=e.unit,entity=e.entity,x=e.x,y=e.y,direction=e.direction,quality=e.quality}
  end
  check(count_keys(resources)<=MAX_FACTORIOFORM_RESOURCES,"factorioform manifest "..name.." holds at most MAX_FACTORIOFORM_RESOURCES resources")
  local owner={}
  for _,address in ipairs(sorted_keys(resources)) do
    local unit=resources[address].unit
    if owner[unit]~=nil then error("unit "..tostring(unit).." is mapped to both "..owner[unit].." and "..address,0) end
    owner[unit]=address
  end
  ff.manifests[name]={surface=surface,force=force,updated=game.tick,resources=resources}
  return replaced
end
`

func factorioformStatePrelude() string {
	return strings.NewReplacer(
		"MAX_FACTORIOFORM_MANIFESTS", strconv.Itoa(maxFactorioformManifests),
		"MAX_FACTORIOFORM_RESOURCES", strconv.Itoa(maxFactorioformResources),
	).Replace(factorioformStateLua)
}

// ffStateEntry is one manifest address mapped to the live entity it controls.
type ffStateEntry struct {
	Address   string
	Unit      uint64
	Entity    string
	X, Y      float64
	Direction int
	Quality   string
}

func runFactorioformState(r *Resolver, body string) (string, error) {
	lua := luaFail + factorioformStatePrelude() + `
local ok,err=pcall(function()
` + body + `
end)
if not ok then fail(tostring(err)) end
`
	response, _, err := executeLua(r.addr, r.password, lua)
	return response, err
}

func validateFactorioformName(name string) error {
	if name == "" || len(name) > maxFactorioformNameCharacters {
		return fmt.Errorf("name must be 1 through %d characters", maxFactorioformNameCharacters)
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("name must contain only lowercase letters, digits, '-' and '_'")
		}
	}
	return nil
}

func validateFactorioformAddress(address string) error {
	if address == "" {
		return errors.New("address must not be empty")
	}
	if strings.TrimSpace(address) != address {
		return errors.New("address must not start or end with whitespace")
	}
	if len(address) > maxFactorioformAddressCharacters {
		return fmt.Errorf("address must be at most %d characters", maxFactorioformAddressCharacters)
	}
	for _, c := range address {
		if c < 0x20 || c == 0x7f {
			return errors.New("address must not contain control characters")
		}
	}
	return nil
}

func luaFactorioformEntries(entries []ffStateEntry) string {
	var b strings.Builder
	b.WriteByte('{')
	for _, e := range entries {
		fmt.Fprintf(&b, "{address=%s,unit=%d,entity=%s,x=%s,y=%s,direction=%d,quality=%s},",
			luaString(e.Address), e.Unit, luaString(e.Entity), luaCoordinate(e.X), luaCoordinate(e.Y), e.Direction, luaString(e.Quality))
	}
	b.WriteByte('}')
	return b.String()
}

func luaFactorioformAddresses(addresses []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for _, a := range addresses {
		b.WriteString(luaString(a))
		b.WriteByte(',')
	}
	b.WriteByte('}')
	return b.String()
}

func factorioformStates(r *Resolver, name *string) ([]*FactorioformManifestState, error) {
	if name == nil {
		response, err := runFactorioformState(r, `rcon.print(helpers.table_to_json({tick=game.tick,manifests=arr(ff_list())}))`)
		if err != nil {
			return nil, err
		}
		decoded, err := factoryDecode[struct {
			Manifests []*FactorioformManifestState `json:"manifests"`
		}](response)
		if err != nil {
			return nil, err
		}
		if decoded.Manifests == nil {
			return []*FactorioformManifestState{}, nil
		}
		return decoded.Manifests, nil
	}
	if err := validateFactorioformName(*name); err != nil {
		return nil, err
	}
	response, err := runFactorioformState(r, fmt.Sprintf(`rcon.print(helpers.table_to_json({tick=game.tick,manifest=ff_get(%s,true)}))`, luaString(*name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Manifest *FactorioformManifestState `json:"manifest"`
	}](response)
	if err != nil {
		return nil, err
	}
	return []*FactorioformManifestState{decoded.Manifest}, nil
}

func factorioformImport(r *Resolver, input FactorioformImportInput) (*FactorioformImportResult, error) {
	if err := validateFactorioformName(input.Name); err != nil {
		return nil, err
	}
	if err := validateFactorioformAddress(input.Address); err != nil {
		return nil, err
	}
	surface := "nauvis"
	if input.Surface != nil && *input.Surface != "" {
		surface = *input.Surface
	}
	force := "player"
	if input.Force != nil && *input.Force != "" {
		force = *input.Force
	}
	unit, name, x, y, targetSurface, err := entityRefTarget(input.Target)
	if err != nil {
		return nil, err
	}
	if input.Target.Surface == nil || *input.Target.Surface == "" {
		targetSurface = surface
	}
	if targetSurface != surface {
		return nil, fmt.Errorf("target surface %q differs from manifest surface %q", targetSurface, surface)
	}
	body := entityRefPrelude("target", unit, name, x, y, surface) + fmt.Sprintf(`
check(target.unit_number~=nil,"target entity "..target.name.." has no unit number and cannot be tracked")
check(target.force.name==%[2]s,"target entity belongs to force "..target.force.name..", not "..%[2]s)
local manifest=ff.manifests[%[1]s]
local address=%[3]s
local current=manifest and manifest.resources[address]
if current and current.unit~=target.unit_number then
  check(%[4]t,"address "..address.." is already mapped to unit "..tostring(current.unit).."; pass overwrite:true to remap it")
end
if manifest then
  for other,r in pairs(manifest.resources) do
    check(other==address or r.unit~=target.unit_number,"unit "..tostring(target.unit_number).." is already mapped to "..other.." in manifest "..%[1]s)
  end
end
local quality="normal"
pcall(function() if target.quality then quality=target.quality.name end end)
local entry={address=address,unit=target.unit_number,entity=target.name,x=target.position.x,y=target.position.y,direction=target.direction or 0,quality=quality}
local replaced=ff_put(%[1]s,%[5]s,%[2]s,{entry},{})
rcon.print(helpers.table_to_json({tick=game.tick,replacedUnitNumber=replaced[address] and tostring(replaced[address]) or nil,resource=resource_json(address,ff.manifests[%[1]s].resources[address]),manifest=manifest_json(%[1]s,ff.manifests[%[1]s],false)}))`,
		luaString(input.Name), luaString(force), luaString(input.Address), input.Overwrite, luaString(surface))
	response, err := runFactorioformState(r, body)
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[FactorioformImportResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("factorioform %s: imported %s as unit %s", input.Name, input.Address, decoded.Resource.UnitNumber))
	return decoded, nil
}

func factorioformForget(r *Resolver, input FactorioformForgetInput) (*FactorioformForgetResult, error) {
	if err := validateFactorioformName(input.Name); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	addresses := make([]string, 0, len(input.Addresses))
	for _, address := range input.Addresses {
		if err := validateFactorioformAddress(address); err != nil {
			return nil, err
		}
		if !seen[address] {
			seen[address] = true
			addresses = append(addresses, address)
		}
	}
	body := fmt.Sprintf(`local name=%s
local m=ff.manifests[name]
check(m~=nil,"factorioform manifest "..name.." has no state")
local addresses=%s
local forgotten,missing={},{}
local deleted=false
if #addresses==0 then
  for _,address in ipairs(sorted_keys(m.resources)) do forgotten[#forgotten+1]=address end
  ff.manifests[name]=nil
  deleted=true
else
  for _,address in ipairs(addresses) do
    if m.resources[address] then forgotten[#forgotten+1]=address else missing[#missing+1]=address end
  end
  if #forgotten>0 then ff_put(name,m.surface,m.force,{},forgotten) end
end
local remaining=0
if ff.manifests[name] then remaining=count_keys(ff.manifests[name].resources) end
rcon.print(helpers.table_to_json({tick=game.tick,name=name,forgotten=arr(forgotten),missing=arr(missing),manifestDeleted=deleted,remaining=remaining}))`,
		luaString(input.Name), luaFactorioformAddresses(addresses))
	response, err := runFactorioformState(r, body)
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[FactorioformForgetResult](response)
	if err != nil {
		return nil, err
	}
	if decoded.Forgotten == nil {
		decoded.Forgotten = []string{}
	}
	if decoded.Missing == nil {
		decoded.Missing = []string{}
	}
	announceMutation(r, input.Message, fmt.Sprintf("factorioform %s: forgot %d address(es)", input.Name, len(decoded.Forgotten)))
	return decoded, nil
}

// ffStateGet returns one manifest's stored state, or nil when none exists. For
// later tickets (diff, refresh).
func ffStateGet(r *Resolver, name string) (*FactorioformManifestState, error) {
	if err := validateFactorioformName(name); err != nil {
		return nil, err
	}
	response, err := runFactorioformState(r, fmt.Sprintf(`local m=ff.manifests[%s]
rcon.print(helpers.table_to_json({tick=game.tick,manifest=m and manifest_json(%s,m,true) or nil}))`, luaString(name), luaString(name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Manifest *FactorioformManifestState `json:"manifest"`
	}](response)
	if err != nil {
		return nil, err
	}
	return decoded.Manifest, nil
}

// ffStatePut atomically upserts entries and removes addresses in one manifest,
// creating it when absent.
func ffStatePut(r *Resolver, name, surface, force string, entries []ffStateEntry, removeAddresses []string) error {
	if err := validateFactorioformName(name); err != nil {
		return err
	}
	for _, e := range entries {
		if err := validateFactorioformAddress(e.Address); err != nil {
			return err
		}
		if e.Unit == 0 {
			return fmt.Errorf("entry %s needs a unit number", e.Address)
		}
	}
	_, err := runFactorioformState(r, fmt.Sprintf(`ff_put(%s,%s,%s,%s,%s)
rcon.print(helpers.table_to_json({tick=game.tick}))`, luaString(name), luaString(surface), luaString(force),
		luaFactorioformEntries(entries), luaFactorioformAddresses(removeAddresses)))
	return err
}

// ffStateDelete drops a whole manifest's state; absent is not an error.
func ffStateDelete(r *Resolver, name string) error {
	if err := validateFactorioformName(name); err != nil {
		return err
	}
	_, err := runFactorioformState(r, fmt.Sprintf(`ff.manifests[%s]=nil
rcon.print(helpers.table_to_json({tick=game.tick}))`, luaString(name)))
	return err
}
