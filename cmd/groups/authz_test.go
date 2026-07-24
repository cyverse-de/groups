package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/cyverse-de/groups/permissions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireUser(t *testing.T) {
	app := newTestApp(&mockKeycloak{})

	// No user query parameter -> 400.
	rec := doRequestAs(app, http.MethodGet, "/groups/abc", "", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateGroupGrantsOwner(t *testing.T) {
	var granted struct {
		resourceType, resourceName, subjectType, subjectID, level string
	}
	kc := &mockKeycloak{
		createGroupFn: func(_ context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error) {
			return &keycloak.Group{ID: "g-new", Name: spec.Name}, nil
		},
	}
	perms := &mockPermissions{
		grantFn: func(_ context.Context, rt, rn, st, sid, level string) error {
			granted.resourceType, granted.resourceName = rt, rn
			granted.subjectType, granted.subjectID, granted.level = st, sid, level
			return nil
		},
	}
	app := newTestAppWith(kc, perms)

	rec := doRequestAs(app, http.MethodPost, "/groups", `{"name":"team-a"}`, "alice")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, resourceTypeGroup, granted.resourceType)
	assert.Equal(t, "g-new", granted.resourceName)
	assert.Equal(t, permissions.SubjectTypeUser, granted.subjectType)
	assert.Equal(t, "alice", granted.subjectID)
	assert.Equal(t, permissions.LevelOwn, granted.level)
}

func TestCreateGroupRollsBackOnGrantFailure(t *testing.T) {
	deleted := false
	kc := &mockKeycloak{
		createGroupFn: func(_ context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error) {
			return &keycloak.Group{ID: "g-new", Name: spec.Name}, nil
		},
		deleteGroupFn: func(_ context.Context, id string) error {
			deleted = true
			assert.Equal(t, "g-new", id)
			return nil
		},
	}
	perms := &mockPermissions{
		grantFn: func(_ context.Context, _, _, _, _, _ string) error {
			return assert.AnError
		},
	}
	app := newTestAppWith(kc, perms)

	rec := doRequestAs(app, http.MethodPost, "/groups", `{"name":"team-a"}`, "alice")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.True(t, deleted, "group should be rolled back when the owner grant fails")
}

func TestUpdateGroupForbiddenWithoutWrite(t *testing.T) {
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, minLevel string, _ bool) (bool, error) {
			assert.Equal(t, permissions.LevelWrite, minLevel)
			return false, nil
		},
	}
	app := newTestAppWith(&mockKeycloak{}, perms)

	rec := doRequestAs(app, http.MethodPut, "/groups/g1", `{"name":"x"}`, "mallory")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGetGroupAllowedByMembership(t *testing.T) {
	kc := &mockKeycloak{
		groupMembersFn: func(_ context.Context, _ string) ([]keycloak.Subject, error) {
			return []keycloak.Subject{{ID: "bob"}}, nil
		},
		getGroupFn: func(_ context.Context, id string) (*keycloak.Group, error) {
			return &keycloak.Group{ID: id, Name: "team"}, nil
		},
	}
	// Permission check denies, so access must come from membership.
	perms := &mockPermissions{
		checkFn: func(context.Context, string, string, string, string, string, bool) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(kc, perms)

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "bob")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetGroupForbiddenForNonMember(t *testing.T) {
	kc := &mockKeycloak{
		groupMembersFn: func(_ context.Context, _ string) ([]keycloak.Subject, error) {
			return []keycloak.Subject{{ID: "bob"}}, nil
		},
	}
	perms := &mockPermissions{
		checkFn: func(context.Context, string, string, string, string, string, bool) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(kc, perms)

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "carol")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDeleteGroupRemovesPermissionsResource(t *testing.T) {
	resourceDeleted := false
	kc := &mockKeycloak{
		deleteGroupFn: func(_ context.Context, _ string) error { return nil },
	}
	perms := &mockPermissions{
		checkFn: func(context.Context, string, string, string, string, string, bool) (bool, error) {
			return true, nil
		},
		deleteResourceFn: func(_ context.Context, rt, rn string) error {
			resourceDeleted = true
			assert.Equal(t, resourceTypeGroup, rt)
			assert.Equal(t, "g1", rn)
			return nil
		},
	}
	app := newTestAppWith(kc, perms)

	rec := doRequestAs(app, http.MethodDelete, "/groups/g1", "", "alice")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, resourceDeleted)
}

func TestAdminUserBypassesMemberAuthz(t *testing.T) {
	kc := &mockKeycloak{
		groupMembersFn: func(_ context.Context, id string) ([]keycloak.Subject, error) {
			assert.Equal(t, "g1", id)
			return []keycloak.Subject{{ID: "alice"}}, nil
		},
	}
	// Deny every permission check; only the admin bypass can let the request through.
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, _ string, _ bool) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(kc, perms)
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	rec := doRequestAs(app, http.MethodGet, "/groups/g1/members", "", "permissions-svc")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAdminUserBypassesLevelAuthz(t *testing.T) {
	kc := &mockKeycloak{
		updateGroupFn: func(_ context.Context, id string, spec keycloak.GroupSpec) (*keycloak.Group, error) {
			return &keycloak.Group{ID: id, Name: spec.Name}, nil
		},
	}
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, _ string, _ bool) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(kc, perms)
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	rec := doRequestAs(app, http.MethodPut, "/groups/g1", `{"name":"team-a"}`, "permissions-svc")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNonAdminUserStillForbidden(t *testing.T) {
	kc := &mockKeycloak{
		groupMembersFn: func(_ context.Context, _ string) ([]keycloak.Subject, error) {
			return []keycloak.Subject{{ID: "alice"}}, nil
		},
	}
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, _ string, _ bool) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(kc, perms)
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	// "mallory" is not an admin and not a member (members list is only alice) -> 403.
	rec := doRequestAs(app, http.MethodGet, "/groups/g1/members", "", "mallory")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
