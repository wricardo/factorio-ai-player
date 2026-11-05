package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Blueprint catalog: named blueprint books held in the save's script storage.
//
// A book is persisted as its exported blueprint-book string, never as an item
// reference: script inventories do not survive a save/load, storage does, and
// a string is also what moves a catalog between saves. Every call imports the
// stored string into a temporary script inventory, mutates the item, exports
// it again, and writes the new string back at the end, so a failed call leaves
// the stored string untouched. The temporary inventories are destroyed on both
// the success and error paths.
const blueprintCatalogLua = `
local catalog=storage.factorio_ai_player_blueprints
if type(catalog)~="table" then catalog={books={}}; storage.factorio_ai_player_blueprints=catalog end
if type(catalog.books)~="table" then catalog.books={} end
local scratch={}
local function new_inventory() local inv=game.create_inventory(1); scratch[#scratch+1]=inv; return inv end
local function cleanup() for i=#scratch,1,-1 do pcall(function() scratch[i].destroy() end) end scratch={} end
local function check(condition,message) if not condition then error(message,0) end end
local function book_names()
  local names={}
  for name in pairs(catalog.books) do names[#names+1]=name end
  table.sort(names)
  return names
end
local function load_book(data)
  local inv=new_inventory()
  local book=inv[1]
  local rc=book.import_stack(data)
  check(rc==0,"catalog book is corrupt: import returned "..tostring(rc))
  check(book.is_blueprint_book,"catalog string is not a blueprint book")
  return book,book.get_inventory(defines.inventory.item_main)
end
local function used_slots(inner)
  local count=0
  for i=1,#inner do if inner[i].valid_for_read then count=count+1 end end
  return count
end
local function free_slot(inner)
  for i=1,#inner do if not inner[i].valid_for_read then return i end end
  return #inner+1
end
local function entry_of(inner,index,with_blueprint)
  local stack=inner[index]
  local entry={index=index,name=stack.name,label=stack.label,entity_count=0,tile_count=0}
  if stack.is_blueprint then
    entry.entity_count=stack.get_blueprint_entity_count()
    local tiles=stack.get_blueprint_tiles()
    entry.tile_count=tiles and #tiles or 0
  end
  if with_blueprint then entry.blueprint=stack.export_stack() end
  return entry
end
local function entries_of(inner,with_blueprint)
  local entries={}
  for i=1,#inner do
    if inner[i].valid_for_read then entries[#entries+1]=entry_of(inner,i,with_blueprint) end
  end
  return entries
end
local function summary_of(name,book,inner,stored)
  return {name=name,label=book.label,description=book.blueprint_description,
    slot_count=#inner,entry_count=used_slots(inner),active_index=book.active_index,
    characters=#stored.blueprint,updated_tick=stored.updated or 0}
end
local function require_book(name)
  local stored=catalog.books[name]
  check(stored~=nil,"blueprint book "..name.." is not in the catalog; create or import it first")
  return stored
end
local function export_book(book)
  local ok,data=pcall(function() return book.export_stack() end)
  check(ok,"could not export blueprint book: "..tostring(data))
  return data
end
local function store_book(name,data)
  local characters=0
  local count=0
  for other,stored in pairs(catalog.books) do
    count=count+1
    if other~=name then characters=characters+#stored.blueprint end
  end
  if catalog.books[name]==nil then check(count<MAX_BLUEPRINT_BOOKS,"catalog holds at most MAX_BLUEPRINT_BOOKS books") end
  check(characters+#data<=MAX_BLUEPRINT_CATALOG_CHARACTERS,"catalog would exceed MAX_BLUEPRINT_CATALOG_CHARACTERS characters")
  catalog.books[name]={blueprint=data,updated=game.tick}
end
local function check_single_blueprint(data)
  -- Validate in a standalone stack: importing a script other than a single
  -- blueprint into a slot inside a book makes the engine raise about the slot
  -- instead of reporting the real problem.
  local inv=new_inventory()
  local stack=inv[1]
  check(stack.set_stack{name="blueprint",count=1},"could not create a blueprint stack")
  local rc=stack.import_stack(data)
  check(rc==0,"invalid blueprint string: "..tostring(rc))
  local ok,is_setup=pcall(function() return stack.is_blueprint_setup() end)
  check(ok,"blueprint string is not a single blueprint: "..tostring(is_setup))
  check(is_setup,"blueprint is not set up")
end
local function import_single_blueprint(stack,data)
  check_single_blueprint(data)
  local rc=stack.import_stack(data)
  check(rc==0,"invalid blueprint string: "..tostring(rc))
end
local function writable_slot(inner,index)
  -- The engine grows a blueprint book, and fills its holes, only through the
  -- first free slot, so any other empty index would silently write elsewhere.
  -- Wording is built only on the failing path: reading a hole raises.
  if index<1 or index>#inner+1 then error("index must be between 1 and "..tostring(#inner+1),0) end
  local hole=free_slot(inner)
  if index<=#inner and inner[index].valid_for_read then
    error("slot "..tostring(index).." already holds "..inner[index].name.."; remove or update it first",0)
  end
  if index>#inner then
    if hole~=index then error("slot "..tostring(index).." is past the last slot, but the engine fills slot "..tostring(hole).." next; pass that index or omit index",0) end
    local before=#inner
    inner.insert{name="blueprint",count=1}
    if #inner~=before+1 then error("could not grow the blueprint book",0) end
  elseif index~=hole then
    error("slot "..tostring(index).." is free, but the engine fills slot "..tostring(hole).." first; fill that hole or omit index",0)
  end
  local target=inner[index]
  if not target.valid_for_read then
    if not target.set_stack{name="blueprint",count=1} then error("could not create a blueprint stack in slot "..tostring(index),0) end
  end
  return target
end
local function book_entry_limit(inner)
  check(used_slots(inner)<MAX_BLUEPRINT_BOOK_ENTRIES,"a blueprint book holds at most MAX_BLUEPRINT_BOOK_ENTRIES entries")
end
`

