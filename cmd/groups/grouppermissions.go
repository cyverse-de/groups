package main

import (
	"errors"
	"net/http"

	"github.com/cyverse-de/groups/permissions"
	"github.com/labstack/echo/v4"
)

// groupPermission is a single grant on a group, expressed in the permissions
// service's own vocabulary.
type groupPermission struct {
	Subject permissions.Subject `json:"subject"`
	Level   string              `json:"level"`
}

// groupPermissionsResponse wraps a list of group permissions.
type groupPermissionsResponse struct {
	Permissions []groupPermission `json:"permissions"`
}

// subjectPermission is the level a subject holds on one group.
type subjectPermission struct {
	GroupID string `json:"group_id"`
	Level   string `json:"level"`
}

// subjectPermissionsResponse wraps a subject's group permissions.
type subjectPermissionsResponse struct {
	Permissions []subjectPermission `json:"permissions"`
}

// permissionRequest is the body accepted when granting a permission.
type permissionRequest struct {
	Level string `json:"level"`
}

// validLevels is the set of permission levels accepted by this service.
var validLevels = map[string]bool{
	permissions.LevelOwn:   true,
	permissions.LevelWrite: true,
	permissions.LevelAdmin: true,
	permissions.LevelRead:  true,
}

// ListPermissionsHandler handles GET /groups/:id/permissions.
//
//	@Summary	List the permissions granted on a group
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	groupPermissionsResponse
//	@Failure	403	{object}	map[string]string
//	@Router	/groups/{id}/permissions [get]
func (a *App) ListPermissionsHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireReadOrMember(c, groupID); err != nil {
		return err
	}

	perms, err := a.permissions.ListResource(c.Request().Context(), resourceTypeGroup, groupID)
	if err != nil {
		return err
	}

	out := make([]groupPermission, 0, len(perms))
	for _, p := range perms {
		out = append(out, groupPermission{Subject: p.Subject, Level: p.PermissionLevel})
	}
	return c.JSON(http.StatusOK, &groupPermissionsResponse{Permissions: out})
}

// SubjectPermissionsHandler handles GET /subjects/:subject-id/permissions. It
// answers "what may this subject do across every group" in one request, which
// callers building a listing would otherwise have to assemble one group at a
// time from GET /groups/:id/permissions.
//
// Permissions inherited through group membership are included, matching what an
// authorization check on any single group would decide.
//
//	@Summary	List the group permissions a subject holds
//	@Produce	json
//	@Param	subject-id	path	string	true	"Subject identifier (username)"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	subjectPermissionsResponse
//	@Failure	403	{object}	map[string]string
//	@Router	/subjects/{subject-id}/permissions [get]
func (a *App) SubjectPermissionsHandler(c echo.Context) error {
	subjectID := c.Param("subject-id")

	// A subject's permissions are as revealing as the access-filtered group
	// listing this parallels: they name every group the subject can reach,
	// private collaborator lists included. Callers may ask about themselves;
	// only the service accounts may ask about anyone else.
	user := actingUser(c)
	if subjectID != user && !a.isAdminUser(user) {
		return echo.NewHTTPError(http.StatusForbidden,
			"only an administrative user may list another subject's permissions")
	}

	perms, err := a.permissions.ListSubject(c.Request().Context(),
		permissions.SubjectTypeUser, subjectID, resourceTypeGroup, true)
	if err != nil {
		return err
	}

	out := make([]subjectPermission, 0, len(perms))
	for _, p := range perms {
		out = append(out, subjectPermission{GroupID: p.ResourceName, Level: p.PermissionLevel})
	}
	return c.JSON(http.StatusOK, &subjectPermissionsResponse{Permissions: out})
}

// GrantPermissionHandler handles PUT /groups/:id/permissions/:subject-type/:subject-id.
//
//	@Summary	Grant a subject a permission level on a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	subject-type	path	string	true	"Subject type (user or group)"
//	@Param	subject-id	path	string	true	"Subject identifier"
//	@Param	body	body	permissionRequest	true	"The permission level to assign"
//	@Success	200	{object}	groupPermission
//	@Failure	400	{object}	map[string]string
//	@Failure	403	{object}	map[string]string
//	@Router	/groups/{id}/permissions/{subject-type}/{subject-id} [put]
func (a *App) GrantPermissionHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	subjectType, subjectID, err := subjectFromPath(c)
	if err != nil {
		return err
	}

	var req permissionRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if !validLevels[req.Level] {
		return echo.NewHTTPError(http.StatusBadRequest, "level must be one of own, write, admin, or read")
	}

	if err := a.permissions.Grant(c.Request().Context(), resourceTypeGroup, groupID, subjectType, subjectID, req.Level); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, &groupPermission{
		Subject: permissions.Subject{SubjectID: subjectID, SubjectType: subjectType},
		Level:   req.Level,
	})
}

// RevokePermissionHandler handles DELETE /groups/:id/permissions/:subject-type/:subject-id.
// Revoking a permission that does not exist succeeds, so the operation is
// idempotent.
//
//	@Summary	Revoke a subject's permission on a group
//	@Param	id	path	string	true	"Group UUID"
//	@Param	subject-type	path	string	true	"Subject type (user or group)"
//	@Param	subject-id	path	string	true	"Subject identifier"
//	@Success	200
//	@Failure	403	{object}	map[string]string
//	@Router	/groups/{id}/permissions/{subject-type}/{subject-id} [delete]
func (a *App) RevokePermissionHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelAdmin); err != nil {
		return err
	}

	subjectType, subjectID, err := subjectFromPath(c)
	if err != nil {
		return err
	}

	err = a.permissions.Revoke(c.Request().Context(), resourceTypeGroup, groupID, subjectType, subjectID)
	if err != nil && !errors.Is(err, permissions.ErrNotFound) {
		return err
	}

	return c.NoContent(http.StatusOK)
}

// subjectFromPath extracts and validates the subject type and id path params.
func subjectFromPath(c echo.Context) (subjectType, subjectID string, err error) {
	subjectType = c.Param("subject-type")
	subjectID = c.Param("subject-id")

	if subjectType != permissions.SubjectTypeUser && subjectType != permissions.SubjectTypeGroup {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "subject type must be user or group")
	}
	return subjectType, subjectID, nil
}
