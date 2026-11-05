package main

import (
	"strings"
	"testing"
)

func TestBlueprintCatalogValidatesBeforeRCON(t *testing.T) {
	longName := strings.Repeat("a", maxBlueprintBookNameCharacters+1)
	longBlueprint := "0" + strings.Repeat("A", maxBlueprintCharacters)
	badArea := &FactoryAreaInput{LeftTopX: 0, LeftTopY: 0, RightBottomX: maxAreaTiles + 1, RightBottomY: 1}
	zero := 0
	one := 1
	direction := 45
	empty := ""
	tests := []struct {
		name string
		run  func(*Resolver) error
		want string
	}{
		{
			name: "create empty name",
			run: func(r *Resolver) error {
				_, err := createBlueprintBook(r, CreateBlueprintBookInput{Name: " "})
				return err
			},
			want: "whitespace",
		},
		{
			name: "create long name",
			run: func(r *Resolver) error {
				_, err := createBlueprintBook(r, CreateBlueprintBookInput{Name: longName})
				return err
			},
			want: "at most",
		},
		{
			name: "import empty blueprint",
			run: func(r *Resolver) error {
				_, err := importBlueprintBook(r, ImportBlueprintBookInput{Name: "book", Blueprint: ""})
				return err
			},
			want: "must not be empty",
		},
		{
			name: "import long blueprint",
			run: func(r *Resolver) error {
				_, err := importBlueprintBook(r, ImportBlueprintBookInput{Name: "book", Blueprint: longBlueprint})
				return err
			},
			want: "exceeds",
		},
		{
			name: "update active index",
			run: func(r *Resolver) error {
				_, err := updateBlueprintBook(r, UpdateBlueprintBookInput{Name: "book", ActiveIndex: &zero})
				return err
			},
			want: "activeIndex",
		},
		{
			name: "update new name",
			run: func(r *Resolver) error {
				padded := " padded "
				_, err := updateBlueprintBook(r, UpdateBlueprintBookInput{Name: "book", NewName: &padded})
				return err
			},
			want: "whitespace",
		},
		{
			name: "add zero index",
			run: func(r *Resolver) error {
				_, err := addBlueprintToBook(r, AddBlueprintToBookInput{Name: "book", Blueprint: "0eNpj", Index: &zero})
				return err
			},
			want: "index",
		},
		{
			name: "add empty blueprint",
			run: func(r *Resolver) error {
				_, err := addBlueprintToBook(r, AddBlueprintToBookInput{Name: "book", Blueprint: ""})
				return err
			},
			want: "must not be empty",
		},
		{
			name: "add area missing",
			run: func(r *Resolver) error {
				_, err := addAreaToBook(r, AddAreaToBookInput{Name: "book"})
				return err
			},
			want: "area",
		},
		{
			name: "add area too large",
			run: func(r *Resolver) error {
				_, err := addAreaToBook(r, AddAreaToBookInput{Name: "book", Area: badArea})
				return err
			},
			want: "area",
		},
		{
			name: "update entry zero index",
			run: func(r *Resolver) error {
				_, err := updateBlueprintInBook(r, UpdateBlueprintInBookInput{Name: "book", Index: 0})
				return err
			},
			want: "index",
		},
		{
			name: "update entry empty blueprint",
			run: func(r *Resolver) error {
				_, err := updateBlueprintInBook(r, UpdateBlueprintInBookInput{Name: "book", Index: 1, Blueprint: &empty})
				return err
			},
			want: "must not be empty",
		},
		{
			name: "remove zero index",
			run: func(r *Resolver) error {
				_, err := removeBlueprintFromBook(r, RemoveBlueprintFromBookInput{Name: "book", Index: 0})
				return err
			},
			want: "index",
		},
		{
			name: "move same index",
			run: func(r *Resolver) error {
				_, err := moveBlueprintInBook(r, MoveBlueprintInBookInput{Name: "book", FromIndex: 2, ToIndex: 2})
				return err
			},
			want: "must differ",
		},
		{
			name: "place missing position",
			run: func(r *Resolver) error {
				_, err := placeBlueprintFromBook(r, PlaceBlueprintFromBookInput{Name: "book"})
				return err
			},
			want: "position",
		},
		{
			name: "place direction",
			run: func(r *Resolver) error {
				_, err := placeBlueprintFromBook(r, PlaceBlueprintFromBookInput{Name: "book", Position: &PositionInput{}, Direction: direction})
				return err
			},
			want: "between 0 and 15",
		},
		{
			name: "place zero index",
			run: func(r *Resolver) error {
				_, err := placeBlueprintFromBook(r, PlaceBlueprintFromBookInput{Name: "book", Position: &PositionInput{}, Index: &zero})
				return err
			},
			want: "index",
		},
		{
			name: "read bad name",
			run: func(r *Resolver) error {
				_, err := blueprintBook(r, "", false)
				return err
			},
			want: "must not be empty",
		},
		{
			name: "delete bad name",
			run: func(r *Resolver) error {
				_, err := deleteBlueprintBook(r, DeleteBlueprintBookInput{Name: ""})
				return err
			},
			want: "must not be empty",
		},
		{
			name: "move zero index",
			run: func(r *Resolver) error {
				_, err := moveBlueprintInBook(r, MoveBlueprintInBookInput{Name: "book", FromIndex: 0, ToIndex: one})
				return err
			},
			want: "fromIndex",
		},
		{
			name: "give bad name",
			run: func(r *Resolver) error {
				_, err := giveBlueprintBook(r, GiveBlueprintBookInput{Name: "", Player: "wricardo"})
				return err
			},
			want: "must not be empty",
		},
		{
			name: "give empty player",
			run: func(r *Resolver) error {
				_, err := giveBlueprintBook(r, GiveBlueprintBookInput{Name: "book", Player: " "})
				return err
			},
			want: "player must not be empty",
		},
		{
			name: "give zero slot",
			run: func(r *Resolver) error {
				zeroSlot := 0
				_, err := giveBlueprintBook(r, GiveBlueprintBookInput{Name: "book", Player: "wricardo", Slot: &zeroSlot})
				return err
			},
			want: "slot",
		},
	}
	for _, test := range tests {
		r := &Resolver{addr: "127.0.0.1:1", password: "password"}
		if err := test.run(r); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: expected %q error, got %v", test.name, test.want, err)
		}
	}
}

