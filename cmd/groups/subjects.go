package main

import (
	"errors"
	"net/http"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/labstack/echo/v4"
)

// subjectsResponse wraps a list of subjects.
type subjectsResponse struct {
	Subjects []keycloak.Subject `json:"subjects"`
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
	subjects, err := a.keycloak.SearchSubjects(c.Request().Context(), c.QueryParam("search"))
	if err != nil {
		return keycloakError(err)
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

	ctx := c.Request().Context()
	subjects := make([]keycloak.Subject, 0, len(req.SubjectIDs))
	for _, id := range req.SubjectIDs {
		subject, err := a.keycloak.GetSubject(ctx, id)
		if err != nil {
			if errors.Is(err, keycloak.ErrNotFound) {
				continue
			}
			return keycloakError(err)
		}
		subjects = append(subjects, *subject)
	}

	return c.JSON(http.StatusOK, &subjectsResponse{Subjects: subjects})
}

// GetSubjectHandler handles GET /subjects/:subject-id.
//
//	@Summary	Get a subject by ID
//	@Produce	json
//	@Param	subject-id	path	string	true	"Subject identifier (username)"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	keycloak.Subject
//	@Failure	404	{object}	map[string]string
//	@Router	/subjects/{subject-id} [get]
func (a *App) GetSubjectHandler(c echo.Context) error {
	subject, err := a.keycloak.GetSubject(c.Request().Context(), c.Param("subject-id"))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, subject)
}

// SubjectGroupsHandler handles GET /subjects/:subject-id/groups.
//
//	@Summary	List the groups a subject belongs to
//	@Produce	json
//	@Param	subject-id	path	string	true	"Subject identifier (username)"
//	@Param	user	query	string	true	"The acting user"
//	@Success	200	{object}	groupListResponse
//	@Failure	404	{object}	map[string]string
//	@Router	/subjects/{subject-id}/groups [get]
func (a *App) SubjectGroupsHandler(c echo.Context) error {
	groups, err := a.keycloak.SubjectGroups(c.Request().Context(), c.Param("subject-id"))
	if err != nil {
		return keycloakError(err)
	}
	return c.JSON(http.StatusOK, &groupListResponse{Groups: groups})
}
