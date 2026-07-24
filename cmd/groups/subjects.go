package main

import (
	"errors"
	"net/http"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/userinfo"
	"github.com/labstack/echo/v4"
)

// subjectsResponse wraps a list of subjects.
type subjectsResponse struct {
	Subjects []model.Subject `json:"subjects"`
}

// lookupRequest is the body for a bulk subject lookup.
type lookupRequest struct {
	SubjectIDs []string `json:"subject_ids"`
}

// SearchSubjectsHandler handles GET /subjects.
//
//	@Summary	Search subjects
//	@Produce	json
//	@Param	search	query	string	false	"Search term matched against users"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	subjectsResponse
//	@Router	/subjects [get]
func (a *App) SearchSubjectsHandler(c echo.Context) error {
	subjects, err := a.userinfo.Search(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, &subjectsResponse{Subjects: subjects})
}

// LookupSubjectsHandler handles POST /subjects/lookup. Subject IDs that do not
// resolve to a user are omitted from the response, matching iplant-groups.
//
//	@Summary	Look up multiple subjects by ID
//	@Accept	json
//	@Produce	json
//	@Param	user	query	string	true	"The acting user"
//	@Param	body	body	lookupRequest	true	"The subject IDs to look up"
//	@Success	200	{object}	subjectsResponse
//	@Router	/subjects/lookup [post]
func (a *App) LookupSubjectsHandler(c echo.Context) error {
	var req lookupRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	subjects, err := a.userinfo.GetMany(c.Request().Context(), req.SubjectIDs)
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, &subjectsResponse{Subjects: subjects})
}

// GetSubjectHandler handles GET /subjects/:subject-id.
//
//	@Summary	Get a subject by ID
//	@Produce	json
//	@Param	subject-id	path	string	true	"Subject identifier (username)"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	model.Subject
//	@Failure	404	{object}	map[string]string
//	@Router	/subjects/{subject-id} [get]
func (a *App) GetSubjectHandler(c echo.Context) error {
	subject, err := a.userinfo.Get(c.Request().Context(), c.Param("subject-id"))
	if err != nil {
		if errors.Is(err, userinfo.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return err
	}
	return c.JSON(http.StatusOK, subject)
}

// SubjectGroupsHandler handles GET /subjects/:subject-id/groups. Groups reached
// through nesting are included, matching what Grouper reported and what
// permission lookup expands.
//
//	@Summary	List the groups a subject belongs to
//	@Produce	json
//	@Param	subject-id	path	string	true	"Subject identifier (username)"
//	@Param	group_type	query	string	false	"Restrict to one kind of group"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	groupListResponse
//	@Router	/subjects/{subject-id}/groups [get]
func (a *App) SubjectGroupsHandler(c echo.Context) error {
	groups, err := a.store.GroupsForSubject(c.Request().Context(),
		c.Param("subject-id"), c.QueryParam("group_type"))
	if err != nil {
		return storeError(err)
	}
	return c.JSON(http.StatusOK, &groupListResponse{Groups: groups})
}