// The Lua prelude above carries the numeric bounds as literal markers so the
// Go constants stay the single source of truth.
func blueprintCatalogPrelude() string {
	replacements := [][2]string{
		{"MAX_BLUEPRINT_BOOKS", strconv.Itoa(maxBlueprintBooks)},
		{"MAX_BLUEPRINT_BOOK_ENTRIES", strconv.Itoa(maxBlueprintBookEntries)},
		{"MAX_BLUEPRINT_CATALOG_CHARACTERS", strconv.FormatInt(maxBlueprintCatalogCharacters, 10)},
	}
	prelude := blueprintCatalogLua
	for _, replacement := range replacements {
		prelude = strings.ReplaceAll(prelude, replacement[0], replacement[1])
	}
	return prelude
}

func runBlueprintCatalog(r *Resolver, body string) (string, error) {
	lua := luaFail + blueprintCatalogPrelude() + `
local ok,err=pcall(function()
` + body + `
end)
cleanup()
if not ok then fail(tostring(err)) end
`
	response, _, err := executeLua(r.addr, r.password, lua)
	if err != nil {
		return "", err
	}
	return response, nil
}

func validateBlueprintBookName(name string) error {
	if name == "" {
		return errors.New("name must not be empty")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("name must not start or end with whitespace")
	}
	if len([]rune(name)) > maxBlueprintBookNameCharacters {
		return fmt.Errorf("name must be at most %d characters", maxBlueprintBookNameCharacters)
	}
	for _, current := range name {
		if current < 0x20 || current == 0x7f {
			return errors.New("name must not contain control characters")
		}
	}
	return nil
}

func validateCatalogBlueprint(blueprint string) error {
	if blueprint == "" {
		return errors.New("blueprint must not be empty")
	}
	if len(blueprint) > maxBlueprintCharacters {
		return fmt.Errorf("blueprint exceeds %d characters", maxBlueprintCharacters)
	}
	return nil
}

// luaLabelAssignment renders an optional label or description write. A nil
// pointer leaves the field untouched, while an empty string clears it, which
// the engine stores as no label at all.
func luaLabelAssignment(target string, value *string) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%s=%s\n", target, luaString(*value))
}

func luaIndex(value int) string {
	return strconv.Itoa(value)
}

