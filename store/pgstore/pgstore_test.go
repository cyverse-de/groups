package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests need a PostgreSQL database with the DE schema applied, and they
// are DESTRUCTIVE: setup deletes every group in the target schema. Point them
// at a scratch database only.
//
//	GROUPS_TEST_DB_DESTROYS_GROUPS=yes \
//	GROUPS_TEST_DB_URI=postgres://postgres:test@localhost:5433/de?sslmode=disable \
//	  go test ./store/pgstore/
//
// The acknowledgement is separate from the URI on purpose. Setting the URI is
// the convenient thing to do while working against a running deployment, and
// nothing about it says the suite will empty the groups tables -- which it has
// done to a live cluster, twice, silently invalidating whatever was being
// checked at the time.
func testStore(t *testing.T) *Store {
	t.Helper()

	uri := os.Getenv("GROUPS_TEST_DB_URI")
	if uri == "" {
		t.Skip("GROUPS_TEST_DB_URI is not set")
	}
	if os.Getenv("GROUPS_TEST_DB_DESTROYS_GROUPS") != "yes" {
		t.Fatal("refusing to run: this suite deletes every group in the target database. " +
			"Set GROUPS_TEST_DB_DESTROYS_GROUPS=yes to confirm the database is disposable.")
	}

	s, err := Open(t.Context(), Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, s.Close()) })

	// A group carrying legacy_name came from a Grouper import, which no test
	// performs -- so the target is a real deployment's database rather than a
	// scratch one, and the acknowledgement was given for the wrong database.
	var imported int
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM groups WHERE legacy_name IS NOT NULL`).Scan(&imported))
	if imported > 0 {
		t.Fatalf("refusing to run: %d imported groups are present, so this is not a scratch "+
			"database. Deleting them would discard data the tests cannot recreate.", imported)
	}

	// Group data is rebuilt per test. Deleting the group subjects cascades to
	// groups, membership, and the closure.
	_, err = s.db.ExecContext(t.Context(), `DELETE FROM subjects WHERE subject_type = 'group'`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(),
		`DELETE FROM subjects WHERE subject_type = 'user' AND subject_id LIKE 'test-%'`)
	require.NoError(t, err)

	return s
}

// mustCreate creates a group and returns it, failing the test on error.
func mustCreate(t *testing.T, s *Store, spec model.GroupSpec) *model.Group {
	t.Helper()
	var g *model.Group
	require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
		var err error
		g, err = tx.CreateGroup(t.Context(), spec)
		return err
	}))
	return g
}

// mustAddMembers adds members and returns the per-member outcomes.
func mustAddMembers(t *testing.T, s *Store, groupID string, ids ...string) []model.MemberChange {
	t.Helper()
	var changes []model.MemberChange
	require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
		var err error
		changes, err = tx.AddMembers(t.Context(), groupID, ids, "test-actor")
		return err
	}))
	return changes
}

func collabList(owner, name string) model.GroupSpec {
	return model.GroupSpec{GroupType: model.GroupTypeCollaboratorList, Owner: owner, Name: name}
}

func TestCreateGroup(t *testing.T) {
	s := testStore(t)

	t.Run("assigns a Grouper-shaped identifier", func(t *testing.T) {
		g := mustCreate(t, s, collabList("test-alice", "default"))
		assert.Regexp(t, `^[0-9a-f]{32}$`, g.ID,
			"new group ids must match the imported Grouper format")
		assert.Equal(t, "test-alice", g.Owner)
		assert.Equal(t, "default", g.Name)
		assert.False(t, g.CreatedAt.IsZero())
	})

	t.Run("rejects a duplicate identity regardless of case and spacing", func(t *testing.T) {
		mustCreate(t, s, collabList("test-bob", "shared"))
		err := s.WithTx(t.Context(), func(tx store.Tx) error {
			_, err := tx.CreateGroup(t.Context(), collabList("TEST-BOB", "  Shared "))
			return err
		})
		assert.ErrorIs(t, err, store.ErrConflict)
	})

	t.Run("enforces owner against group type", func(t *testing.T) {
		cases := []struct {
			name string
			spec model.GroupSpec
		}{
			{"community with an owner", model.GroupSpec{
				GroupType: model.GroupTypeCommunity, Owner: "test-alice", Name: "owned"}},
			{"team without an owner", model.GroupSpec{
				GroupType: model.GroupTypeTeam, Name: "ownerless"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := s.WithTx(t.Context(), func(tx store.Tx) error {
					_, err := tx.CreateGroup(t.Context(), tc.spec)
					return err
				})
				assert.ErrorIs(t, err, store.ErrOwnerRequired)
			})
		}
	})

	t.Run("rolls back when the transaction returns an error", func(t *testing.T) {
		sentinel := errors.New("caller changed its mind")
		err := s.WithTx(t.Context(), func(tx store.Tx) error {
			if _, err := tx.CreateGroup(t.Context(), collabList("test-carol", "rollback")); err != nil {
				return err
			}
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		_, err = s.LookupGroup(t.Context(),
			model.Ref{GroupType: model.GroupTypeCollaboratorList, Owner: "test-carol", Name: "rollback"})
		assert.ErrorIs(t, err, store.ErrNotFound, "the group must not survive a rolled-back transaction")
	})
}

func TestLookupAndGet(t *testing.T) {
	s := testStore(t)
	created := mustCreate(t, s, collabList("test-alice", "My List"))

	t.Run("by id", func(t *testing.T) {
		g, err := s.GetGroup(t.Context(), created.ID)
		require.NoError(t, err)
		assert.Equal(t, created.ID, g.ID)
	})

	t.Run("by identity, case- and space-insensitively", func(t *testing.T) {
		g, err := s.LookupGroup(t.Context(), model.Ref{
			GroupType: model.GroupTypeCollaboratorList, Owner: "TEST-ALICE", Name: "my  list"})
		require.NoError(t, err)
		assert.Equal(t, created.ID, g.ID)
	})

	t.Run("missing group reports not found", func(t *testing.T) {
		_, err := s.GetGroup(t.Context(), "ffffffffffffffffffffffffffffffff")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func TestUpdateAndDelete(t *testing.T) {
	s := testStore(t)

	t.Run("updates only the fields supplied", func(t *testing.T) {
		g := mustCreate(t, s, model.GroupSpec{
			GroupType: model.GroupTypeCommunity, Name: "NEON", Description: "original"})

		newName := "NEON Updated"
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			updated, err := tx.UpdateGroup(t.Context(), g.ID, model.GroupUpdate{Name: &newName})
			if err != nil {
				return err
			}
			assert.Equal(t, "NEON Updated", updated.Name)
			assert.Equal(t, "original", updated.Description, "description must be left alone")
			return nil
		}))
	})

	t.Run("delete removes the group and a second delete is idempotent", func(t *testing.T) {
		g := mustCreate(t, s, collabList("test-dave", "doomed"))
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			return tx.DeleteGroup(t.Context(), g.ID)
		}))

		_, err := s.GetGroup(t.Context(), g.ID)
		assert.ErrorIs(t, err, store.ErrNotFound)

		err = s.WithTx(t.Context(), func(tx store.Tx) error {
			return tx.DeleteGroup(t.Context(), g.ID)
		})
		assert.NoError(t, err, "deleting an absent group must succeed silently")
	})
}

func TestListGroups(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "default"))
	mustCreate(t, s, model.GroupSpec{GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Ecology"})
	mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeCommunity, Name: "NEON", Description: "sensor network"})
	mustAddMembers(t, s, list.ID, "test-zed")

	cases := []struct {
		name   string
		filter store.ListFilter
		want   []string
	}{
		{"everything", store.ListFilter{}, []string{"default", "NEON", "Ecology"}},
		{"by type", store.ListFilter{GroupType: model.GroupTypeTeam}, []string{"Ecology"}},
		{"by owner", store.ListFilter{Owner: "test-alice"}, []string{"default"}},
		{"by name search", store.ListFilter{Search: "eco"}, []string{"Ecology"}},
		{"search matches description", store.ListFilter{Search: "sensor"}, []string{"NEON"}},
		{"by member", store.ListFilter{Member: "test-zed"}, []string{"default"}},
		{"limit", store.ListFilter{Limit: 1}, []string{"default"}},
		{"no match", store.ListFilter{Search: "nonexistent"}, []string{}},
		{"wildcards are matched literally", store.ListFilter{Search: "%"}, []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups, err := s.ListGroups(t.Context(), tc.filter)
			require.NoError(t, err)

			names := make([]string, 0, len(groups))
			for _, g := range groups {
				names = append(names, g.Name)
			}
			assert.ElementsMatch(t, tc.want, names)
		})
	}
}

func TestMembership(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "default"))

	t.Run("adds users and creates their subject rows", func(t *testing.T) {
		changes := mustAddMembers(t, s, list.ID, "test-bob", "test-carol")
		require.Len(t, changes, 2)
		for _, c := range changes {
			assert.NoError(t, c.Err)
			assert.Equal(t, model.MemberTypeUser, c.Type)
		}

		members, err := s.ListMembers(t.Context(), list.ID, store.MemberFilter{})
		require.NoError(t, err)
		assert.Len(t, members, 2)
	})

	t.Run("adding an existing member is idempotent", func(t *testing.T) {
		mustAddMembers(t, s, list.ID, "test-bob")
		members, err := s.ListMembers(t.Context(), list.ID, store.MemberFilter{})
		require.NoError(t, err)
		assert.Len(t, members, 2, "re-adding a member must not duplicate it")
	})

	t.Run("removing a non-member succeeds", func(t *testing.T) {
		var changes []model.MemberChange
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			var err error
			changes, err = tx.RemoveMembers(t.Context(), list.ID, []string{"test-nobody"})
			return err
		}))
		assert.Len(t, changes, 1)
		assert.NoError(t, changes[0].Err)
	})

	t.Run("membership of a missing group reports not found", func(t *testing.T) {
		err := s.WithTx(t.Context(), func(tx store.Tx) error {
			_, err := tx.AddMembers(t.Context(), "ffffffffffffffffffffffffffffffff",
				[]string{"test-bob"}, "test-actor")
			return err
		})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("replace reports only the differences", func(t *testing.T) {
		group := mustCreate(t, s, collabList("test-erin", "default"))
		mustAddMembers(t, s, group.ID, "test-bob", "test-carol")

		var changes []model.MemberChange
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			var err error
			changes, err = tx.ReplaceMembers(t.Context(), group.ID,
				[]string{"test-carol", "test-dan"}, "test-actor")
			return err
		}))

		ids := make([]string, 0, len(changes))
		for _, c := range changes {
			ids = append(ids, c.SubjectID)
		}
		assert.ElementsMatch(t, []string{"test-bob", "test-dan"}, ids,
			"carol was already a member and must not be reported")
	})

	t.Run("replace deduplicates a repeated subject", func(t *testing.T) {
		group := mustCreate(t, s, collabList("test-frank", "default"))

		var changes []model.MemberChange
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			var err error
			changes, err = tx.ReplaceMembers(t.Context(), group.ID,
				[]string{"test-gina", "test-gina"}, "test-actor")
			return err
		}))
		assert.Len(t, changes, 1, "a repeated subject must yield one add and one change")

		members, err := s.ListMembers(t.Context(), group.ID, store.MemberFilter{})
		require.NoError(t, err)
		assert.Len(t, members, 1)
	})
}

func TestNesting(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "default"))
	team := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Ecology"})

	mustAddMembers(t, s, team.ID, "test-carol", "test-dan")
	changes := mustAddMembers(t, s, list.ID, team.ID)
	require.Len(t, changes, 1)
	require.NoError(t, changes[0].Err)
	assert.Equal(t, model.MemberTypeGroup, changes[0].Type)

	t.Run("effective membership resolves through the nested group", func(t *testing.T) {
		for _, user := range []string{"test-carol", "test-dan"} {
			member, err := s.IsEffectiveMember(t.Context(), list.ID, user)
			require.NoError(t, err)
			assert.True(t, member, "%s should reach the list through the nested team", user)
		}
	})

	t.Run("direct membership still reports the group, not its users", func(t *testing.T) {
		members, err := s.ListMembers(t.Context(), list.ID, store.MemberFilter{})
		require.NoError(t, err)
		require.Len(t, members, 1)
		assert.Equal(t, team.ID, members[0].ID)
		assert.Equal(t, model.MemberTypeGroup, members[0].Type)
	})

	t.Run("adding to the nested group propagates to the container", func(t *testing.T) {
		mustAddMembers(t, s, team.ID, "test-frank")
		member, err := s.IsEffectiveMember(t.Context(), list.ID, "test-frank")
		require.NoError(t, err)
		assert.True(t, member, "a new team member must reach the containing list")
	})

	t.Run("removing from the nested group propagates to the container", func(t *testing.T) {
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			_, err := tx.RemoveMembers(t.Context(), team.ID, []string{"test-dan"})
			return err
		}))
		member, err := s.IsEffectiveMember(t.Context(), list.ID, "test-dan")
		require.NoError(t, err)
		assert.False(t, member, "a removed team member must lose access through the list")
	})

	t.Run("GroupsForSubject reports containers reached through nesting", func(t *testing.T) {
		groups, err := s.GroupsForSubject(t.Context(), "test-carol", "", "")
		require.NoError(t, err)

		ids := make([]string, 0, len(groups))
		for _, g := range groups {
			ids = append(ids, g.ID)
		}
		assert.ElementsMatch(t, []string{team.ID, list.ID}, ids)

		filtered, err := s.GroupsForSubject(t.Context(), "test-carol", model.GroupTypeTeam, "")
		require.NoError(t, err)
		require.Len(t, filtered, 1)
		assert.Equal(t, team.ID, filtered[0].ID)
	})

	t.Run("deleting the nested group revokes inherited membership", func(t *testing.T) {
		require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
			return tx.DeleteGroup(t.Context(), team.ID)
		}))

		member, err := s.IsEffectiveMember(t.Context(), list.ID, "test-carol")
		require.NoError(t, err)
		assert.False(t, member,
			"deleting a nested group must not leave its members with inherited access")
	})
}

func TestCycleRejection(t *testing.T) {
	s := testStore(t)
	outer := mustCreate(t, s, model.GroupSpec{GroupType: model.GroupTypeCommunity, Name: "Outer"})
	inner := mustCreate(t, s, model.GroupSpec{GroupType: model.GroupTypeCommunity, Name: "Inner"})

	require.NoError(t, mustAddMembers(t, s, outer.ID, inner.ID)[0].Err)

	cases := []struct {
		name          string
		group, member string
	}{
		{"direct cycle", inner.ID, outer.ID},
		{"self membership", outer.ID, outer.ID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes := mustAddMembers(t, s, tc.group, tc.member)
			require.Len(t, changes, 1)
			assert.ErrorIs(t, changes[0].Err, store.ErrCycle)
		})
	}

	t.Run("a rejected member does not abort the rest of the batch", func(t *testing.T) {
		changes := mustAddMembers(t, s, inner.ID, outer.ID, "test-grace")
		require.Len(t, changes, 2)
		assert.ErrorIs(t, changes[0].Err, store.ErrCycle)
		assert.NoError(t, changes[1].Err)

		member, err := s.IsEffectiveMember(t.Context(), inner.ID, "test-grace")
		require.NoError(t, err)
		assert.True(t, member)
	})
}

func TestGrouperAllIsNotAGroup(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "default"))

	// GrouperAll is a group-typed subject with no group row: the sentinel that
	// marks a group public. It must not be usable as a member.
	_, err := s.db.ExecContext(t.Context(),
		`INSERT INTO subjects (subject_id, subject_type) VALUES ('GrouperAll', 'group')
		 ON CONFLICT (subject_id) DO NOTHING`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := s.db.ExecContext(context.Background(),
			`DELETE FROM subjects WHERE subject_id = 'GrouperAll'`)
		assert.NoError(t, err)
	})

	changes := mustAddMembers(t, s, list.ID, "GrouperAll")
	require.Len(t, changes, 1)
	require.Error(t, changes[0].Err)
	assert.Contains(t, changes[0].Err.Error(), "not a group that can hold members")
}

func TestDSNWithSearchPath(t *testing.T) {
	cases := []struct {
		name, uri, schema, want string
	}{
		{
			name:   "adds search_path when absent",
			uri:    "postgres://de@dedb:5432/de?sslmode=disable",
			schema: "permissions",
			want:   "postgres://de@dedb:5432/de?search_path=permissions%2Cpublic&sslmode=disable",
		},
		{
			name:   "leaves an explicit search_path alone",
			uri:    "postgres://de@dedb:5432/de?search_path=custom",
			schema: "permissions",
			want:   "postgres://de@dedb:5432/de?search_path=custom",
		},
		{
			name:   "passes keyword/value connection strings through",
			uri:    "host=dedb user=de dbname=de",
			schema: "permissions",
			want:   "host=dedb user=de dbname=de",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DSNWithSearchPath(tc.uri, tc.schema)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Guards the assumption the closure depends on: the trigger repairs containers
// on delete, so a from-scratch recomputation must always agree with the stored
// table. This is the same check the reconciliation job runs.
func TestClosureMatchesRecomputation(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "default"))
	team := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Ecology"})

	mustAddMembers(t, s, team.ID, "test-carol")
	mustAddMembers(t, s, list.ID, team.ID, "test-alice")
	require.NoError(t, s.WithTx(t.Context(), func(tx store.Tx) error {
		return tx.DeleteGroup(t.Context(), team.ID)
	}))

	var missing, stale int
	err := s.db.QueryRowContext(t.Context(), `
		WITH RECURSIVE reachable(root, member_id, member_type) AS (
			SELECT gm.group_id, gm.member_id, gm.member_type FROM group_memberships gm
			UNION
			SELECT r.root, gm.member_id, gm.member_type
			  FROM reachable r JOIN group_memberships gm ON gm.group_id = r.member_id
			 WHERE r.member_type = 'group'
		),
		expected AS (SELECT DISTINCT root AS group_id, member_id FROM reachable WHERE member_type = 'user')
		SELECT
			(SELECT count(*) FROM (SELECT group_id, member_id FROM expected
			 EXCEPT SELECT group_id, member_id FROM group_effective_members) d),
			(SELECT count(*) FROM (SELECT group_id, member_id FROM group_effective_members
			 EXCEPT SELECT group_id, member_id FROM expected) d)`).Scan(&missing, &stale)
	require.NoError(t, err)

	assert.Zero(t, missing, "the closure is missing rows the membership graph implies")
	assert.Zero(t, stale, "the closure grants membership the graph no longer justifies")
}

// TestExistingSubjects covers the query that decides which members need
// validating against the identity provider: anything it does not return would
// cause a new subject row to be created.
func TestExistingSubjects(t *testing.T) {
	s := testStore(t)

	g := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-owner", Name: "existing-subjects",
	})
	mustAddMembers(t, s, g.ID, "test-alice")

	tests := []struct {
		name string
		ids  []string
		want []string
	}{
		{name: "no identifiers", ids: nil, want: nil},
		{name: "a user with a subject row", ids: []string{"test-alice"}, want: []string{"test-alice"}},
		{name: "a user without one", ids: []string{"test-nobody"}, want: nil},
		{
			name: "a group is an existing subject",
			ids:  []string{g.ID},
			want: []string{g.ID},
		},
		{
			name: "reports only the known ones from a mixed set",
			ids:  []string{"test-alice", "test-nobody", g.ID},
			want: []string{"test-alice", g.ID},
		},
		{
			name: "matching is case-sensitive",
			ids:  []string{"TEST-ALICE"},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ExistingSubjects(t.Context(), tt.ids)
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

// TestAddMemberRejectsWhitespaceIdentifier proves the whitespace rule holds on
// the path the Grouper importer uses -- writing through the store, which bypasses
// the handler-level identity-provider check.
func TestAddMemberRejectsWhitespaceIdentifier(t *testing.T) {
	s := testStore(t)

	g := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-owner", Name: "whitespace",
	})

	// The exact shape production Grouper holds.
	changes := mustAddMembers(t, s, g.ID, "test-cencas ", "test-clean")
	require.Len(t, changes, 2)

	byID := map[string]model.MemberChange{}
	for _, c := range changes {
		byID[c.SubjectID] = c
	}
	require.Error(t, byID["test-cencas "].Err, "a trailing space must be rejected")
	assert.NoError(t, byID["test-clean"].Err, "the rest of the batch must still apply")

	members, err := s.ListMembers(t.Context(), g.ID, store.MemberFilter{})
	require.NoError(t, err)
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []string{"test-clean"}, ids, "no subject row may be created for it")

	var leaked int
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM subjects WHERE subject_id = 'test-cencas '`).Scan(&leaked))
	assert.Zero(t, leaked, "the rejected identifier must not reach the subjects table")
}

