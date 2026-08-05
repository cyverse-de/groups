package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/permissions"
	"github.com/cyverse-de/groups/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireUser(t *testing.T) {
	app := newTestApp(&mockStore{})

	// No user query parameter -> 400.
	rec := doRequestAs(app, http.MethodGet, "/groups/abc", "", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCreateGroupGrantsOwner(t *testing.T) {
	var granted struct {
		resourceType, resourceName, subjectType, subjectID, level string
	}
	perms := &mockPermissions{
		grantFn: func(_ context.Context, rt, rn, st, sid, level string) error {
			granted.resourceType, granted.resourceName = rt, rn
			granted.subjectType, granted.subjectID, granted.level = st, sid, level
			return nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequestAs(app, http.MethodPost, "/groups", `{"group_type":"team","name":"Ecology"}`, "alice")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, resourceTypeGroup, granted.resourceType)
	assert.Equal(t, "g-new", granted.resourceName)
	assert.Equal(t, permissions.SubjectTypeUser, granted.subjectType)
	assert.Equal(t, "alice", granted.subjectID)
	assert.Equal(t, permissions.LevelOwn, granted.level)
}

// The owner grant is an HTTP call made inside the database transaction, so a
// failed grant must abort the transaction rather than leave an ownerless group.
func TestCreateGroupRollsBackOnGrantFailure(t *testing.T) {
	s := &mockStore{}
	perms := &mockPermissions{
		grantFn: func(context.Context, string, string, string, string, string) error {
			return assert.AnError
		},
	}
	app := newTestAppWith(s, perms)

	rec := doRequestAs(app, http.MethodPost, "/groups", `{"group_type":"team","name":"Ecology"}`, "alice")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, s.committed,
		"the transaction must not commit when the owner grant fails")
}

func TestUpdateGroupForbiddenWithoutWrite(t *testing.T) {
	perms := &mockPermissions{
		checkFn: func(_ context.Context, _, _, _, _, minLevel string, _ bool) (bool, error) {
			assert.Equal(t, permissions.LevelWrite, minLevel)
			return false, nil
		},
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequestAs(app, http.MethodPut, "/groups/g1", `{"name":"x"}`, "mallory")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGetGroupAllowedByMembership(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(_ context.Context, groupID, username string) (bool, error) {
			assert.Equal(t, "g1", groupID)
			return username == "bob", nil
		},
	}
	// Permission checks deny, so access must come from membership.
	app := newTestAppWith(s, denyAllPermissions())

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "bob")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetGroupForbiddenForNonMember(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(_ context.Context, _, username string) (bool, error) {
			return username == "bob", nil
		},
	}
	app := newTestAppWith(s, denyAllPermissions())

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "carol")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// A public team or community is marked by a read grant to GrouperAll, which
// Grouper resolved as everyone. Nothing expands a user to GrouperAll -- it has
// no members -- so the acting-user check can never see that grant, and without
// consulting it directly every public group becomes unreadable to non-members.
// Production carries ~183 public teams and ~55 public communities.
func TestGetGroupAllowedWhenPublic(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
	}
	perms := &mockPermissions{
		checkFn: func(_ context.Context, subjectType, subjectID, _, _, minLevel string, _ bool) (bool, error) {
			return subjectType == permissions.SubjectTypeGroup &&
				subjectID == publicSubjectID &&
				minLevel == permissions.LevelRead, nil
		},
	}
	app := newTestAppWith(s, perms)

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "stranger")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// The members list is the call Sonora's team page depends on, and the one that
// returned 403 where Grouper returned an empty list.
func TestListMembersAllowedWhenPublic(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
	}
	perms := &mockPermissions{
		checkFn: func(_ context.Context, subjectType, subjectID, _, _, _ string, _ bool) (bool, error) {
			return subjectType == permissions.SubjectTypeGroup && subjectID == publicSubjectID, nil
		},
	}
	app := newTestAppWith(s, perms)

	rec := doRequestAs(app, http.MethodGet, "/groups/g1/members", "", "stranger")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// The public check must not become a blanket allow: a group with no GrouperAll
// grant stays private to everyone who is neither a member nor granted access.
func TestGetGroupStillForbiddenWhenNotPublic(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
	}
	var askedAboutPublic bool
	perms := &mockPermissions{
		checkFn: func(_ context.Context, subjectType, subjectID, _, _, _ string, _ bool) (bool, error) {
			if subjectType == permissions.SubjectTypeGroup && subjectID == publicSubjectID {
				askedAboutPublic = true
			}
			return false, nil
		},
	}
	app := newTestAppWith(s, perms)

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "stranger")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.True(t, askedAboutPublic, "the public marker should have been consulted")
}

// Membership through a nested group grants access, because the check reads
// effective rather than direct membership.
func TestGetGroupAllowedByNestedMembership(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return true, nil
		},
		listMembersFn: func(context.Context, string) ([]model.MemberRef, error) {
			// The user is not a direct member: they are in a nested group.
			return []model.MemberRef{{ID: "team-1", Type: model.MemberTypeGroup}}, nil
		},
	}
	app := newTestAppWith(s, denyAllPermissions())

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "carol")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDeleteGroupRemovesPermissionsResource(t *testing.T) {
	resourceDeleted := false
	perms := allowAllPermissions()
	perms.deleteResourceFn = func(_ context.Context, rt, rn string) error {
		resourceDeleted = true
		assert.Equal(t, resourceTypeGroup, rt)
		assert.Equal(t, "g1", rn)
		return nil
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequestAs(app, http.MethodDelete, "/groups/g1", "", "alice")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, resourceDeleted)
}

// A failure cleaning up the permissions resource leaves an orphan but must not
// fail the request: the group is already gone.
func TestDeleteGroupSucceedsWhenResourceCleanupFails(t *testing.T) {
	perms := allowAllPermissions()
	perms.deleteResourceFn = func(context.Context, string, string) error {
		return assert.AnError
	}
	app := newTestAppWith(&mockStore{}, perms)

	rec := doRequestAs(app, http.MethodDelete, "/groups/g1", "", "alice")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAdminUserBypassesMemberAuthz(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(s, denyAllPermissions())
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	rec := doRequestAs(app, http.MethodGet, "/groups/g1/members", "", "permissions-svc")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAdminUserBypassesLevelAuthz(t *testing.T) {
	app := newTestAppWith(&mockStore{}, denyAllPermissions())
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	rec := doRequestAs(app, http.MethodPut, "/groups/g1", `{"name":"Ecology"}`, "permissions-svc")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNonAdminUserStillForbidden(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, nil
		},
	}
	app := newTestAppWith(s, denyAllPermissions())
	app.adminUsers = map[string]struct{}{"permissions-svc": {}}

	rec := doRequestAs(app, http.MethodGet, "/groups/g1/members", "", "mallory")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// A store failure during the membership check must surface as an error rather
// than as a denial that looks like a policy decision.
func TestMembershipCheckErrorIsNotADenial(t *testing.T) {
	s := &mockStore{
		isEffectiveMemberFn: func(context.Context, string, string) (bool, error) {
			return false, store.ErrNotFound
		},
	}
	app := newTestAppWith(s, denyAllPermissions())

	rec := doRequestAs(app, http.MethodGet, "/groups/g1", "", "carol")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
