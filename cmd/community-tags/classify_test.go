package main

import (
	"reflect"
	"testing"
)

func TestClassifyTagValues(t *testing.T) {
	// Two communities, as the importer leaves them: keyed by ID, and findable by
	// the Grouper name the tag was originally written with.
	communities := communityIndex{
		byID: map[string]bool{
			"78f26e8c49654deb83e710aab64a25fa": true,
			"68e324b021384e1b874b9bc3c121df57": true,
		},
		byLegacyName: map[string]string{
			"iplant:de:prod:communities:Imaging":  "78f26e8c49654deb83e710aab64a25fa",
			"iplant:de:prod:communities:Genomics": "68e324b021384e1b874b9bc3c121df57",
		},
		byName: map[string]string{
			"Imaging":  "78f26e8c49654deb83e710aab64a25fa",
			"Genomics": "68e324b021384e1b874b9bc3c121df57",
		},
	}

	tests := []struct {
		name        string
		values      []string
		wantRewrite map[string]string
		wantOrphan  []string
	}{
		{
			name:        "a legacy Grouper path rewrites to the community ID",
			values:      []string{"iplant:de:prod:communities:Imaging"},
			wantRewrite: map[string]string{"iplant:de:prod:communities:Imaging": "78f26e8c49654deb83e710aab64a25fa"},
		},
		{
			// The whole point of running it twice.
			name:        "a value that is already an ID is left alone",
			values:      []string{"78f26e8c49654deb83e710aab64a25fa"},
			wantRewrite: map[string]string{},
		},
		{
			name:        "a bare community name rewrites too",
			values:      []string{"Genomics"},
			wantRewrite: map[string]string{"Genomics": "68e324b021384e1b874b9bc3c121df57"},
		},
		{
			// 17 of production's 51 distinct values name a community that no
			// longer exists. Those are reported and left exactly as they are.
			name:        "a value naming no community is an orphan",
			values:      []string{"iplant:de:prod:communities:Vanished"},
			wantRewrite: map[string]string{},
			wantOrphan:  []string{"iplant:de:prod:communities:Vanished"},
		},
		{
			name: "a mixed set",
			values: []string{
				"iplant:de:prod:communities:Imaging",
				"68e324b021384e1b874b9bc3c121df57",
				"iplant:de:prod:communities:Vanished",
				"Genomics",
			},
			wantRewrite: map[string]string{
				"iplant:de:prod:communities:Imaging": "78f26e8c49654deb83e710aab64a25fa",
				"Genomics":                           "68e324b021384e1b874b9bc3c121df57",
			},
			wantOrphan: []string{"iplant:de:prod:communities:Vanished"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rewrite, orphan := classifyTagValues(tt.values, communities)
			if !reflect.DeepEqual(rewrite, tt.wantRewrite) {
				t.Errorf("rewrite = %v, want %v", rewrite, tt.wantRewrite)
			}
			if !reflect.DeepEqual(orphan, tt.wantOrphan) {
				t.Errorf("orphan = %v, want %v", orphan, tt.wantOrphan)
			}
		})
	}
}

// A legacy path must win over a bare name, or a community literally named
// "iplant:de:prod:communities:Imaging" could redirect another community's tags.
func TestClassifyPrefersLegacyNameOverShortName(t *testing.T) {
	communities := communityIndex{
		byID:         map[string]bool{"aaa": true, "bbb": true},
		byLegacyName: map[string]string{"iplant:de:prod:communities:Imaging": "aaa"},
		byName:       map[string]string{"Imaging": "bbb"},
	}
	rewrite, _ := classifyTagValues([]string{"iplant:de:prod:communities:Imaging"}, communities)
	if got := rewrite["iplant:de:prod:communities:Imaging"]; got != "aaa" {
		t.Fatalf("rewrote to %q, want aaa", got)
	}
}