// TestEnsureUserSubjectCorrelatesDEUser covers populating subjects.user_id when
// a membership creates a subject row. The correlation is best-effort: a user who
// has never logged in to the DE has no users row, and must still become a member.
func TestEnsureUserSubjectCorrelatesDEUser(t *testing.T) {
	s := testStore(t)

	var deUserID string
	require.NoError(t, s.db.QueryRowContext(t.Context(),
		`INSERT INTO public.users (username) VALUES ('test-known' || $1)
		 ON CONFLICT (username) DO UPDATE SET username = EXCLUDED.username
		 RETURNING id`, DefaultUserSuffix).Scan(&deUserID))
	t.Cleanup(func() {
		_, err := s.db.ExecContext(context.Background(),
			`DELETE FROM public.users WHERE username LIKE 'test-%'`)
		assert.NoError(t, err)
	})

	g := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-owner", Name: "correlation",
	})
	changes := mustAddMembers(t, s, g.ID, "test-known", "test-nologin")
	for _, c := range changes {
		require.NoError(t, c.Err)
	}

	userIDFor := func(subjectID string) *string {
		var id *string
		require.NoError(t, s.db.QueryRowContext(t.Context(),
			`SELECT user_id::text FROM subjects WHERE subject_id = $1`, subjectID).Scan(&id))
		return id
	}

	require.NotNil(t, userIDFor("test-known"), "a member with a DE user must be correlated")
	assert.Equal(t, deUserID, *userIDFor("test-known"))
	assert.Nil(t, userIDFor("test-nologin"),
		"a member who has never logged in must still be added, uncorrelated")
}

