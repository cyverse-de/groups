package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/userinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchSubjects(t *testing.T) {
	app := newTestApp(&mockStore{})
	app.userinfo = &mockUserInfo{
		searchFn: func(_ context.Context, search string) ([]model.Subject, error) {
			assert.Equal(t, "jane", search)
			return []model.Subject{{ID: "jdoe", FirstName: "Jane"}}, nil
		},
	}

	rec := doRequest(app, http.MethodGet, "/subjects?search=jane", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp subjectsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Subjects, 1)
	assert.Equal(t, "jdoe", resp.Subjects[0].ID)
}

func TestGetSubjectNotFound(t *testing.T) {
	app := newTestApp(&mockStore{})
	app.userinfo = &mockUserInfo{
		getFn: func(context.Context, string) (*model.Subject, error) {
			return nil, userinfo.ErrNotFound
		},
	}

	rec := doRequest(app, http.MethodGet, "/subjects/ghost", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestLookupSubjectsSkipsMissing(t *testing.T) {
	app := newTestApp(&mockStore{})
	app.userinfo = &mockUserInfo{
		getManyFn: func(_ context.Context, usernames []string) ([]model.Subject, error) {
			assert.Equal(t, []string{"alice", "missing", "bob"}, usernames)
			// The directory omits identifiers it cannot resolve.
			return []model.Subject{{ID: "alice"}, {ID: "bob"}}, nil
		},
	}

	rec := doRequest(app, http.MethodPost, "/subjects/lookup", `{"subject_ids":["alice","missing","bob"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp subjectsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Subjects, 2)
	assert.Equal(t, "alice", resp.Subjects[0].ID)
	assert.Equal(t, "bob", resp.Subjects[1].ID)
}

func TestSubjectGroups(t *testing.T) {
	t.Run("returns the subject's groups", func(t *testing.T) {
		app := newTestApp(&mockStore{
			groupsForSubjectFn: func(_ context.Context, username, groupType string) ([]model.Group, error) {
				assert.Equal(t, "jdoe", username)
				assert.Empty(t, groupType)
				return []model.Group{{ID: "g1", GroupType: model.GroupTypeTeam, Name: "Ecology"}}, nil
			},
		})

		rec := doRequest(app, http.MethodGet, "/subjects/jdoe/groups", "")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp groupListResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Groups, 1)
		assert.Equal(t, "Ecology", resp.Groups[0].Name)
	})

	t.Run("passes the group type filter through", func(t *testing.T) {
		app := newTestApp(&mockStore{
			groupsForSubjectFn: func(_ context.Context, _, groupType string) ([]model.Group, error) {
				assert.Equal(t, model.GroupTypeCommunity, groupType)
				return nil, nil
			},
		})

		rec := doRequest(app, http.MethodGet, "/subjects/jdoe/groups?group_type=community", "")
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestSubjectsRequireUser(t *testing.T) {
	app := newTestApp(&mockStore{})

	rec := doRequestAs(app, http.MethodGet, "/subjects/jdoe", "", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
