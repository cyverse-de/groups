package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/permissions"
	"github.com/cyverse-de/groups/store"
	"github.com/cyverse-de/groups/userinfo"
	"github.com/labstack/echo/v4"
)

// maxListLimit caps how many groups one listing may return, so a caller that
// omits a limit cannot ask for the whole table.
const maxListLimit = 1000

// groupRequest is the body accepted when creating a group.
type groupRequest struct {
	GroupType   string `json:"group_type"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// groupUpdateRequest is the body accepted when updating a group. Fields are
// pointers so an absent field is distinguishable from one set to empty; group
// type and owner are absent because they are immutable.
type groupUpdateRequest struct {
	Name        *string `json:"name"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
}

// groupListResponse wraps a list of groups.
type groupListResponse struct {
	Groups []model.Group `json:"groups"`
}

// storeError maps a store error to an HTTP error. Anything unrecognized falls
// through to a 500 via the router's error handler.
func storeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound), errors.Is(err, userinfo.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrCycle):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrOwnerRequired), errors.Is(err, store.ErrInvalid), errors.Is(err, errUnknownUser):
		// errUnknownUser is not a 404: the group was found, and it is the named
		// member that does not exist. A 404 here would read as "no such group".
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	default:
		return err
	}
}

// ListGroupsHandler handles GET /groups.
//
//	@Summary	List groups
//	@Produce	json
//	@Param	group_type	query	string	false	"Restrict to one kind of group"
//	@Param	owner	query	string	false	"Restrict to groups owned by this user"
//	@Param	member	query	string	false	"Restrict to groups this user belongs to, including through nesting"
//	@Param	search	query	string	false	"Match against group name and description"
//	@Param	limit	query	int	false	"Maximum groups to return"
//	@Param	offset	query	int	false	"Groups to skip"
//	@Success	200	{object}	groupListResponse
//	@Router	/groups [get]
func (a *App) ListGroupsHandler(c echo.Context) error {
	limit, err := intParam(c, "limit", maxListLimit)
	if err != nil {
		return err
	}
	offset, err := intParam(c, "offset", 0)
	if err != nil {
		return err
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	groups, err := a.store.ListGroups(c.Request().Context(), store.ListFilter{
		GroupType: c.QueryParam("group_type"),
		Owner:     c.QueryParam("owner"),
		Member:    c.QueryParam("member"),
		Search:    c.QueryParam("search"),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, &groupListResponse{Groups: groups})
}

// LookupGroupHandler handles GET /groups/lookup, resolving a group's structured
// identity to the group itself. Callers address groups by type, owner, and name
// rather than searching and filtering.
//
//	@Summary	Look up a group by its identity
//	@Produce	json
//	@Param	group_type	query	string	true	"The kind of group"
//	@Param	owner	query	string	false	"Owning user, for group types that have one"
//	@Param	name	query	string	true	"The group's short name"
//	@Success	200	{object}	model.Group
//	@Failure	400	{object}	map[string]string
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/lookup [get]
func (a *App) LookupGroupHandler(c echo.Context) error {
	ref := model.Ref{
		GroupType: c.QueryParam("group_type"),
		Owner:     c.QueryParam("owner"),
		Name:      c.QueryParam("name"),
	}
	if ref.GroupType == "" || ref.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "group_type and name are required")
	}

	g, err := a.store.LookupGroup(c.Request().Context(), ref)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, g)
}

// GetGroupHandler handles GET /groups/:id.
//
//	@Summary	Get a group by ID
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Success	200	{object}	model.Group
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id} [get]
func (a *App) GetGroupHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireReadOrMember(c, groupID); err != nil {
		return err
	}

	g, err := a.store.GetGroup(c.Request().Context(), groupID)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, g)
}

