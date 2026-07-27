package main

import (
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func team(id, owner, name string) parsedGroup {
	return parsedGroup{
		grouper: grouperGroup{ID: id, Name: "iplant:de:prod:teams:" + owner + ":" + name},
		parsed:  &ParsedName{GroupType: model.GroupTypeTeam, Owner: owner, Name: name},
	}
}

// TestResolveCollisions covers two Grouper groups landing on the same identity
// after the case- and whitespace-insensitive normalization the unique index
// applies. Production has exactly one such pair, and both sides are empty.
func TestResolveCollisions(t *testing.T) {
	tests := []struct {
		name    string
		groups  []parsedGroup
		content map[string]bool

		wantSkipped []string
		wantErr     bool
	}{
		{
			name:   "no collisions",
			groups: []parsedGroup{team("a", "sriram", "Alpha"), team("b", "sriram", "Beta")},
		},
		{
			// The production case: iplant:de:prod:teams:sriram:{Sriram Team,sriram team}.
			name:        "both sides empty, the survivor is deterministic",
			groups:      []parsedGroup{team("b2", "sriram", "sriram team"), team("a1", "sriram", "Sriram Team")},
			wantSkipped: []string{"b2"},
		},
		{
			name:        "one side has content and survives",
			groups:      []parsedGroup{team("a1", "sriram", "Sriram Team"), team("b2", "sriram", "sriram team")},
			content:     map[string]bool{"b2": true},
			wantSkipped: []string{"a1"},
		},
		{
			// Picking a winner here is a human decision.
			name:    "both sides have content",
			groups:  []parsedGroup{team("a1", "sriram", "Sriram Team"), team("b2", "sriram", "sriram team")},
			content: map[string]bool{"a1": true, "b2": true},
			wantErr: true,
		},
		{
			name:        "whitespace and case both normalize",
			groups:      []parsedGroup{team("a1", "Sriram", "The  Team"), team("b2", "sriram", "the team")},
			wantSkipped: []string{"b2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipped, err := resolveCollisions(tt.groups, func(pg parsedGroup) bool {
				return tt.content[pg.grouper.ID]
			})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := make([]string, 0, len(skipped))
			for id := range skipped {
				got = append(got, id)
			}
			assert.ElementsMatch(t, tt.wantSkipped, got)
		})
	}
}

// TestResolveCollisionsIsDeterministic guards the re-run requirement: two runs
// must agree on which group exists, so the survivor cannot depend on input order.
func TestResolveCollisionsIsDeterministic(t *testing.T) {
	forward := []parsedGroup{team("b2", "sriram", "sriram team"), team("a1", "sriram", "Sriram Team")}
	reverse := []parsedGroup{team("a1", "sriram", "Sriram Team"), team("b2", "sriram", "sriram team")}
	none := func(parsedGroup) bool { return false }

	first, err := resolveCollisions(forward, none)
	require.NoError(t, err)
	second, err := resolveCollisions(reverse, none)
	require.NoError(t, err)

	assert.Equal(t, first, second, "the survivor must not depend on the order Grouper returned rows")
}
