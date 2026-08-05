package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/cyverse-de/groups/permissions"
	"github.com/labstack/echo/v4"
)

// resourceTypeGroup is the permissions-service resource type under which groups
// are registered.
const resourceTypeGroup = "group"

// publicSubjectID is Grouper's "everyone" subject. A read grant held by it marks
// a team or community public, and Grouper's own privilege engine resolved it as
// every user. Nothing here expands a user to it -- it is a group-typed subject
// with no members and no groups row -- so it has to be checked as itself.
const publicSubjectID = "GrouperAll"

// userContextKey is the echo context key under which the acting user is stored.
const userContextKey = "user"

// requireUser is middleware that requires the acting user to be supplied via the
// `user` query parameter (passed by terrain, the auth boundary) and stashes it
// in the request context.
func requireUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := c.QueryParam("user")
		if user == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "the user query parameter is required")
		}
		c.Set(userContextKey, user)
		return next(c)
	}
}

// actingUser returns the acting user previously stored by requireUser.
func actingUser(c echo.Context) string {
	user, _ := c.Get(userContextKey).(string)
	return user
}

// isAdminUser reports whether the acting user is a configured administrative
// service account (admin.users) that bypasses per-group permission checks.
func (a *App) isAdminUser(user string) bool {
	_, ok := a.adminUsers[user]
	return ok
}

// requireLevel ensures the acting user holds at least minLevel on the group,
// returning a 403 error otherwise.
func (a *App) requireLevel(c echo.Context, groupID, minLevel string) error {
	if a.isAdminUser(actingUser(c)) {
		return nil
	}
	ok, err := a.permissions.Check(c.Request().Context(), permissions.SubjectTypeUser,
		actingUser(c), resourceTypeGroup, groupID, minLevel, true)
	if err != nil {
		return err
	}
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient privileges")
	}
	return nil
}

// requireReadOrMember ensures the acting user either holds at least read
// permission on the group or is a member of it.
func (a *App) requireReadOrMember(c echo.Context, groupID string) error {
	ctx := c.Request().Context()
	user := actingUser(c)

	if a.isAdminUser(user) {
		return nil
	}

	ok, err := a.permissions.Check(ctx, permissions.SubjectTypeUser, user,
		resourceTypeGroup, groupID, permissions.LevelRead, true)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	// A public group is readable by anyone. This is a separate check because the
	// one above expands the acting user to their groups, and no user is ever a
	// member of GrouperAll -- so the grant that marks a group public is
	// invisible to it. Without this, every public team and community in the
	// deployment becomes unreadable to non-members, which is not how Grouper
	// behaved: it resolved GrouperAll as everyone.
	public, err := a.isPublic(ctx, groupID)
	if err != nil {
		return err
	}
	if public {
		return nil
	}

	member, err := a.isMember(ctx, groupID, user)
	if err != nil {
		return err
	}
	if !member {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient privileges")
	}
	return nil
}

// memberListAccess says how the member list should be served.
type memberListAccess int

const (
	// membersDenied means the caller may not see the group at all.
	membersDenied memberListAccess = iota
	// membersRedacted means the group is public but its membership is not, so
	// the list is served empty rather than refused.
	membersRedacted
	// membersVisible means the caller may see the membership.
	membersVisible
)

// memberListAccess decides how to serve a group's member list.
//
// Grouper marked public teams with `viewers` and public communities with
// `readers`. A `viewers` group was visible to everyone and returned an *empty*
// member list to non-members rather than an error -- so the DE's team page
// renders for any public team. Refusing instead would break that page for all
// 183 of them, which is the whole reason this distinction is preserved.
func (a *App) memberListAccess(c echo.Context, groupID string) (memberListAccess, error) {
	if err := a.requireReadOrMember(c, groupID); err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusForbidden {
			return membersDenied, nil
		}
		return membersDenied, err
	}

	// The group is readable. Whether its membership is depends on the acting
	// user's own standing, and failing that on the group's own flag.
	ctx := c.Request().Context()
	user := actingUser(c)
	if a.isAdminUser(user) {
		return membersVisible, nil
	}

	ok, err := a.permissions.Check(ctx, permissions.SubjectTypeUser, user,
		resourceTypeGroup, groupID, permissions.LevelRead, true)
	if err != nil {
		return membersDenied, err
	}
	if ok {
		return membersVisible, nil
	}

	member, err := a.isMember(ctx, groupID, user)
	if err != nil {
		return membersDenied, err
	}
	if member {
		return membersVisible, nil
	}

	group, err := a.store.GetGroup(ctx, groupID)
	if err != nil {
		return membersDenied, storeError(err)
	}
	if group.MembersPublic {
		return membersVisible, nil
	}
	return membersRedacted, nil
}

// isPublic reports whether the group carries the read grant to GrouperAll that
// marks a team or community public.
func (a *App) isPublic(ctx context.Context, groupID string) (bool, error) {
	return a.permissions.Check(ctx, permissions.SubjectTypeGroup, publicSubjectID,
		resourceTypeGroup, groupID, permissions.LevelRead, false)
}

// isMember reports whether the named user belongs to the group, through any
// depth of nesting. This reads the materialized effective membership, so it is
// an index probe rather than a fetch of the whole member list.
func (a *App) isMember(ctx context.Context, groupID, user string) (bool, error) {
	member, err := a.store.IsEffectiveMember(ctx, groupID, user)
	if err != nil {
		return false, storeError(err)
	}
	return member, nil
}