// AddGroupHandler handles POST /groups.
//
// The group and the owner's permission grant are made atomic by holding the
// transaction open across the grant: the grant is an HTTP call to the
// permissions service, so committing first would leave an ownerless,
// unmanageable group behind whenever it failed.
//
//	@Summary	Create a group
//	@Accept	json
//	@Produce	json
//	@Param	body	body	groupRequest	true	"Group to create"
//	@Success	200	{object}	model.Group
//	@Failure	400	{object}	map[string]string
//	@Failure	409	{object}	map[string]string
//	@Router	/groups [post]
func (a *App) AddGroupHandler(c echo.Context) error {
	req, err := bindGroupRequest(c)
	if err != nil {
		return err
	}

	ctx := c.Request().Context()
	user := actingUser(c)

	// A caller may only create groups in their own namespace; the trusted service
	// accounts may create them anywhere.
	owner := req.Owner
	if owner == "" {
		owner = user
	}
	if owner != user && !a.isAdminUser(user) {
		return echo.NewHTTPError(http.StatusForbidden, "cannot create a group owned by another user")
	}
	if req.GroupType == model.GroupTypeCommunity || req.GroupType == model.GroupTypeSystem {
		owner = ""
	}

	var created *model.Group
	err = a.store.WithTx(ctx, func(tx store.Tx) error {
		g, err := tx.CreateGroup(ctx, model.GroupSpec{
			GroupType:   req.GroupType,
			Owner:       owner,
			Name:        req.Name,
			DisplayName: req.DisplayName,
			Description: req.Description,
		})
		if err != nil {
			return err
		}
		created = g

		return a.permissions.Grant(ctx, resourceTypeGroup, g.ID,
			permissions.SubjectTypeUser, user, permissions.LevelOwn)
	})
	if err != nil {
		return storeError(err)
	}

	a.publishGroupChanged(c, created.ID)
	return c.JSON(http.StatusOK, created)
}

// UpdateGroupHandler handles PUT /groups/:id. Group type and owner are
// immutable: they form the group's identity, and the owner is the namespace the
// group was created under rather than a transferable ownership record.
//
//	@Summary	Update a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group identifier"
//	@Param	body	body	groupUpdateRequest	true	"Fields to change"
//	@Success	200	{object}	model.Group
//	@Failure	400	{object}	map[string]string
//	@Failure	404	{object}	map[string]string
//	@Failure	409	{object}	map[string]string
//	@Router	/groups/{id} [put]
func (a *App) UpdateGroupHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelWrite); err != nil {
		return err
	}

	var req groupUpdateRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Name != nil && *req.Name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name cannot be empty")
	}

	ctx := c.Request().Context()
	var updated *model.Group
	err := a.store.WithTx(ctx, func(tx store.Tx) error {
		g, err := tx.UpdateGroup(ctx, groupID, model.GroupUpdate{
			Name:        req.Name,
			DisplayName: req.DisplayName,
			Description: req.Description,
		})
		if err != nil {
			return err
		}
		updated = g
		return nil
	})
	if err != nil {
		return storeError(err)
	}

	a.publishGroupChanged(c, groupID)
	return c.JSON(http.StatusOK, updated)
}

// DeleteGroupHandler handles DELETE /groups/:id.
//
//	@Summary	Delete a group
//	@Param	id	path	string	true	"Group identifier"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id} [delete]
func (a *App) DeleteGroupHandler(c echo.Context) error {
	groupID := c.Param("id")
	if err := a.requireLevel(c, groupID, permissions.LevelOwn); err != nil {
		return err
	}

	ctx := c.Request().Context()
	err := a.store.WithTx(ctx, func(tx store.Tx) error {
		return tx.DeleteGroup(ctx, groupID)
	})
	if err != nil {
		return storeError(err)
	}

	// Remove the group as a permissions resource. Best-effort: the group is gone
	// and its grants went with it, so a failure here leaves a harmless orphan
	// rather than a reason to fail the request.
	if err := a.permissions.DeleteResource(ctx, resourceTypeGroup, groupID); err != nil && !errors.Is(err, permissions.ErrNotFound) {
		log.WithField("context", "delete-group").
			Warnf("could not remove the permissions resource for group %s; it will linger as an orphan: %s", groupID, err)
	}

	a.publishGroupChanged(c, groupID)
	return c.NoContent(http.StatusOK)
}

// bindGroupRequest binds and validates a group creation body.
func bindGroupRequest(c echo.Context) (groupRequest, error) {
	var req groupRequest
	if err := c.Bind(&req); err != nil {
		return req, echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Name == "" {
		return req, echo.NewHTTPError(http.StatusBadRequest, "name is required")
	}
	if !validGroupType(req.GroupType) {
		return req, echo.NewHTTPError(http.StatusBadRequest,
			"group_type must be one of collaborator_list, team, community, or system")
	}
	return req, nil
}

func validGroupType(t string) bool {
	switch t {
	case model.GroupTypeCollaboratorList, model.GroupTypeTeam,
		model.GroupTypeCommunity, model.GroupTypeSystem:
		return true
	}
	return false
}

// intParam reads a non-negative integer query parameter, falling back to def
// when it is absent.
func intParam(c echo.Context, name string, def int) (int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, echo.NewHTTPError(http.StatusBadRequest, name+" must be a non-negative integer")
	}
	return v, nil
}