// Listings are access-filtered. Grouper's group search only ever returned what
// the caller could see, so an unfiltered listing lets any user enumerate every
// team, community, and -- worst -- every other user's personal collaborator
// lists by name, description, and owner.
//
// The filter has to agree exactly with what the read check allows. A group that
// lists but cannot be opened is the bug this replaces; one that opens but never
// lists is unreachable.
func TestListGroupsVisibleTo(t *testing.T) {
	s := testStore(t)

	mine := mustCreate(t, s, collabList("test-alice", "default"))
	joined := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Joined"})
	public := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeCommunity, Name: "Public"})
	granted := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Granted"})
	viaGroup := mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "ViaGroup"})
	mustCreate(t, s, model.GroupSpec{
		GroupType: model.GroupTypeTeam, Owner: "test-bob", Name: "Private"})

	mustAddMembers(t, s, joined.ID, "test-alice")
	mustAddMembers(t, s, mine.ID, "test-alice")

	grantOnGroup(t, s, public.ID, "GrouperAll", "group", "read")
	grantOnGroup(t, s, granted.ID, "test-alice", "user", "read")
	// A grant held by a group alice belongs to reaches her the same way the
	// permission hot path expands a subject.
	grantOnGroup(t, s, viaGroup.ID, mine.ID, "group", "read")

	groups, err := s.ListGroups(t.Context(), store.ListFilter{VisibleTo: "test-alice"})
	require.NoError(t, err)

	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	assert.ElementsMatch(t, []string{"default", "Joined", "Public", "Granted", "ViaGroup"}, names)
	assert.NotContains(t, names, "Private")

	t.Run("an empty VisibleTo does not filter, for the admin accounts", func(t *testing.T) {
		all, err := s.ListGroups(t.Context(), store.ListFilter{})
		require.NoError(t, err)
		assert.Len(t, all, 6)
	})
}