func blueprintBookList(response string) ([]*BlueprintBookSummary, error) {
	decoded, err := factoryDecode[struct {
		Tick           float64                 `json:"tick"`
		BlueprintBooks []*BlueprintBookSummary `json:"blueprintBooks"`
	}](response)
	if err != nil {
		return nil, err
	}
	if decoded.BlueprintBooks == nil {
		return []*BlueprintBookSummary{}, nil
	}
	return decoded.BlueprintBooks, nil
}

func blueprintBooks(r *Resolver) ([]*BlueprintBookSummary, error) {
	response, err := runBlueprintCatalog(r, `local books={}
for _,name in ipairs(book_names()) do
  local stored=catalog.books[name]
  local book,inner=load_book(stored.blueprint)
  books[#books+1]=summary_of(name,book,inner,stored)
end
rcon.print(helpers.table_to_json({tick=game.tick,blueprintBooks=books}))`)
	if err != nil {
		return nil, err
	}
	return blueprintBookList(response)
}

func blueprintBook(r *Resolver, name string, includeBlueprint bool) (*BlueprintBook, error) {
	if err := validateBlueprintBookName(name); err != nil {
		return nil, err
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
local summary=summary_of(%s,book,inner,stored)
summary.blueprint=stored.blueprint
summary.entries=entries_of(inner,%t)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary}))`,
		luaString(name), luaString(name), includeBlueprint))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Tick float64        `json:"tick"`
		Book *BlueprintBook `json:"book"`
	}](response)
	if err != nil {
		return nil, err
	}
	if decoded.Book == nil {
		return nil, errors.New("server returned no blueprint book")
	}
	if decoded.Book.Entries == nil {
		decoded.Book.Entries = []*BlueprintBookEntry{}
	}
	return decoded.Book, nil
}

func createBlueprintBook(r *Resolver, input CreateBlueprintBookInput) (*BlueprintBookSummary, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`check(catalog.books[%s]==nil,"blueprint book "..%s.." already exists; import with overwrite or update it instead")
local inv=new_inventory()
local book=inv[1]
check(book.set_stack{name="blueprint-book",count=1},"could not create a blueprint book stack")
%s%s
local data=export_book(book)
store_book(%s,data)
local summary=summary_of(%s,book,book.get_inventory(defines.inventory.item_main),{blueprint=data,updated=game.tick})
rcon.print(helpers.table_to_json({tick=game.tick,book=summary}))`,
		luaString(input.Name), luaString(input.Name),
		luaLabelAssignment("book.label", input.Label),
		luaLabelAssignment("book.blueprint_description", input.Description),
		luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Tick float64               `json:"tick"`
		Book *BlueprintBookSummary `json:"book"`
	}](response)
	if err != nil {
		return nil, err
	}
	if decoded.Book == nil {
		return nil, errors.New("server returned no blueprint book summary")
	}
	announceMutation(r, input.Message, fmt.Sprintf("created blueprint book %q", input.Name))
	return decoded.Book, nil
}

func importBlueprintBook(r *Resolver, input ImportBlueprintBookInput) (*BlueprintBookSummary, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if err := validateCatalogBlueprint(input.Blueprint); err != nil {
		return nil, err
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`check(%t or catalog.books[%s]==nil,"blueprint book "..%s.." already exists; pass overwrite to replace it")
local inv=new_inventory()
local book=inv[1]
local rc=book.import_stack(%s)
check(rc==0,"invalid blueprint string: "..tostring(rc))
check(book.is_blueprint_book,"blueprint string is not a blueprint book; use addBlueprintToBook for a single blueprint")
%s%s
local data=export_book(book)
store_book(%s,data)
local summary=summary_of(%s,book,book.get_inventory(defines.inventory.item_main),{blueprint=data,updated=game.tick})
rcon.print(helpers.table_to_json({tick=game.tick,book=summary}))`,
		input.Overwrite, luaString(input.Name), luaString(input.Name), luaString(input.Blueprint),
		luaLabelAssignment("book.label", input.Label),
		luaLabelAssignment("book.blueprint_description", input.Description),
		luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Tick float64               `json:"tick"`
		Book *BlueprintBookSummary `json:"book"`
	}](response)
	if err != nil {
		return nil, err
	}
	if decoded.Book == nil {
		return nil, errors.New("server returned no blueprint book summary")
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("imported blueprint book %q with %d entries", input.Name, decoded.Book.EntryCount))
	return decoded.Book, nil
}

func updateBlueprintBook(r *Resolver, input UpdateBlueprintBookInput) (*BlueprintBookSummary, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.NewName != nil {
		if err := validateBlueprintBookName(*input.NewName); err != nil {
			return nil, err
		}
	}
	if input.ActiveIndex != nil && *input.ActiveIndex < 1 {
		return nil, errors.New("activeIndex must be 1 or greater")
	}
	newName := input.Name
	if input.NewName != nil {
		newName = *input.NewName
	}
	rename := ""
	if newName != input.Name {
		rename = fmt.Sprintf(`catalog.books[%s]=nil`, luaString(input.Name))
	}
	active := ""
	if input.ActiveIndex != nil {
		active = fmt.Sprintf(`check(%[1]s>=1 and %[1]s<=#inner,"activeIndex must be between 1 and "..tostring(#inner))
check(inner[%[1]s].valid_for_read,"slot "..tostring(%[1]s).." is empty; activeIndex must hold an entry")
book.active_index=%[1]s
`, luaIndex(*input.ActiveIndex))
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
check(%t or %s==%s or catalog.books[%s]==nil,"blueprint book "..%s.." already exists; pick another newName")
local book,inner=load_book(stored.blueprint)
%s%s%s
local data=export_book(book)
%s
store_book(%s,data)
local summary=summary_of(%s,book,inner,{blueprint=data,updated=game.tick})
rcon.print(helpers.table_to_json({tick=game.tick,book=summary}))`,
		luaString(input.Name),
		newName == input.Name, luaString(newName), luaString(input.Name), luaString(newName), luaString(newName),
		luaLabelAssignment("book.label", input.Label),
		luaLabelAssignment("book.blueprint_description", input.Description),
		active, rename, luaString(newName), luaString(newName)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		Tick float64               `json:"tick"`
		Book *BlueprintBookSummary `json:"book"`
	}](response)
	if err != nil {
		return nil, err
	}
	if decoded.Book == nil {
		return nil, errors.New("server returned no blueprint book summary")
	}
	announceMutation(r, input.Message, fmt.Sprintf("updated blueprint book %q", newName))
	return decoded.Book, nil
}

