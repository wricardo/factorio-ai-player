package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeconstructionAreaRejectsInvalidBoundsBeforeRCON(t *testing.T) {
	tests := []string{
		`mutation { deconstructArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:257,rightBottomY:1}}) { orders } }`,
		`mutation { cancelDeconstructArea(input:{area:{leftTopX:0,leftTopY:0,rightBottomX:0,rightBottomY:1}}) { orders } }`,
	}
	for _, mutation := range tests {
		var out bytes.Buffer
		err := runWithCredentials([]string{"mutation", mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), "area") {
			t.Errorf("expected invalid-area error for %s, got %v", mutation, err)
		}
	}
}

func TestDeconstructionEntitiesValidateTargetsBeforeRCON(t *testing.T) {
	tests := []struct {
		mutation string
		want     string
	}{
		{`mutation { deconstructEntities(input:{targets:[]}) { orders } }`, "targets"},
		{`mutation { deconstructEntities(input:{targets:[{}]}) { orders } }`, "entity name or unitNumber"},
		{`mutation { deconstructEntities(input:{targets:[{entity:"transport-belt",x:1,y:2,unitNumber:"3"}]}) { orders } }`, "not both"},
		{`mutation { cancelDeconstructEntities(input:{targets:[{entity:"transport-belt",x:1}]}) { orders } }`, "both x and y"},
		{`mutation { deconstructEntities(input:{force:"",targets:[{unitNumber:"3"}]}) { orders } }`, "force"},
	}
	for _, test := range tests {
		var out bytes.Buffer
		err := runWithCredentials([]string{"mutation", test.mutation}, &out, "")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("expected %q error for %s, got %v", test.want, test.mutation, err)
		}
	}
}
