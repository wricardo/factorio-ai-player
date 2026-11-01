package main

import (
	"os/exec"
	"strings"
	"testing"
)

// Exercise the shared belt-line reader against engine-identity fixtures.
// get_contents reports one tile's items, so lines of different entities must all
// count while one entity exposing the same internal line twice counts once.
func TestStockBeltLines(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("standalone Lua interpreter not installed")
	}
	fixture := `
local function line(items, identity)
  return {identity=identity, get_contents=function() return items end,
    line_equals=function(other) return other.identity==identity end}
end
local function entity(lines)
  local e={lines=lines}
  e.get_max_transport_line_index=function() return #e.lines end
  e.get_transport_line=function(index) return e.lines[index] end
  return e
end
local first=entity({line({{name="coal",count=4}},"a"),line({{name="iron-plate",count=2}},"b")})
local second=entity({line({{name="coal",count=4}},"c"),line({{name="iron-plate",count=2}},"d")})
local repeated=entity({line({{name="stone",count=3}},"dup"),line({{name="stone",count=3}},"dup")})
local three=entity({line({{name="coal",count=1}},"x"),line({{name="coal",count=1}},"y"),line({{name="coal",count=1}},"z")})
`
	assertions := `
local function never() return true end
local a=stock_belt_lines(first,never)
assert(#a==2, "two distinct lines kept, got "..#a)
assert(a[1].index==1 and a[2].index==2)
local b=stock_belt_lines(second,never)
assert(#b==2, "identical content on another entity is not a duplicate, got "..#b)
local r=stock_belt_lines(repeated,never)
assert(#r==1, "one entity exposing the same line twice counts once, got "..#r)
assert(r[1].line.get_contents()[1].count==3)
local calls=0
local limited=stock_belt_lines(three,function()
  calls=calls+1
  if calls>1 then return false end
  return true
end)
assert(#limited==2, "budget exhaustion keeps the lines read so far, got "..#limited)
`
	cmd := exec.Command(lua, "-")
	cmd.Stdin = strings.NewReader(fixture + stockBeltLineLua + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stock belt line regression: %v\n%s", err, output)
	}
}

func TestStockCategoryName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		category *StockCategory
		want     string
	}{
		{"all", nil, ""},
		{"stored", stockCategoryPointer(StockCategoryStored), "stored"},
		{"in-transit", stockCategoryPointer(StockCategoryInTransit), "in-transit"},
		{"ground", stockCategoryPointer(StockCategoryGround), "ground"},
		{"player", stockCategoryPointer(StockCategoryPlayer), "player"},
		{"unknown", stockCategoryPointer(StockCategory("BOGUS")), ""},
	} {
		got, err := stockCategoryName(tc.category)
		if tc.want == "" && tc.category != nil {
			if err == nil {
				t.Fatalf("%s: expected error", tc.name)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

func stockCategoryPointer(value StockCategory) *StockCategory { return &value }

func TestStockCategoryFilterLua(t *testing.T) {
	r, commands := serveLuaOnce(t, `{"tick":1,"surface":"nauvis","force":"player","location_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"locations":{},"total_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"totals":{},"item_page_info":{"total":0,"limit":10,"offset":0,"has_more":false},"items":{}}`)
	category := StockCategoryStored
	result, err := stockQuery(r, &StockInput{Surface: "nauvis", Force: "player", Category: &category, Limit: 10, TotalsLimit: 10, ItemsLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Tick != 1 {
		t.Fatalf("tick=%v", result.Tick)
	}
	lua := <-commands
	for _, want := range []string{
		"local surface_name, force_name, item, category_filter = " + luaString("nauvis") + ", " + luaString("player") + `, "", ` + luaString("stored"),
		`if category_filter ~= "" and category ~= category_filter then return end`,
	} {
		if !strings.Contains(lua, want) {
			t.Fatalf("stock Lua missing %q\n%s", want, lua)
		}
	}
}