func deleteBlueprintBook(r *Resolver, input DeleteBlueprintBookInput) (*BlueprintBookDeleteResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`require_book(%s)
catalog.books[%s]=nil
local count=0
for _ in pairs(catalog.books) do count=count+1 end
rcon.print(helpers.table_to_json({tick=game.tick,name=%s,book_count=count}))`,
		luaString(input.Name), luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookDeleteResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message, fmt.Sprintf("deleted blueprint book %q", input.Name))
	return decoded, nil
}

func addBlueprintToBook(r *Resolver, input AddBlueprintToBookInput) (*BlueprintBookMutationResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if err := validateCatalogBlueprint(input.Blueprint); err != nil {
		return nil, err
	}
	if input.Index != nil && *input.Index < 1 {
		return nil, errors.New("index must be 1 or greater")
	}
	index := "free_slot(inner)"
	if input.Index != nil {
		index = luaIndex(*input.Index)
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
book_entry_limit(inner)
local index=%s
local target=writable_slot(inner,index)
import_single_blueprint(target,%s)
%s
local data=export_book(book)
store_book(%s,data)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary_of(%s,book,inner,{blueprint=data,updated=game.tick}),entry=entry_of(inner,index,false)}))`,
		luaString(input.Name), index, luaString(input.Blueprint),
		luaLabelAssignment("target.label", input.Label),
		luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookMutationResult](response)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("added blueprint to book %q", input.Name)
	if decoded.Book != nil && decoded.Entry != nil {
		summary = fmt.Sprintf("added blueprint %q to slot %d of book %q", entryLabel(decoded.Entry), decoded.Entry.Index, input.Name)
	}
	announceMutation(r, input.Message, summary)
	return decoded, nil
}

func addAreaToBook(r *Resolver, input AddAreaToBookInput) (*BlueprintBookMutationResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.Area == nil {
		return nil, errors.New("area is required")
	}
	bounds := areaBounds{
		LeftTopX:     input.Area.LeftTopX,
		LeftTopY:     input.Area.LeftTopY,
		RightBottomX: input.Area.RightBottomX,
		RightBottomY: input.Area.RightBottomY,
	}
	if err := bounds.validate(); err != nil {
		return nil, err
	}
	if input.Index != nil && *input.Index < 1 {
		return nil, errors.New("index must be 1 or greater")
	}
	index := "free_slot(inner)"
	if input.Index != nil {
		index = luaIndex(*input.Index)
	}
	surface := stringOr(input.Surface, defaultSurface)
	force := stringOr(input.Force, "player")
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local s=game.surfaces[%s]; check(s~=nil,"surface not found")
local f=game.forces[%s]; check(f~=nil,"force not found")
local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
book_entry_limit(inner)
local index=%s
local target=writable_slot(inner,index)
target.create_blueprint{surface=s,force=f,area=%s,always_include_tiles=%t,include_entities=true,include_modules=%t,include_station_names=%t,include_trains=%t,include_fuel=%t}
%s
local data=export_book(book)
store_book(%s,data)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary_of(%s,book,inner,{blueprint=data,updated=game.tick}),entry=entry_of(inner,index,false)}))`,
		luaString(surface), luaString(force), luaString(input.Name), index, bounds.lua(),
		input.IncludeTiles, input.IncludeModules, input.IncludeStationNames, input.IncludeTrains, input.IncludeFuel,
		luaLabelAssignment("target.label", input.Label),
		luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookMutationResult](response)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("added area blueprint to book %q", input.Name)
	if decoded.Entry != nil {
		summary = fmt.Sprintf("added area blueprint with %d entities to slot %d of book %q",
			decoded.Entry.EntityCount, decoded.Entry.Index, input.Name)
	}
	announceMutation(r, input.Message, summary)
	return decoded, nil
}