func TestBlueprintCatalogListsAndReadsBooks(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":7,"blueprintBooks":[{"name":"main","label":"Main","description":"shared","slot_count":4,"entry_count":3,"active_index":2,"characters":512,"updated_tick":6}]}`,
		`{"tick":8,"book":{"name":"main","label":null,"description":"d","slot_count":2,"entry_count":1,"active_index":1,"characters":300,"updated_tick":8,"blueprint":"0eNqBook","entries":[{"index":1,"name":"blueprint","label":null,"entity_count":5,"tile_count":2,"blueprint":"0eNqBp"},{"index":2,"name":"blueprint-book","label":"nested","entity_count":0,"tile_count":0}]}}`,
	)
	books, err := blueprintBooks(fake.resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %+v", books)
	}
	book := books[0]
	if book.Name != "main" || book.Label == nil || *book.Label != "Main" || book.SlotCount != 4 || book.EntryCount != 3 {
		t.Fatalf("book = %+v", book)
	}
	if book.ActiveIndex == nil || *book.ActiveIndex != 2 || book.Characters != 512 || book.UpdatedTick != 6 {
		t.Fatalf("book = %+v", book)
	}

	read, err := blueprintBook(fake.resolver, "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if read.Name != "main" || read.Description == nil || *read.Description != "d" || read.Blueprint != "0eNqBook" {
		t.Fatalf("read = %+v", read)
	}
	if len(read.Entries) != 2 {
		t.Fatalf("entries = %+v", read.Entries)
	}
	first := read.Entries[0]
	if first.Index != 1 || first.EntityCount != 5 || first.TileCount != 2 || first.Blueprint == nil || *first.Blueprint != "0eNqBp" {
		t.Fatalf("first = %+v", first)
	}
	nested := read.Entries[1]
	if nested.Name != "blueprint-book" || nested.Label == nil || *nested.Label != "nested" || nested.Blueprint != nil {
		t.Fatalf("nested = %+v", nested)
	}
}

func TestBlueprintCatalogEmptyBookList(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":1,"blueprintBooks":[]}`)
	books, err := blueprintBooks(fake.resolver)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 0 {
		t.Fatalf("books = %+v", books)
	}
}

func TestGiveBlueprintBookToPlayer(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":9,"player":"wricardo","slot":4,"label":"Balancers","entry_count":13,"slot_count":13}`,
		"",
	)
	result, err := giveBlueprintBook(fake.resolver, GiveBlueprintBookInput{Name: "Balancers", Player: "wricardo"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Player != "wricardo" || result.Slot != 4 || result.EntryCount != 13 || result.SlotCount != 13 {
		t.Fatalf("result = %+v", result)
	}
	if result.Label == nil || *result.Label != "Balancers" {
		t.Fatalf("label = %+v", result.Label)
	}
	commands := fake.commands()
	if len(commands) == 0 || !strings.Contains(commands[0], "swap_stack(book)") || !strings.Contains(commands[0], "stored.blueprint") {
		t.Fatalf("give command = %q", commands)
	}
}
