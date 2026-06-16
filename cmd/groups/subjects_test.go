package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchSubjects(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		searchSubjectsFn: func(_ context.Context, search string) ([]keycloak.Subject, error) {
			assert.Equal(t, "jane", search)
			return []keycloak.Subject{{ID: "jdoe", FirstName: "Jane"}}, nil
		},
	})

	rec := doRequest(app, http.MethodGet, "/subjects?search=jane", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp subjectsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Subjects, 1)
	assert.Equal(t, "jdoe", resp.Subjects[0].ID)
}

func TestGetSubjectNotFound(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		getSubjectFn: func(_ context.Context, _ string) (*keycloak.Subject, error) {
			return nil, keycloak.ErrNotFound
		},
	})

	rec := doRequest(app, http.MethodGet, "/subjects/ghost", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestLookupSubjectsSkipsMissing(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		getSubjectFn: func(_ context.Context, id string) (*keycloak.Subject, error) {
			if id == "missing" {
				return nil, keycloak.ErrNotFound
			}
			return &keycloak.Subject{ID: id}, nil
		},
	})

	rec := doRequest(app, http.MethodPost, "/subjects/lookup", `{"subject_ids":["alice","missing","bob"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp subjectsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Subjects, 2)
	assert.Equal(t, "alice", resp.Subjects[0].ID)
	assert.Equal(t, "bob", resp.Subjects[1].ID)
}

func TestSubjectGroups(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		subjectGroupsFn: func(_ context.Context, username string) ([]keycloak.Group, error) {
			assert.Equal(t, "jdoe", username)
			return []keycloak.Group{{ID: "g1", Name: "team-a"}}, nil
		},
	})

	rec := doRequest(app, http.MethodGet, "/subjects/jdoe/groups", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp groupListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 1)
	assert.Equal(t, "team-a", resp.Groups[0].Name)
}

func TestSubjectsRequireUser(t *testing.T) {
	app := newTestApp(&mockKeycloak{})

	rec := doRequestAs(app, http.MethodGet, "/subjects/jdoe", "", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
