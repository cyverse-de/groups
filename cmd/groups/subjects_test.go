package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
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

// Each subject ID costs a round trip to the directory, so an unbounded list is
// an unbounded fan-out of calls to it.
func TestLookupSubjectsSizeLimit(t *testing.T) {
	app := newTestApp(&mockStore{})
	app.userinfo = &mockUserInfo{
		getManyFn: func(context.Context, []string) ([]model.Subject, error) {
			t.Error("an oversized lookup must be rejected before reaching the directory")
			return nil, nil
		},
	}

	ids := make([]string, maxBulkMembers+1)
	for i := range ids {
		ids[i] = "user"
	}
	body := `{"subject_ids":["` + strings.Join(ids, `","`) + `"]}`

	rec := doRequest(app, http.MethodPost, "/subjects/lookup", body)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestSubjectGroups(t *testing.T) {
	t.Run("returns the subject's groups", func(t *testing.T) {
		app := newTestApp(&mockStore{
			groupsForSubjectFn: func(_ context.Context, username, groupType, _ string) ([]model.Group, error) {
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
			groupsForSubjectFn: func(_ context.Context, _, groupType, _ string) ([]model.Group, error) {
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

// A subject's group listing is filtered by what the caller may see, exactly as
// /groups is. Grouper's equivalent query returned nothing to an unrelated user;
// leaving this one unfiltered lets anybody enumerate another user's entire
// membership, private collaborator lists included.
func TestSubjectGroupsFiltersByActingUser(t *testing.T) {
	tests := []struct {
		name, user, wantVisibleTo string
	}{
		{name: "an ordinary caller is filtered", user: "alice", wantVisibleTo: "alice"},
		{name: "an admin service account is not", user: "permissions-svc", wantVisibleTo: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			s := &mockStore{
				groupsForSubjectFn: func(_ context.Context, _, _, visibleTo string) ([]model.Group, error) {
					got = visibleTo
					return nil, nil
				},
			}
			app := newTestAppWith(s, &mockPermissions{})
			app.adminUsers = map[string]struct{}{"permissions-svc": {}}

			rec := doRequestAs(app, http.MethodGet, "/subjects/bob/groups", "", tt.user)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantVisibleTo, got)
		})
	}
}

// Groups are subjects too. Grouper's subject search returned teams and
// collaborator lists alongside users, tagged source_id "g:gsa", and the DE's
// sharing dialog recognizes a group by exactly that: without them, sharing data
// or an app with a team is unreachable from the UI.
func TestSearchSubjectsIncludesGroups(t *testing.T) {
	var gotFilter store.ListFilter
	s := &mockStore{
		listGroupsFn: func(_ context.Context, f store.ListFilter) ([]model.Group, error) {
			gotFilter = f
			return []model.Group{
				{ID: "abc123", Name: "Field Team", Description: "field sampling"},
			}, nil
		},
	}
	app := newTestAppWith(s, &mockPermissions{})
	app.userinfo = &mockUserInfo{
		searchFn: func(context.Context, string) ([]model.Subject, error) {
			return []model.Subject{{ID: "bob", Name: "bob", SourceID: model.SourceUser}}, nil
		},
	}

	rec := doRequestAs(app, http.MethodGet, "/subjects?search=Field", "", "alice")
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Subjects []model.Subject `json:"subjects"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	var group *model.Subject
	for i, sub := range body.Subjects {
		if sub.SourceID == model.SourceGroup {
			group = &body.Subjects[i]
		}
	}
	require.NotNil(t, group, "the search must return group subjects, not only users")
	assert.Equal(t, "abc123", group.ID, "a group subject is identified by its external group id")
	assert.Equal(t, "Field Team", group.Name)

	// Users are still returned; the group half is additive, not a replacement.
	var user *model.Subject
	for i, sub := range body.Subjects {
		if sub.SourceID == model.SourceUser {
			user = &body.Subjects[i]
		}
	}
	require.NotNil(t, user, "user subjects must still be returned")
	assert.Equal(t, "bob", user.ID)

	// The search term reaches the store, and the listing is access-filtered so
	// the search cannot expose a private team's name.
	assert.Equal(t, "Field", gotFilter.Search)
	assert.Equal(t, "alice", gotFilter.VisibleTo)
}
