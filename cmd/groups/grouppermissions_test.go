package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/permissions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allowAll returns a check function that authorizes every request.
func allowAllCheck() func(context.Context, string, string, string, string, string, bool) (bool, error) {
	return func(context.Context, string, string, string, string, string, bool) (bool, error) {
		return true, nil
	}
}

func TestListPermissions(t *testing.T) {
	perms := &mockPermissions{
		checkFn: allowAllCheck(),
		listResourceFn: func(_ context.Context, rt, rn string) ([]permissions.Permission, error) {
			assert.Equal(t, resourceTypeGroup, rt)
			assert.Equal(t, "g1", rn)
			return []permissions.Permission{
				{Subject: permissions.Subject{SubjectID: "alice", SubjectType: "user"}, PermissionLevel: "own"},
			}, nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodGet, "/groups/g1/permissions", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp groupPermissionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Permissions, 1)
	assert.Equal(t, "alice", resp.Permissions[0].Subject.SubjectID)
	assert.Equal(t, "own", resp.Permissions[0].Level)
}

func TestGrantPermission(t *testing.T) {
	var got struct{ rt, rn, st, sid, level string }
	perms := &mockPermissions{
		checkFn: allowAllCheck(),
		grantFn: func(_ context.Context, rt, rn, st, sid, level string) error {
			got.rt, got.rn, got.st, got.sid, got.level = rt, rn, st, sid, level
			return nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodPut, "/groups/g1/permissions/user/bob", `{"level":"write"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, resourceTypeGroup, got.rt)
	assert.Equal(t, "g1", got.rn)
	assert.Equal(t, "user", got.st)
	assert.Equal(t, "bob", got.sid)
	assert.Equal(t, "write", got.level)
}

func TestGrantPermissionRejectsInvalidLevel(t *testing.T) {
	app := newTestAppWith(&mockStore{}, &mockPermissions{checkFn: allowAllCheck()})

	rec := doRequest(app, http.MethodPut, "/groups/g1/permissions/user/bob", `{"level":"superuser"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGrantPermissionRejectsInvalidSubjectType(t *testing.T) {
	app := newTestAppWith(&mockStore{}, &mockPermissions{checkFn: allowAllCheck()})

	rec := doRequest(app, http.MethodPut, "/groups/g1/permissions/robot/bob", `{"level":"write"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGrantPermissionForbiddenWithoutAdmin(t *testing.T) {
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, minLevel string, _ bool) (bool, error) {
			assert.Equal(t, permissions.LevelAdmin, minLevel)
			return false, nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodPut, "/groups/g1/permissions/user/bob", `{"level":"write"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// Revoking a permission that does not exist succeeds: DELETE is idempotent,
// and the caller's intent -- the subject holds nothing -- is already true.
func TestRevokePermissionOfAbsentGrantSucceeds(t *testing.T) {
	perms := &mockPermissions{
		checkFn: allowAllCheck(),
		revokeFn: func(_ context.Context, _, _, _, _ string) error {
			return permissions.ErrNotFound
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodDelete, "/groups/g1/permissions/user/bob", "")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRevokePermission(t *testing.T) {
	revoked := false
	perms := &mockPermissions{
		checkFn: allowAllCheck(),
		revokeFn: func(_ context.Context, rt, rn, st, sid string) error {
			revoked = true
			assert.Equal(t, "g1", rn)
			assert.Equal(t, "user", st)
			assert.Equal(t, "bob", sid)
			return nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodDelete, "/groups/g1/permissions/user/bob", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, revoked)
}

func TestSubjectPermissions(t *testing.T) {
	t.Run("reports the level held on each group", func(t *testing.T) {
		var got struct {
			st, sid, rt string
			lookup      bool
		}
		perms := &mockPermissions{
			listSubjectFn: func(_ context.Context, st, sid, rt string, lookup bool) ([]permissions.SubjectPermission, error) {
				got.st, got.sid, got.rt, got.lookup = st, sid, rt, lookup
				return []permissions.SubjectPermission{
					{ResourceName: "g1", ResourceType: resourceTypeGroup, PermissionLevel: "own"},
					{ResourceName: "g2", ResourceType: resourceTypeGroup, PermissionLevel: "read"},
				}, nil
			},
		}
		app := newTestAppWith(&mockStore{}, perms)

		rec := doRequestAs(app, http.MethodGet, "/subjects/alice/permissions", "", "alice")
		require.Equal(t, http.StatusOK, rec.Code)

		assert.Equal(t, permissions.SubjectTypeUser, got.st)
		assert.Equal(t, "alice", got.sid)
		assert.Equal(t, resourceTypeGroup, got.rt)
		assert.True(t, got.lookup, "permissions inherited through group membership must be included")

		var resp subjectPermissionsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Permissions, 2)
		assert.Equal(t, "g1", resp.Permissions[0].GroupID)
		assert.Equal(t, "own", resp.Permissions[0].Level)
		assert.Equal(t, "g2", resp.Permissions[1].GroupID)
		assert.Equal(t, "read", resp.Permissions[1].Level)
	})

	t.Run("holds no permissions", func(t *testing.T) {
		app := newTestAppWith(&mockStore{}, &mockPermissions{})

		rec := doRequestAs(app, http.MethodGet, "/subjects/alice/permissions", "", "alice")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp subjectPermissionsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		assert.Empty(t, resp.Permissions)
		assert.Contains(t, rec.Body.String(), `"permissions":[]`,
			"an empty list must not serialize as null")
	})

	t.Run("authorization", func(t *testing.T) {
		called := false
		perms := &mockPermissions{
			listSubjectFn: func(context.Context, string, string, string, bool) ([]permissions.SubjectPermission, error) {
				called = true
				return nil, nil
			},
		}

		for _, tc := range []struct {
			name    string
			subject string
			actor   string
			admin   bool
			want    int
		}{
			{"about oneself", "alice", "alice", false, http.StatusOK},
			{"about another subject", "alice", "bob", false, http.StatusForbidden},
			{"about another subject as an admin user", "alice", "de_grouper", true, http.StatusOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				called = false
				app := newTestAppWith(&mockStore{}, perms)
				if tc.admin {
					app.adminUsers = map[string]struct{}{tc.actor: {}}
				}

				rec := doRequestAs(app, http.MethodGet, "/subjects/"+tc.subject+"/permissions", "", tc.actor)
				assert.Equal(t, tc.want, rec.Code)
				assert.Equal(t, tc.want == http.StatusOK, called,
					"the permissions service must not be consulted for a refused request")
			})
		}
	})
}
