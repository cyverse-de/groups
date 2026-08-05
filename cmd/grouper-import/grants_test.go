package main

import (
	"context"
	"testing"

	"github.com/cyverse-de/groups/permissions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPermClient records grants and revokes and serves a fixed permission list.
type mockPermClient struct {
	have    []permissions.Permission
	granted []grantSpec
	revoked []grantSpec
}

var _ permissions.Client = (*mockPermClient)(nil)

func (m *mockPermClient) EnsureResourceType(context.Context, string, string) error { return nil }

func (m *mockPermClient) Grant(_ context.Context, _, _, subjectType, subjectID, level string) error {
	m.granted = append(m.granted, grantSpec{SubjectType: subjectType, SubjectID: subjectID, Level: level})
	return nil
}

func (m *mockPermClient) Revoke(_ context.Context, _, _, subjectType, subjectID string) error {
	m.revoked = append(m.revoked, grantSpec{SubjectType: subjectType, SubjectID: subjectID})
	return nil
}

func (m *mockPermClient) Check(context.Context, string, string, string, string, string, bool) (bool, error) {
	return false, nil
}

func (m *mockPermClient) ListResource(context.Context, string, string) ([]permissions.Permission, error) {
	return m.have, nil
}

func (m *mockPermClient) DeleteResource(context.Context, string, string) error { return nil }

func heldPermission(subjectType, subjectID, level string) permissions.Permission {
	return permissions.Permission{
		Subject:         permissions.Subject{SubjectID: subjectID, SubjectType: subjectType},
		PermissionLevel: level,
	}
}

// applyGrants reconciles one group's grants against what Grouper justifies:
// grant what is missing, revoke what nothing wants -- but never revoke a
// subject whose level merely changed, because the grant has already replaced
// the old level and revoking would strip the subject entirely.
func TestApplyGrants(t *testing.T) {
	tests := []struct {
		name        string
		have        []permissions.Permission
		want        []grantSpec
		wantGranted []grantSpec
		wantRevoked []grantSpec
		wantAdded   int
		wantRemoved int
	}{
		{
			name:        "a missing grant is granted",
			want:        []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelAdmin}},
			wantGranted: []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelAdmin}},
			wantAdded:   1,
		},
		{
			name:        "an unjustified grant is revoked",
			have:        []permissions.Permission{heldPermission(subjectTypeUser, "carol", levelRead)},
			wantRevoked: []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "carol"}},
			wantRemoved: 1,
		},
		{
			name:        "a level change grants the new level and does not revoke",
			have:        []permissions.Permission{heldPermission(subjectTypeUser, "bob", levelAdmin)},
			want:        []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelOwn}},
			wantGranted: []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelOwn}},
			wantAdded:   1,
		},
		{
			name: "an already-satisfied grant does nothing",
			have: []permissions.Permission{heldPermission(subjectTypeUser, "bob", levelAdmin)},
			want: []grantSpec{{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelAdmin}},
		},
		{
			name: "grants and revokes are counted across subjects",
			have: []permissions.Permission{
				heldPermission(subjectTypeUser, "bob", levelAdmin),
				heldPermission(subjectTypeUser, "carol", levelRead),
				heldPermission(subjectTypeGroup, grouperAllSubject, levelRead),
			},
			want: []grantSpec{
				{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelOwn},
				{SubjectType: subjectTypeUser, SubjectID: "dave", Level: levelAdmin},
			},
			wantGranted: []grantSpec{
				{SubjectType: subjectTypeUser, SubjectID: "bob", Level: levelOwn},
				{SubjectType: subjectTypeUser, SubjectID: "dave", Level: levelAdmin},
			},
			wantRevoked: []grantSpec{
				{SubjectType: subjectTypeUser, SubjectID: "carol"},
				{SubjectType: subjectTypeGroup, SubjectID: grouperAllSubject},
			},
			wantAdded:   2,
			wantRemoved: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockPermClient{have: tt.have}
			rep := newReport()
			pg := parsedGroup{grouper: grouperGroup{ID: "abc123", Name: "iplant:de:prod:teams:alice:x"}}

			want := make(map[grantSpec]bool, len(tt.want))
			for _, spec := range tt.want {
				want[spec] = true
			}

			require.NoError(t, applyGrants(t.Context(), client, pg, want, rep))

			assert.ElementsMatch(t, tt.wantGranted, client.granted)
			assert.ElementsMatch(t, tt.wantRevoked, client.revoked)
			assert.Equal(t, tt.wantAdded, rep.GrantsAdded, "GrantsAdded")
			assert.Equal(t, tt.wantRemoved, rep.GrantsRemoved, "GrantsRemoved")
		})
	}
}
