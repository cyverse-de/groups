package main

import (
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testGrouperAdmin = "de_grouper"

// TestClassifyPrivilege covers the Grouper privilege vocabulary as it actually
// appears in production, measured over iplant:de:prod: with membership_type
// 'immediate'. Effective rows are excluded by the caller: they are Grouper's own
// expansion of nested groups, which the permissions service reproduces by
// expanding group membership itself.
func TestClassifyPrivilege(t *testing.T) {
	tests := []struct {
		name string
		// The Grouper privilege row.
		list    string
		subject string
		source  string

		// Production count, for the record.
		count int

		wantLevel   string
		wantType    string
		wantSubject string
		wantDropped bool
	}{
		{
			name: "a user administering a group", list: "admins", subject: "alice", source: "ldap",
			count: 2920, wantLevel: levelAdmin, wantType: subjectTypeUser, wantSubject: "alice",
		},
		{
			name: "a group administering a group", list: "admins", subject: "e3339a35", source: "g:gsa",
			count: 7, wantLevel: levelAdmin, wantType: subjectTypeGroup, wantSubject: "e3339a35",
		},
		{
			// It uses the admin.users bypass instead, and importing 397 grants
			// for it would be noise.
			name: "the Grouper service account", list: "admins", subject: testGrouperAdmin, source: "ldap",
			count: 397, wantDropped: true,
		},
		{
			name: "a user reading a group", list: "readers", subject: "bob", source: "ldap",
			count: 1579, wantLevel: levelRead, wantType: subjectTypeUser, wantSubject: "bob",
		},
		{
			name: "a group reading a group", list: "readers", subject: "adae7118", source: "g:gsa",
			count: 6, wantLevel: levelRead, wantType: subjectTypeGroup, wantSubject: "adae7118",
		},
		{
			// The three shapes of the public marker. GrouperAll is a group-typed
			// subject with no group row, and terrain reads it as "public".
			name: "a public community by readers", list: "readers", subject: grouperAllSubject, source: "g:isa",
			count: 55, wantLevel: levelRead, wantType: subjectTypeGroup, wantSubject: grouperAllSubject,
		},
		{
			name: "a public community by optins", list: "optins", subject: grouperAllSubject, source: "g:isa",
			count: 55, wantLevel: levelRead, wantType: subjectTypeGroup, wantSubject: grouperAllSubject,
		},
		{
			name: "a public team by viewers", list: "viewers", subject: grouperAllSubject, source: "g:isa",
			count: 183, wantLevel: levelRead, wantType: subjectTypeGroup, wantSubject: grouperAllSubject,
		},
		{
			// No equivalent: membership already implies the ability to leave.
			name: "a user opted in", list: "optins", subject: "carol", source: "ldap",
			count: 51, wantDropped: true,
		},
		{
			name: "a user opted out", list: "optouts", subject: "dave", source: "ldap",
			count: 1472, wantDropped: true,
		},
		{
			name: "a group opted out", list: "optouts", subject: "4220400d", source: "g:gsa",
			count: 6, wantDropped: true,
		},
		{
			// Production has none, but an unknown privilege must be reported
			// rather than silently discarded.
			name: "an unrecognized privilege", list: "updaters", subject: "erin", source: "ldap",
			wantDropped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := classifyPrivilege(tt.list, tt.subject, tt.source, testGrouperAdmin)

			if tt.wantDropped {
				assert.Nil(t, got)
				assert.NotEmpty(t, reason, "a dropped privilege must carry a reason for the report")
				return
			}

			require.NotNil(t, got, "reason: %s", reason)
			assert.Empty(t, reason)
			assert.Equal(t, tt.wantLevel, got.Level)
			assert.Equal(t, tt.wantType, got.SubjectType)
			assert.Equal(t, tt.wantSubject, got.SubjectID)
		})
	}
}

// TestClassifyPrivilegeUnknownSource guards the abort-by-default rule: a subject
// source we have never seen must not be guessed at.
func TestClassifyPrivilegeUnknownSource(t *testing.T) {
	got, reason := classifyPrivilege("admins", "someone", "jdbc", testGrouperAdmin)
	assert.Nil(t, got)
	assert.Contains(t, reason, "jdbc")
}

// TestOwnerGrant covers the synthesized grant that lets a namespace owner delete
// their own group, which Grouper expressed as folder structure rather than as a
// privilege.
func TestOwnerGrant(t *testing.T) {
	t.Run("a type with an owner", func(t *testing.T) {
		g := ownerGrant(&ParsedName{GroupType: model.GroupTypeTeam, Owner: "mszens", Name: "MADI"})
		require.NotNil(t, g)
		assert.Equal(t, levelOwn, g.Level)
		assert.Equal(t, subjectTypeUser, g.SubjectType)
		assert.Equal(t, "mszens", g.SubjectID)
	})

	t.Run("a community has no owner to grant to", func(t *testing.T) {
		assert.Nil(t, ownerGrant(&ParsedName{GroupType: model.GroupTypeCommunity, Name: "NEON"}))
	})

	t.Run("a system group has no owner to grant to", func(t *testing.T) {
		assert.Nil(t, ownerGrant(&ParsedName{GroupType: model.GroupTypeSystem, Name: "de-users"}))
	})
}

func TestMembersPublicPrivilege(t *testing.T) {
	tests := []struct {
		name      string
		listName  string
		subjectID string
		want      bool
	}{
		{name: "readers held by GrouperAll, as a public community", listName: "readers", subjectID: "GrouperAll", want: true},
		{name: "viewers held by GrouperAll, as a public team", listName: "viewers", subjectID: "GrouperAll", want: false},
		{name: "optins marks joinable, not readable", listName: "optins", subjectID: "GrouperAll", want: false},
		{name: "readers held by a real user is an ordinary grant", listName: "readers", subjectID: "alice", want: false},
		{name: "admins is unrelated", listName: "admins", subjectID: "GrouperAll", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, membersPublicPrivilege(tt.listName, tt.subjectID))
		})
	}
}
