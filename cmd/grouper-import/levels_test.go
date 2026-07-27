package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCollapseGrants covers a subject holding more than one privilege on the
// same group. The permissions API is keyed by (resource, subject), so two grants
// for one subject would overwrite each other -- and which one survived would
// depend on map iteration order, making two runs disagree.
func TestCollapseGrants(t *testing.T) {
	user := func(id, level string) grantSpec {
		return grantSpec{SubjectType: subjectTypeUser, SubjectID: id, Level: level}
	}

	tests := []struct {
		name string
		in   []grantSpec
		want []grantSpec
	}{
		{
			name: "nothing to collapse",
			in:   []grantSpec{user("alice", levelRead), user("bob", levelAdmin)},
			want: []grantSpec{user("alice", levelRead), user("bob", levelAdmin)},
		},
		{
			// The common case: a collaborator-list owner also holds admins.
			name: "own beats admin",
			in:   []grantSpec{user("alice", levelAdmin), user("alice", levelOwn)},
			want: []grantSpec{user("alice", levelOwn)},
		},
		{
			name: "own beats admin regardless of order",
			in:   []grantSpec{user("alice", levelOwn), user("alice", levelAdmin)},
			want: []grantSpec{user("alice", levelOwn)},
		},
		{
			name: "admin beats read",
			in:   []grantSpec{user("alice", levelRead), user("alice", levelAdmin)},
			want: []grantSpec{user("alice", levelAdmin)},
		},
		{
			name: "a group subject collapses independently of a user with the same id",
			in: []grantSpec{
				user("shared", levelRead),
				{SubjectType: subjectTypeGroup, SubjectID: "shared", Level: levelAdmin},
			},
			want: []grantSpec{
				user("shared", levelRead),
				{SubjectType: subjectTypeGroup, SubjectID: "shared", Level: levelAdmin},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := map[grantSpec]bool{}
			for _, g := range tt.in {
				set[g] = true
			}
			got := collapseGrants(set)

			gotSlice := make([]grantSpec, 0, len(got))
			for g := range got {
				gotSlice = append(gotSlice, g)
			}
			assert.ElementsMatch(t, tt.want, gotSlice)
		})
	}
}