// grantOnGroup records a permission on a group the way the permissions service
// does: the resource is named by the group's external id, and the grantee is a
// subject of the given type.
func grantOnGroup(t *testing.T, s *Store, groupID, subjectID, subjectType, level string) {
	t.Helper()
	ctx := t.Context()

	var rtID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM resource_types WHERE name = 'group'`).Scan(&rtID)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO resource_types (name, description) VALUES ('group', 'A group.') RETURNING id`).Scan(&rtID)
	}
	require.NoError(t, err)

	var resID string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM resources WHERE name = $1 AND resource_type_id = $2`, groupID, rtID).Scan(&resID)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO resources (name, resource_type_id) VALUES ($1, $2) RETURNING id`,
			groupID, rtID).Scan(&resID)
	}
	require.NoError(t, err)

	var subjID string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM subjects WHERE subject_id = $1`, subjectID).Scan(&subjID)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx,
			`INSERT INTO subjects (subject_id, subject_type) VALUES ($1, $2::subject_type_enum) RETURNING id`,
			subjectID, subjectType).Scan(&subjID)
	}
	require.NoError(t, err)

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO permissions (subject_id, resource_id, permission_level_id)
		SELECT $1, $2, pl.id FROM permission_levels pl WHERE pl.name = $3
		ON CONFLICT (subject_id, resource_id) DO NOTHING`, subjID, resID, level)
	require.NoError(t, err)
}