func updateBlueprintInBook(r *Resolver, input UpdateBlueprintInBookInput) (*BlueprintBookMutationResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.Index < 1 {
		return nil, errors.New("index must be 1 or greater")
	}
	if input.Blueprint != nil {
		if err := validateCatalogBlueprint(*input.Blueprint); err != nil {
			return nil, err
		}
	}
	replace := ""
	if input.Blueprint != nil {
		replace = fmt.Sprintf("import_single_blueprint(target,%s)\n", luaString(*input.Blueprint))
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
check(%s>=1 and %s<=#inner,"index must be between 1 and "..tostring(#inner))
local target=inner[%s]
check(target.valid_for_read,"slot "..tostring(%s).." is empty; use addBlueprintToBook to fill it")
%s%s
local data=export_book(book)
store_book(%s,data)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary_of(%s,book,inner,{blueprint=data,updated=game.tick}),entry=entry_of(inner,%s,false)}))`,
		luaString(input.Name), luaIndex(input.Index), luaIndex(input.Index), luaIndex(input.Index), luaIndex(input.Index),
		replace, luaLabelAssignment("target.label", input.Label),
		luaString(input.Name), luaString(input.Name), luaIndex(input.Index)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookMutationResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("updated slot %d of blueprint book %q", input.Index, input.Name))
	return decoded, nil
}

func removeBlueprintFromBook(r *Resolver, input RemoveBlueprintFromBookInput) (*BlueprintBookMutationResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.Index < 1 {
		return nil, errors.New("index must be 1 or greater")
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
check(%s>=1 and %s<=#inner,"index must be between 1 and "..tostring(#inner))
local target=inner[%s]
check(target.valid_for_read,"slot "..tostring(%s).." is already empty")
local removed=entry_of(inner,%s,false)
target.clear()
local data=export_book(book)
store_book(%s,data)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary_of(%s,book,inner,{blueprint=data,updated=game.tick}),entry=removed}))`,
		luaString(input.Name), luaIndex(input.Index), luaIndex(input.Index), luaIndex(input.Index), luaIndex(input.Index), luaIndex(input.Index),
		luaString(input.Name), luaString(input.Name)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookMutationResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("removed slot %d from blueprint book %q", input.Index, input.Name))
	return decoded, nil
}

