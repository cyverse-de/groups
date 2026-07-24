package main

import (
	"context"
	"net/http"

	"github.com/cyverse-de/groups/permissions"
	"github.com/labstack/echo/v4"
)

// resourceTypeGroup is the permissions-service resource type under which groups
// are registered.
const resourceTypeGroup = "group"

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

	member, err := a.isMember(ctx, groupID, user)
	if err != nil {
		return err
	}
	if !member {
		return echo.NewHTTPError(http.StatusForbidden, "insufficient privileges")
	}
	return nil
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