func TestListMembersPaging(t *testing.T) {
	s := testStore(t)
	list := mustCreate(t, s, collabList("test-alice", "paging"))

	ids := []string{"test-p1", "test-p2", "test-p3", "test-p4", "test-p5"}
	mustAddMembers(t, s, list.ID, ids...)

	names := func(members []model.MemberRef) []string {
		out := make([]string, 0, len(members))
		for _, m := range members {
			out = append(out, m.ID)
		}
		return out
	}

	t.Run("counts every member", func(t *testing.T) {
		total, err := s.CountMembers(t.Context(), list.ID)
		require.NoError(t, err)
		assert.Equal(t, len(ids), total)
	})

	t.Run("an empty filter returns every member", func(t *testing.T) {
		members, err := s.ListMembers(t.Context(), list.ID, store.MemberFilter{})
		require.NoError(t, err)
		assert.Equal(t, ids, names(members))
	})

	t.Run("pages cover the listing exactly once", func(t *testing.T) {
		var seen []string
		for offset := 0; offset < len(ids); offset += 2 {
			members, err := s.ListMembers(t.Context(), list.ID,
				store.MemberFilter{Limit: 2, Offset: offset})
			require.NoError(t, err)
			seen = append(seen, names(members)...)
		}
		assert.Equal(t, ids, seen, "paging must neither repeat nor skip a member")
	})

	t.Run("an offset past the end is empty, not an error", func(t *testing.T) {
		members, err := s.ListMembers(t.Context(), list.ID,
			store.MemberFilter{Limit: 2, Offset: len(ids) + 10})
		require.NoError(t, err)
		assert.Empty(t, members)
	})

	t.Run("an offset with no limit skips without truncating", func(t *testing.T) {
		members, err := s.ListMembers(t.Context(), list.ID, store.MemberFilter{Offset: 3})
		require.NoError(t, err)
		assert.Equal(t, ids[3:], names(members))
	})

	t.Run("counting a group that does not exist reports not found", func(t *testing.T) {
		_, err := s.CountMembers(t.Context(), "00000000000000000000000000000000")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}
