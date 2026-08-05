package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
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

func TestListGroups(t *testing.T) {
	var captured store.ListFilter
	app := newTestApp(&mockStore{
		listGroupsFn: func(_ context.Context, f store.ListFilter) ([]model.Group, error) {
			captured = f
			return []model.Group{{ID: "1", GroupType: model.GroupTypeTeam, Name: "Ecology"}}, nil
		},
	})

	rec := doRequest(app, http.MethodGet,
		"/groups?group_type=team&owner=alice&member=bob&search=eco&limit=5&offset=10", "")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, model.GroupTypeTeam, captured.GroupType)
	assert.Equal(t, "alice", captured.Owner)
	assert.Equal(t, "bob", captured.Member)
	assert.Equal(t, "eco", captured.Search)
	assert.Equal(t, 5, captured.Limit)
	assert.Equal(t, 10, captured.Offset)

	var resp groupListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Groups, 1)
	assert.Equal(t, "Ecology", resp.Groups[0].Name)
}

func TestListGroupsCapsTheLimit(t *testing.T) {
	var captured store.ListFilter
	app := newTestApp(&mockStore{
		listGroupsFn: func(_ context.Context, f store.ListFilter) ([]model.Group, error) {
			captured = f
			return nil, nil
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups?limit=100000", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, maxListLimit, captured.Limit,
		"an oversized limit must be capped rather than passed through")
}

func TestListGroupsRejectsBadPaging(t *testing.T) {
	app := newTestApp(&mockStore{})

	for _, target := range []string{"/groups?limit=abc", "/groups?offset=-1"} {
		rec := doRequest(app, http.MethodGet, target, "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, target)
	}
}

func TestLookupGroup(t *testing.T) {
	t.Run("resolves an identity", func(t *testing.T) {
		var captured model.Ref
		app := newTestApp(&mockStore{
			lookupGroupFn: func(_ context.Context, ref model.Ref) (*model.Group, error) {
				captured = ref
				return &model.Group{ID: "g1", GroupType: ref.GroupType, Owner: ref.Owner, Name: ref.Name}, nil
			},
		})

		rec := doRequest(app, http.MethodGet,
			"/groups/lookup?group_type=collaborator_list&owner=alice&name=default", "")
		require.Equal(t, http.StatusOK, rec.Code)

		assert.Equal(t, model.GroupTypeCollaboratorList, captured.GroupType)
		assert.Equal(t, "alice", captured.Owner)
		assert.Equal(t, "default", captured.Name)
	})

	t.Run("requires type and name", func(t *testing.T) {
		app := newTestApp(&mockStore{})
		rec := doRequest(app, http.MethodGet, "/groups/lookup?owner=alice", "")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("reports a missing group as 404", func(t *testing.T) {
		app := newTestApp(&mockStore{
			lookupGroupFn: func(context.Context, model.Ref) (*model.Group, error) {
				return nil, store.ErrNotFound
			},
		})
		rec := doRequest(app, http.MethodGet, "/groups/lookup?group_type=team&name=nope", "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("is not shadowed by the group-by-id route", func(t *testing.T) {
		app := newTestApp(&mockStore{
			getGroupFn: func(_ context.Context, id string) (*model.Group, error) {
				t.Errorf("/groups/lookup was routed to GetGroup with id %q", id)
				return nil, store.ErrNotFound
			},
		})
		rec := doRequest(app, http.MethodGet, "/groups/lookup?group_type=team&name=x", "")
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestGetGroupNotFound(t *testing.T) {
	app := newTestApp(&mockStore{
		getGroupFn: func(context.Context, string) (*model.Group, error) {
			return nil, store.ErrNotFound
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups/missing", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAddGroup(t *testing.T) {
	t.Run("passes the structured identity through", func(t *testing.T) {
		var captured model.GroupSpec
		app := newTestApp(&mockStore{
			createGroupFn: func(_ context.Context, spec model.GroupSpec) (*model.Group, error) {
				captured = spec
				return &model.Group{ID: "new-id", GroupType: spec.GroupType, Name: spec.Name}, nil
			},
		})

		rec := doRequestAs(app, http.MethodPost, "/groups",
			`{"group_type":"team","name":"Ecology","description":"A team","display_name":"Ecology Lab"}`,
			"alice")
		require.Equal(t, http.StatusOK, rec.Code)

		assert.Equal(t, model.GroupTypeTeam, captured.GroupType)
		assert.Equal(t, "Ecology", captured.Name)
		assert.Equal(t, "A team", captured.Description)
		assert.Equal(t, "Ecology Lab", captured.DisplayName)
		assert.Equal(t, "alice", captured.Owner, "owner defaults to the acting user")

		var g model.Group
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &g))
		assert.Equal(t, "new-id", g.ID)
	})

	t.Run("clears the owner for globally named types", func(t *testing.T) {
		var captured model.GroupSpec
		app := newTestApp(&mockStore{
			createGroupFn: func(_ context.Context, spec model.GroupSpec) (*model.Group, error) {
				captured = spec
				return &model.Group{ID: "new-id", GroupType: spec.GroupType, Name: spec.Name}, nil
			},
		})

		rec := doRequestAs(app, http.MethodPost, "/groups",
			`{"group_type":"community","name":"NEON"}`, "alice")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, captured.Owner, "a community must not inherit the creator as owner")
	})

	t.Run("refuses to create a group in another user's namespace", func(t *testing.T) {
		app := newTestApp(&mockStore{})
		rec := doRequestAs(app, http.MethodPost, "/groups",
			`{"group_type":"team","owner":"bob","name":"Ecology"}`, "alice")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("lets a trusted service account create for another user", func(t *testing.T) {
		app := newTestApp(&mockStore{})
		app.adminUsers = map[string]struct{}{"de_grouper": {}}

		rec := doRequestAs(app, http.MethodPost, "/groups",
			`{"group_type":"team","owner":"bob","name":"Ecology"}`, "de_grouper")
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("validates the body", func(t *testing.T) {
		cases := []struct{ name, body string }{
			{"missing name", `{"group_type":"team"}`},
			{"missing group type", `{"name":"Ecology"}`},
			{"unknown group type", `{"group_type":"folder","name":"Ecology"}`},
		}
		app := newTestApp(&mockStore{})
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := doRequest(app, http.MethodPost, "/groups", tc.body)
				assert.Equal(t, http.StatusBadRequest, rec.Code)
			})
		}
	})

	t.Run("reports a duplicate identity as a conflict", func(t *testing.T) {
		app := newTestApp(&mockStore{
			createGroupFn: func(context.Context, model.GroupSpec) (*model.Group, error) {
				return nil, store.ErrConflict
			},
		})
		rec := doRequest(app, http.MethodPost, "/groups", `{"group_type":"team","name":"Ecology"}`)
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
}

func TestUpdateGroup(t *testing.T) {
	t.Run("only sends the fields present in the body", func(t *testing.T) {
		var captured model.GroupUpdate
		app := newTestApp(&mockStore{
			updateGroupFn: func(_ context.Context, id string, upd model.GroupUpdate) (*model.Group, error) {
				captured = upd
				return &model.Group{ID: id, GroupType: model.GroupTypeTeam, Name: "Ecology"}, nil
			},
		})

		rec := doRequest(app, http.MethodPut, "/groups/g1", `{"description":"changed"}`)
		require.Equal(t, http.StatusOK, rec.Code)

		assert.Nil(t, captured.Name, "an absent name must not be sent as an update")
		require.NotNil(t, captured.Description)
		assert.Equal(t, "changed", *captured.Description)
	})

	t.Run("rejects an empty name", func(t *testing.T) {
		app := newTestApp(&mockStore{})
		rec := doRequest(app, http.MethodPut, "/groups/g1", `{"name":""}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestDeleteGroup(t *testing.T) {
	called := false
	app := newTestApp(&mockStore{
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

// The listing is access-filtered by the acting user, except for the
// administrative service accounts, which terrain relies on for its own lookups.
func TestListGroupsFiltersByActingUser(t *testing.T) {
	tests := []struct {
		name          string
		user          string
		wantVisibleTo string
	}{
		{name: "an ordinary user sees only what they may read", user: "alice", wantVisibleTo: "alice"},
		{name: "an admin service account sees everything", user: "permissions-svc", wantVisibleTo: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got store.ListFilter
			s := &mockStore{
				listGroupsFn: func(_ context.Context, f store.ListFilter) ([]model.Group, error) {
					got = f
					return nil, nil
				},
			}
			app := newTestAppWith(s, &mockPermissions{})
			app.adminUsers = map[string]struct{}{"permissions-svc": {}}

			rec := doRequestAs(app, http.MethodGet, "/groups", "", tt.user)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantVisibleTo, got.VisibleTo)
		})
	}
}
