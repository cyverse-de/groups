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
	if err := a.requireLevel(c, groupID, permissions.LevelOwn); err != nil {
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
//
//	@Summary	Revoke a subject's permission on a group
//	@Param	id	path	string	true	"Group UUID"
//	@Param	subject-type	path	string	true	"Subject type (user or group)"
//	@Param	subject-id	path	string	true	"Subject identifier"
//	@Success	200
//	@Failure	403	{object}	map[string]string
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/permissions/{subject-type}/{subject-id} [delete]
func (a *App) RevokePermissionHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelOwn); err != nil {
		return err
	}

	subjectType, subjectID, err := subjectFromPath(c)
	if err != nil {
		return err
	}

	if err := a.permissions.Revoke(c.Request().Context(), resourceTypeGroup, groupID, subjectType, subjectID); err != nil {
		if errors.Is(err, permissions.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "no such permission")
		}
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
