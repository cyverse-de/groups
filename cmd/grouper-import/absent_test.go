package main

import (
	"reflect"
	"testing"
)

func TestClassifyAbsentGroups(t *testing.T) {
	existing := map[string]targetGroup{
		"aaa": {ID: "aaa", GroupType: "team", Name: "Field Team", LegacyName: "iplant:de:de:teams:jdoe:Field Team"},
		"bbb": {ID: "bbb", GroupType: "system", Name: "de-users", LegacyName: "iplant:de:de:users:de-users"},
		"ccc": {ID: "ccc", GroupType: "system", Name: "workshop-users"},
		"ddd": {ID: "ddd", GroupType: "community", Name: "Imaging", LegacyName: "iplant:de:de:communities:Imaging"},
	}

	tests := []struct {
		name       string
		inGrouper  []string
		wantGone   []string
		wantNative []string
	}{
		{
			name:       "everything imported is still in Grouper",
			inGrouper:  []string{"aaa", "bbb", "ddd"},
			wantGone:   nil,
			wantNative: []string{"ccc (system/workshop-users)"},
		},
		{
			name:      "an imported group disappeared from Grouper",
			inGrouper: []string{"aaa", "bbb"},
			// Only the group with a legacy name counts as gone; apps creates
			// workshop-users natively during the soak and it was never there.
			wantGone:   []string{"ddd (community/Imaging)"},
			wantNative: []string{"ccc (system/workshop-users)"},
		},
		{
			name:       "nothing read from Grouper at all",
			inGrouper:  nil,
			wantGone:   []string{"aaa (team/Field Team)", "bbb (system/de-users)", "ddd (community/Imaging)"},
			wantNative: []string{"ccc (system/workshop-users)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			present := map[string]bool{}
			for _, id := range tt.inGrouper {
				present[id] = true
			}

			gone, native := classifyAbsentGroups(existing, present)
			if !reflect.DeepEqual(gone, tt.wantGone) {
				t.Errorf("gone = %v, want %v", gone, tt.wantGone)
			}
			if !reflect.DeepEqual(native, tt.wantNative) {
				t.Errorf("native = %v, want %v", native, tt.wantNative)
			}
		})
	}
}
