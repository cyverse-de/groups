package main

import (
	"context"
	"net/http"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/labstack/echo/v4"
)

// membersResponse wraps a list of group members.
type membersResponse struct {
	Members []keycloak.Subject `json:"members"`
}

// membersRequest is the body for bulk membership operations.
type membersRequest struct {
	Members []string `json:"members"`
}

// memberResult reports the outcome of a single membership change.
type memberResult struct {
	SubjectID string `json:"subject_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// membersResults wraps the per-member outcomes of a bulk operation.
type membersResults struct {
	Results []memberResult `json:"results"`
}

// GetMembersHandler handles GET /groups/:id/members.
//
//	@Summary	List group members
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Success	200	{object}	membersResponse
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members [get]
func (a *App) GetMembersHandler(c echo.Context) error {
	members, err := a.keycloak.GroupMembers(c.Request().Context(), c.Param("id"))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, &membersResponse{Members: members})
}

// AddMembersHandler handles POST /groups/:id/members (bulk add).
//
//	@Summary	Add members to a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	body	body	membersRequest	true	"Usernames to add"
//	@Success	200	{object}	membersResults
//	@Router	/groups/{id}/members [post]
func (a *App) AddMembersHandler(c echo.Context) error {
	return a.bulkMembership(c, a.keycloak.AddMember)
}

// RemoveMembersHandler handles POST /groups/:id/members/deleter (bulk remove).
//
//	@Summary	Remove members from a group
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	body	body	membersRequest	true	"Usernames to remove"
//	@Success	200	{object}	membersResults
//	@Router	/groups/{id}/members/deleter [post]
func (a *App) RemoveMembersHandler(c echo.Context) error {
	return a.bulkMembership(c, a.keycloak.RemoveMember)
}

// ReplaceMembersHandler handles PUT /groups/:id/members. It makes the group's
// membership exactly match the supplied list: members not in the list are
// removed and members not already present are added.
//
//	@Summary	Replace all group members
//	@Accept	json
//	@Produce	json
//	@Param	id	path	string	true	"Group UUID"
//	@Param	body	body	membersRequest	true	"The complete desired membership"
//	@Success	200	{object}	membersResults
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members [put]
func (a *App) ReplaceMembersHandler(c echo.Context) error {
	ctx := c.Request().Context()
	groupID := c.Param("id")

	var req membersRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	current, err := a.keycloak.GroupMembers(ctx, groupID)
	if err != nil {
		return keycloakError(err)
	}

	desired := make(map[string]bool, len(req.Members))
	for _, m := range req.Members {
		desired[m] = true
	}
	existing := make(map[string]bool, len(current))
	for _, m := range current {
		existing[m.ID] = true
	}

	results := make([]memberResult, 0, len(req.Members)+len(current))

	// Remove members that are no longer desired.
	for _, m := range current {
		if !desired[m.ID] {
			results = append(results, runMembership(ctx, a.keycloak.RemoveMember, groupID, m.ID))
		}
	}
	// Add members that aren't already present.
	for _, m := range req.Members {
		if !existing[m] {
			results = append(results, runMembership(ctx, a.keycloak.AddMember, groupID, m))
		}
	}

	return c.JSON(http.StatusOK, &membersResults{Results: results})
}

// AddMemberHandler handles PUT /groups/:id/members/:subject (single add).
//
//	@Summary	Add a single member to a group
//	@Param	id	path	string	true	"Group UUID"
//	@Param	subject	path	string	true	"Username (subject ID)"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members/{subject} [put]
func (a *App) AddMemberHandler(c echo.Context) error {
	if err := a.keycloak.AddMember(c.Request().Context(), c.Param("id"), c.Param("subject")); err != nil {
		return keycloakError(err)
	}
	return c.NoContent(http.StatusOK)
}

// RemoveMemberHandler handles DELETE /groups/:id/members/:subject (single remove).
//
//	@Summary	Remove a single member from a group
//	@Param	id	path	string	true	"Group UUID"
//	@Param	subject	path	string	true	"Username (subject ID)"
//	@Success	200
//	@Failure	404	{object}	map[string]string
//	@Router	/groups/{id}/members/{subject} [delete]
func (a *App) RemoveMemberHandler(c echo.Context) error {
	if err := a.keycloak.RemoveMember(c.Request().Context(), c.Param("id"), c.Param("subject")); err != nil {
		return keycloakError(err)
	}
	return c.NoContent(http.StatusOK)
}

// membershipFunc is the shared signature of AddMember/RemoveMember.
type membershipFunc func(ctx context.Context, groupID, username string) error

// bulkMembership applies op to each member in the request body, collecting a
// per-member result so that one failure does not abort the whole batch.
func (a *App) bulkMembership(c echo.Context, op membershipFunc) error {
	ctx := c.Request().Context()
	groupID := c.Param("id")

	var req membersRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	results := make([]memberResult, 0, len(req.Members))
	for _, username := range req.Members {
		results = append(results, runMembership(ctx, op, groupID, username))
	}

	return c.JSON(http.StatusOK, &membersResults{Results: results})
}

// runMembership executes a single membership change and converts it to a result.
func runMembership(ctx context.Context, op membershipFunc, groupID, username string) memberResult {
	result := memberResult{SubjectID: username, Success: true}
	if err := op(ctx, groupID, username); err != nil {
		result.Success = false
		result.Error = err.Error()
	}
	return result
}
