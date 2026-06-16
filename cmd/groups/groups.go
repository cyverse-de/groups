package main

import (
	"errors"
	"net/http"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/labstack/echo/v4"
)

// groupRequest is the body accepted when creating or updating a group.
type groupRequest struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	DisplayExtension string `json:"display_extension"`
}

// groupListResponse wraps a list of groups.
type groupListResponse struct {
	Groups []keycloak.Group `json:"groups"`
}

// keycloakError maps a Keycloak client error to an appropriate HTTP error.
// Missing entities become 404s; everything else falls through to a 500 via the
// router's error handler.
func keycloakError(err error) error {
	if errors.Is(err, keycloak.ErrNotFound) {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}
	return err
}

// SearchGroupsHandler handles GET /groups.
//
//	@Summary	Search groups
//	@Produce	json
//	@Param	search	query	string	false	"Search term matched against group names"
//	@Success	200	{object}	groupListResponse
//	@Router	/groups [get]
func (a *App) SearchGroupsHandler(c echo.Context) error {
	groups, err := a.keycloak.SearchGroups(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, &groupListResponse{Groups: groups})
}

// GetGroupHandler handles GET /groups/:id.
//
//	@Summary	Get a group by ID
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Success	200	{object}	keycloak.Group
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id} [get]
func (a *App) GetGroupHandler(c echo.Context) error {
	g, err := a.keycloak.GetGroup(c.Request().Context(), c.Param("id"))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, g)
}

// AddGroupHandler handles POST /groups.
//
//	@Summary	Create a group
//	@Accept	json
//	@Produce	json
//	@Param	body	body	groupRequest	true	"Group to create"
//	@Success	200	{object}	keycloak.Group
//	@Failure	400	{object}	map[string]string
//	@Router	/groups [post]
func (a *App) AddGroupHandler(c echo.Context) error {
	req, err := bindGroupRequest(c)
	if err != nil {
		return err
	}

	g, err := a.keycloak.CreateGroup(c.Request().Context(), specFromRequest(req))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, g)
}

// UpdateGroupHandler handles PUT /groups/:id.
//
//	@Summary	Update a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	body	body	groupRequest	true	"Updated group fields"
//	@Success	200	{object}	keycloak.Group
//	@Failure	400	{object}	map[string]string
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id} [put]
func (a *App) UpdateGroupHandler(c echo.Context) error {
	req, err := bindGroupRequest(c)
	if err != nil {
		return err
	}

	g, err := a.keycloak.UpdateGroup(c.Request().Context(), c.Param("id"), specFromRequest(req))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, g)
}

// DeleteGroupHandler handles DELETE /groups/:id.
//
//	@Summary	Delete a group
//	@Param	id	path	string	true	"Group UUID"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id} [delete]
func (a *App) DeleteGroupHandler(c echo.Context) error {
	if err := a.keycloak.DeleteGroup(c.Request().Context(), c.Param("id")); err != nil {
		return keycloakError(err)
	}
	return c.NoContent(http.StatusOK)
}

// bindGroupRequest binds and validates a group request body.
func bindGroupRequest(c echo.Context) (groupRequest, error) {
	var req groupRequest
	if err := c.Bind(&req); err != nil {
		return req, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Name == "" {
		return req, echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	return req, nil
}

func specFromRequest(req groupRequest) keycloak.GroupSpec {
	return keycloak.GroupSpec{
		Name:             req.Name,
		Description:      req.Description,
		DisplayExtension: req.DisplayExtension,
	}
}
