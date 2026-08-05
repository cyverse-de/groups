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

func TestRevokePermissionNotFound(t *testing.T) {
	perms := &mockPermissions{
		checkFn: allowAllCheck(),
		revokeFn: func(_ context.Context, _, _, _, _ string) error {
			return permissions.ErrNotFound
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequest(app, http.MethodDelete, "/groups/g1/permissions/user/bob", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
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
