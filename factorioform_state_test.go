package main

import (
	"strings"
	"testing"
)

func TestFactorioformValidatesBeforeRCON(t *testing.T) {
	offline := &Resolver{addr: "127.0.0.1:1"}
	unit := "5"
	cases := []struct {
		name string
		run  func() error
		want string
	}{
		{"state bad name", func() error { n := "Bad Name"; _, err := factorioformStates(offline, &n); return err }, "lowercase"},
		{"import empty name", func() error {
			_, err := factorioformImport(offline, FactorioformImportInput{Address: "a.b", Target: &EntityRefInput{UnitNumber: &unit}})
			return err
		}, "name must be"},
		{"import long address", func() error {
			_, err := factorioformImport(offline, FactorioformImportInput{Name: "m", Address: strings.Repeat("a", maxFactorioformAddressCharacters+1), Target: &EntityRefInput{UnitNumber: &unit}})
			return err
		}, "at most"},
		{"import no target", func() error {
			_, err := factorioformImport(offline, FactorioformImportInput{Name: "m", Address: "a.b"})
			return err
		}, "target"},
		{"import other surface", func() error {
			other := "space"
			_, err := factorioformImport(offline, FactorioformImportInput{Name: "m", Address: "a.b", Target: &EntityRefInput{UnitNumber: &unit, Surface: &other}})
			return err
		}, "differs"},
		{"forget bad address", func() error {
			_, err := factorioformForget(offline, FactorioformForgetInput{Name: "m", Addresses: []string{" x"}})
			return err
		}, "whitespace"},
	}
	for _, c := range cases {
		if err := c.run(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestFactorioformStateDecode(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":9,"manifest":{"name":"m","surface":"nauvis","force":"player","updatedTick":7,"resourceCount":1,"resources":[{"address":"a.b[0]","unitNumber":12,"entity":"wooden-chest","position":{"x":1.5,"y":2.5},"direction":0,"quality":"normal"}]}}`,
		`{"tick":9,"manifests":[{"name":"m","surface":"nauvis","force":"player","updatedTick":7,"resourceCount":1,"resources":[]}]}`,
		`{"tick":9,"manifests":[]}`,
	)
	name := "m"
	got, err := factorioformStates(fake.resolver, &name)
	if err != nil || len(got) != 1 || len(got[0].Resources) != 1 || got[0].Resources[0].UnitNumber != "12" || got[0].Resources[0].Position.X != 1.5 {
		t.Fatalf("named state = %+v err=%v", got, err)
	}
	got, err = factorioformStates(fake.resolver, nil)
	if err != nil || len(got) != 1 || got[0].ResourceCount != 1 || len(got[0].Resources) != 0 {
		t.Fatalf("summaries = %+v err=%v", got, err)
	}
	got, err = factorioformStates(fake.resolver, nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty summaries = %+v err=%v", got, err)
	}
	cmds := fake.commands()
	if !strings.Contains(cmds[0], "ff_get(") || !strings.Contains(cmds[1], "ff_list()") {
		t.Fatalf("unexpected commands %q", cmds)
	}
}

func TestFactorioformImportAndForget(t *testing.T) {
	fake := newFakeRCON(t,
		`{"tick":9,"replacedUnitNumber":"4","resource":{"address":"a.b","unitNumber":12,"entity":"wooden-chest","position":{"x":1.5,"y":2.5},"direction":0,"quality":"normal"},"manifest":{"name":"m","surface":"nauvis","force":"player","updatedTick":9,"resourceCount":1,"resources":[]}}`,
		`{"tick":0}`,
		`{"error":"address a.b is already mapped to unit 4; pass overwrite:true to remap it"}`,
		`{"tick":9,"name":"m","forgotten":["a.b"],"missing":["x"],"manifestDeleted":false,"remaining":0}`,
		`{"tick":0}`,
		`{"tick":9,"name":"m","forgotten":[],"missing":[],"manifestDeleted":true,"remaining":0}`,
		`{"tick":0}`,
	)
	unit := "12"
	res, err := factorioformImport(fake.resolver, FactorioformImportInput{Name: "m", Address: "a.b", Overwrite: true, Target: &EntityRefInput{UnitNumber: &unit}})
	if err != nil || res.Resource.UnitNumber != "12" || res.ReplacedUnitNumber == nil || *res.ReplacedUnitNumber != "4" {
		t.Fatalf("import = %+v err=%v", res, err)
	}
	if _, err = factorioformImport(fake.resolver, FactorioformImportInput{Name: "m", Address: "a.b", Target: &EntityRefInput{UnitNumber: &unit}}); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("refusal err = %v", err)
	}
	forget, err := factorioformForget(fake.resolver, FactorioformForgetInput{Name: "m", Addresses: []string{"a.b", "x", "a.b"}})
	if err != nil || len(forget.Forgotten) != 1 || len(forget.Missing) != 1 {
		t.Fatalf("forget = %+v err=%v", forget, err)
	}
	forget, err = factorioformForget(fake.resolver, FactorioformForgetInput{Name: "m"})
	if err != nil || !forget.ManifestDeleted {
		t.Fatalf("forget all = %+v err=%v", forget, err)
	}
	cmds := fake.commands()
	if !strings.Contains(cmds[0], "ff_put(") {
		t.Errorf("import command lacks ff_put")
	}
	if strings.Count(cmds[4], "\\") == 0 {
		t.Errorf("forget command not escaped: %q", cmds[4])
	}
}

func TestFactorioformStatePreludeBounds(t *testing.T) {
	prelude := factorioformStatePrelude()
	if strings.Contains(prelude, "MAX_FACTORIOFORM") {
		t.Fatal("cap markers left unreplaced")
	}
	if !strings.Contains(prelude, "<200") || !strings.Contains(prelude, "<=20000") {
		t.Fatal("caps not injected")
	}
}

func TestFactorioformPutDeleteHelpers(t *testing.T) {
	fake := newFakeRCON(t, `{"tick":1}`, `{"tick":1}`)
	err := ffStatePut(fake.resolver, "m", "nauvis", "player", []ffStateEntry{{Address: "a.b", Unit: 3, Entity: "wooden-chest", X: 1.5, Y: 2.5, Quality: "normal"}}, []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ffStateDelete(fake.resolver, "m"); err != nil {
		t.Fatal(err)
	}
	if err := ffStatePut(&Resolver{addr: "127.0.0.1:1"}, "m", "nauvis", "player", []ffStateEntry{{Address: "x"}}, nil); err == nil || !strings.Contains(err.Error(), "unit number") {
		t.Fatalf("zero unit err = %v", err)
	}
}
