package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doRequest serves a single request against the app and returns the recorder.
// It defaults the required `user` query parameter to "tester" so tests that
// aren't exercising the user requirement don't have to set it explicitly.
func doRequest(app *App, method, target, body string) *httptest.ResponseRecorder {
	return doRequestAs(app, method, target, body, "tester")
}

// doRequestAs serves a request with an explicit acting user. An empty user
// sends no `user` query parameter at all.
func doRequestAs(app *App, method, target, body, user string) *httptest.ResponseRecorder {
	if user != "" && !strings.Contains(target, "user=") {
		if strings.Contains(target, "?") {
			target += "&user=" + user
		} else {
			target += "?user=" + user
		}
	}

	reader := strings.NewReader(body)
	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.router.ServeHTTP(rec, req)
	return rec
}

func TestSearchGroups(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		searchGroupsFn: func(_ context.Context, search string) ([]keycloak.Group, error) {
			assert.Equal(t, "team", search)
			return []keycloak.Group{{ID: "1", Name: "team-a"}}, nil
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups?search=team", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp groupListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 1)
	assert.Equal(t, "team-a", resp.Groups[0].Name)
}

func TestGetGroupNotFound(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		getGroupFn: func(_ context.Context, _ string) (*keycloak.Group, error) {
			return nil, keycloak.ErrNotFound
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups/missing", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAddGroup(t *testing.T) {
	var captured keycloak.GroupSpec
	app := newTestApp(&mockKeycloak{
		createGroupFn: func(_ context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error) {
			captured = spec
			return &keycloak.Group{ID: "new-id", Name: spec.Name, Description: spec.Description}, nil
		},
	})

	rec := doRequest(app, http.MethodPost, "/groups",
		`{"name":"team-a","description":"A team","display_extension":"Team A"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "team-a", captured.Name)
	assert.Equal(t, "A team", captured.Description)
	assert.Equal(t, "Team A", captured.DisplayExtension)

	var g keycloak.Group
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
	assert.Equal(t, "new-id", g.ID)
}

func TestAddGroupRequiresName(t *testing.T) {
	app := newTestApp(&mockKeycloak{})

	rec := doRequest(app, http.MethodPost, "/groups", `{"description":"no name"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDeleteGroup(t *testing.T) {
	called := false
	app := newTestApp(&mockKeycloak{
		deleteGroupFn: func(_ context.Context, id string) error {
			called = true
			assert.Equal(t, "abc", id)
			return nil
		},
	})

	rec := doRequest(app, http.MethodDelete, "/groups/abc", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, called)
}