func moveBlueprintInBook(r *Resolver, input MoveBlueprintInBookInput) (*BlueprintBookMutationResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.FromIndex < 1 || input.ToIndex < 1 {
		return nil, errors.New("fromIndex and toIndex must be 1 or greater")
	}
	if input.FromIndex == input.ToIndex {
		return nil, errors.New("fromIndex and toIndex must differ")
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
check(%s>=1 and %s<=#inner,"fromIndex must be between 1 and "..tostring(#inner))
check(%s>=1 and %s<=#inner,"toIndex must be between 1 and "..tostring(#inner))
local source=inner[%s]
check(source.valid_for_read,"fromIndex slot "..tostring(%s).." is empty")
check(source.swap_stack(inner[%s]),"could not move the entry")
local data=export_book(book)
store_book(%s,data)
rcon.print(helpers.table_to_json({tick=game.tick,book=summary_of(%s,book,inner,{blueprint=data,updated=game.tick}),entry=entry_of(inner,%s,false)}))`,
		luaString(input.Name),
		luaIndex(input.FromIndex), luaIndex(input.FromIndex),
		luaIndex(input.ToIndex), luaIndex(input.ToIndex),
		luaIndex(input.FromIndex), luaIndex(input.FromIndex), luaIndex(input.ToIndex),
		luaString(input.Name), luaString(input.Name), luaIndex(input.ToIndex)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookMutationResult](response)
	if err != nil {
		return nil, err
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("moved slot %d to slot %d in blueprint book %q", input.FromIndex, input.ToIndex, input.Name))
	return decoded, nil
}

func placeBlueprintFromBook(r *Resolver, input PlaceBlueprintFromBookInput) (*BlueprintPlaceResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if input.Position == nil {
		return nil, errors.New("position is required")
	}
	if input.Direction < 0 || input.Direction > 15 {
		return nil, errors.New("direction must be between 0 and 15")
	}
	if input.Direction%4 != 0 {
		return nil, errors.New("direction must be 0, 4, 8 or 12; a blueprint only rotates in 90 degree steps")
	}
	if input.Index != nil && *input.Index < 1 {
		return nil, errors.New("index must be 1 or greater")
	}
	index := "book.active_index"
	if input.Index != nil {
		index = luaIndex(*input.Index)
	}
	surface := stringOr(input.Surface, defaultSurface)
	force := stringOr(input.Force, "player")
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local s=game.surfaces[%s]; check(s~=nil,"surface not found")
local f=game.forces[%s]; check(f~=nil,"force not found")
local stored=require_book(%s)
local book,inner=load_book(stored.blueprint)
local index=%s
check(index~=nil,"blueprint book "..%s.." has no active entry; pass index")
check(index>=1 and index<=#inner,"index must be between 1 and "..tostring(#inner))
local target=inner[index]
check(target.valid_for_read,"slot "..tostring(index).." is empty")
check(target.is_blueprint,"slot "..tostring(index).." holds a nested blueprint book, not a blueprint")
local ok_setup,is_setup=pcall(function() return target.is_blueprint_setup() end)
check(ok_setup,"slot "..tostring(index).." is not a single blueprint: "..tostring(is_setup))
check(is_setup,"slot "..tostring(index).." is not set up")
local entity_count=target.get_blueprint_entity_count()
local ok,built=pcall(function() return target.build_blueprint{surface=s,force=f,position={x=%s,y=%s},direction=%d,build_mode=defines.build_mode.normal,raise_built=true} end)
check(ok,"could not place blueprint: "..tostring(built))
local entities={}
for _,e in ipairs(built) do entities[#entities+1]={name=e.name,type=e.type,position=e.position,unit_number=e.unit_number} end
local skipped=entity_count-#built
if skipped<0 then skipped=0 end
check(#built>0 or entity_count==0,"blueprint placed none of "..entity_count.." entities at %s,%s; the footprint is outside the map or already occupied")
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,force=f.name,index=index,ghosts=#built,entity_count=entity_count,skipped=skipped,entities=entities}))`,
		luaString(surface), luaString(force), luaString(input.Name), index, luaString(input.Name),
		strconv.FormatFloat(input.Position.X, 'g', -1, 64), strconv.FormatFloat(input.Position.Y, 'g', -1, 64), input.Direction,
		strconv.FormatFloat(input.Position.X, 'g', -1, 64), strconv.FormatFloat(input.Position.Y, 'g', -1, 64)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[struct {
		BlueprintPlaceResult
		Index int `json:"index"`
	}](response)
	if err != nil {
		return nil, err
	}
	result := decoded.BlueprintPlaceResult
	summary := fmt.Sprintf("placed %d of %d ghosts from book %q slot %d on %s",
		result.Ghosts, result.EntityCount, input.Name, decoded.Index, result.Surface)
	if result.Skipped > 0 {
		summary = fmt.Sprintf("placed %d of %d ghosts from book %q slot %d on %s; %d skipped",
			result.Ghosts, result.EntityCount, input.Name, decoded.Index, result.Surface, result.Skipped)
	}
	announceMutation(r, input.Message, summary)
	return &result, nil
}

func giveBlueprintBook(r *Resolver, input GiveBlueprintBookInput) (*BlueprintBookGiveResult, error) {
	if err := validateBlueprintBookName(input.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Player) == "" {
		return nil, errors.New("player must not be empty")
	}
	if input.Slot != nil && *input.Slot < 1 {
		return nil, errors.New("slot must be 1 or greater")
	}
	slot := "nil"
	if input.Slot != nil {
		slot = luaIndex(*input.Slot)
	}
	response, err := runBlueprintCatalog(r, fmt.Sprintf(`local stored=require_book(%s)
local p=game.players[%s]
check(p~=nil,"player "..%s.." not found")
check(p.connected,"player "..%s.." is not connected")
local inv=p.get_main_inventory()
check(inv~=nil,"player "..%s.." has no main inventory")
local slot=%s
if slot==nil then
  for i=1,#inv do if not inv[i].valid_for_read then slot=i; break end end
  check(slot~=nil,"player "..%s.." has no empty main-inventory slot")
else
  check(slot>=1 and slot<=#inv,"slot must be between 1 and "..tostring(#inv))
end
local target=inv[slot]
if target.valid_for_read then error("slot "..tostring(slot).." already holds "..target.name.."; empty it or omit slot",0) end
local scratch_inv=new_inventory()
local book=scratch_inv[1]
local rc=book.import_stack(stored.blueprint)
check(rc==0,"catalog book is corrupt: import returned "..tostring(rc))
check(book.is_blueprint_book,"catalog string is not a blueprint book")
local inner=book.get_inventory(defines.inventory.item_main)
local entry_count=used_slots(inner)
local slot_count=#inner
local label=book.label
check(target.swap_stack(book),"could not move the blueprint book into slot "..tostring(slot))
rcon.print(helpers.table_to_json({tick=game.tick,player=p.name,slot=slot,label=label,entry_count=entry_count,slot_count=slot_count}))`,
		luaString(input.Name), luaString(input.Player),
		luaString(input.Player), luaString(input.Player), luaString(input.Player), slot, luaString(input.Player)))
	if err != nil {
		return nil, err
	}
	decoded, err := factoryDecode[BlueprintBookGiveResult](response)
	if err != nil {
		return nil, err
	}
	label := input.Name
	if decoded.Label != nil && *decoded.Label != "" {
		label = *decoded.Label
	}
	announceMutation(r, input.Message,
		fmt.Sprintf("gave blueprint book %q with %d entries to player %q in slot %d",
			label, decoded.EntryCount, decoded.Player, decoded.Slot))
	return decoded, nil
}

func entryLabel(entry *BlueprintBookEntry) string {
	if entry.Label != nil && *entry.Label != "" {
		return *entry.Label
	}
	return entry.Name
}
