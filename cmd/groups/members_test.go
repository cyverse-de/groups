package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMembers(t *testing.T) {
	t.Run("hydrates users from the directory", func(t *testing.T) {
		app := newTestApp(&mockStore{
			listMembersFn: func(_ context.Context, id string) ([]model.MemberRef, error) {
				assert.Equal(t, "g1", id)
				return []model.MemberRef{
					{ID: "alice", Type: model.MemberTypeUser},
					{ID: "bob", Type: model.MemberTypeUser},
				}, nil
			},
		})
		app.userinfo = &mockUserInfo{
			getManyFn: func(_ context.Context, usernames []string) ([]model.Subject, error) {
				assert.ElementsMatch(t, []string{"alice", "bob"}, usernames,
					"users must be resolved in one batch")
				return []model.Subject{
					{ID: "alice", Name: "Alice A", Email: "alice@example.org", SourceID: model.SourceUser},
					{ID: "bob", Name: "Bob B", SourceID: model.SourceUser},
				}, nil
			},
		}

		rec := doRequest(app, http.MethodGet, "/groups/g1/members", "")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp membersResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Members, 2)
		assert.Equal(t, "Alice A", resp.Members[0].Name)
		assert.Equal(t, "alice@example.org", resp.Members[0].Email)
	})

	t.Run("reports a nested group with the group source", func(t *testing.T) {
		app := newTestApp(&mockStore{
			listMembersFn: func(context.Context, string) ([]model.MemberRef, error) {
				return []model.MemberRef{
					{ID: "team-1", Type: model.MemberTypeGroup},
					{ID: "alice", Type: model.MemberTypeUser},
				}, nil
			},
			getGroupFn: func(_ context.Context, id string) (*model.Group, error) {
				return &model.Group{ID: id, GroupType: model.GroupTypeTeam, Name: "Ecology"}, nil
			},
		})

		rec := doRequest(app, http.MethodGet, "/groups/g1/members", "")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp membersResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Members, 2)

		byID := map[string]model.Subject{}
		for _, m := range resp.Members {
			byID[m.ID] = m
		}
		assert.Equal(t, model.SourceGroup, byID["team-1"].SourceID)
		assert.Equal(t, "Ecology", byID["team-1"].Name)
		assert.Equal(t, model.SourceUser, byID["alice"].SourceID)
	})

	t.Run("keeps a member the directory does not know", func(t *testing.T) {
		app := newTestApp(&mockStore{
			listMembersFn: func(context.Context, string) ([]model.MemberRef, error) {
				return []model.MemberRef{{ID: "ghost", Type: model.MemberTypeUser}}, nil
			},
		})
		app.userinfo = &mockUserInfo{
			getManyFn: func(context.Context, []string) ([]model.Subject, error) {
				return []model.Subject{}, nil
			},
		}

		rec := doRequest(app, http.MethodGet, "/groups/g1/members", "")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp membersResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Members, 1,
			"membership lives in our database, so an unknown user must still be reported")
		assert.Equal(t, "ghost", resp.Members[0].ID)
	})

	t.Run("reports a missing group as 404", func(t *testing.T) {
		app := newTestApp(&mockStore{
			listMembersFn: func(context.Context, string) ([]model.MemberRef, error) {
				return nil, store.ErrNotFound
			},
		})
		rec := doRequest(app, http.MethodGet, "/groups/missing/members", "")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	// A directory outage degrades display data rather than authorization:
	// membership lives in our database, so the list is still served.
	t.Run("survives a directory outage", func(t *testing.T) {
		app := newTestApp(&mockStore{
			listMembersFn: func(context.Context, string) ([]model.MemberRef, error) {
				return []model.MemberRef{
					{ID: "alice", Type: model.MemberTypeUser},
					{ID: "team-1", Type: model.MemberTypeGroup},
				}, nil
			},
			getGroupFn: func(_ context.Context, id string) (*model.Group, error) {
				return &model.Group{ID: id, Name: "Ecology"}, nil
			},
		})
		app.userinfo = &mockUserInfo{
			getManyFn: func(context.Context, []string) ([]model.Subject, error) {
				return nil, assert.AnError
			},
		}

		rec := doRequest(app, http.MethodGet, "/groups/g1/members", "")
		require.Equal(t, http.StatusOK, rec.Code)

		var resp membersResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Len(t, resp.Members, 2)

		byID := map[string]model.Subject{}
		for _, m := range resp.Members {
			byID[m.ID] = m
		}
		assert.Equal(t, "alice", byID["alice"].Name, "falls back to the bare identifier")
		assert.Equal(t, model.SourceUser, byID["alice"].SourceID)
		assert.Equal(t, "Ecology", byID["team-1"].Name, "a nested group is described from the store")
	})
}

func TestAddMembersBulkCollectsResults(t *testing.T) {
	app := newTestApp(&mockStore{
		addMembersFn: func(_ context.Context, _ string, ids []string, addedBy string) ([]model.MemberChange, error) {
			assert.Equal(t, "tester", addedBy)
			changes := make([]model.MemberChange, 0, len(ids))
			for _, id := range ids {
				change := model.MemberChange{SubjectID: id, Type: model.MemberTypeUser}
				if id == "bad" {
					change.Err = errors.New("nope")
				}
				changes = append(changes, change)
			}
			return changes, nil
		},
	})

	rec := doRequest(app, http.MethodPost, "/groups/g1/members",
		`{"members":["alice","bad","carol"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp membersResults
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 3)

	byID := map[string]memberResult{}
	for _, r := range resp.Results {
		byID[r.SubjectID] = r
	}
	assert.True(t, byID["alice"].Success)
	assert.Equal(t, model.SourceUser, byID["alice"].SourceID)
	assert.Equal(t, "alice", byID["alice"].SubjectName)
	assert.False(t, byID["bad"].Success)
	assert.Equal(t, "nope", byID["bad"].Error)
	assert.Empty(t, byID["bad"].SourceID, "a failed change has no resolved subject")
	assert.True(t, byID["carol"].Success)
}

func TestAddMembersReportsNestedGroups(t *testing.T) {
	app := newTestApp(&mockStore{
		addMembersFn: func(context.Context, string, []string, string) ([]model.MemberChange, error) {
			return []model.MemberChange{{SubjectID: "team-1", Type: model.MemberTypeGroup}}, nil
		},
		getGroupFn: func(_ context.Context, id string) (*model.Group, error) {
			return &model.Group{ID: id, Name: "Ecology"}, nil
		},
	})

	rec := doRequest(app, http.MethodPost, "/groups/g1/members", `{"members":["team-1"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp membersResults
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 1)
	assert.Equal(t, model.SourceGroup, resp.Results[0].SourceID)
	assert.Equal(t, "Ecology", resp.Results[0].SubjectName)
}

// A directory outage must not fail a membership change that already committed;
// the names are decoration on a completed operation.
func TestAddMembersSurvivesDirectoryFailure(t *testing.T) {
	app := newTestApp(&mockStore{})
	app.userinfo = &mockUserInfo{
		getManyFn: func(context.Context, []string) ([]model.Subject, error) {
			return nil, assert.AnError
		},
	}

	rec := doRequest(app, http.MethodPost, "/groups/g1/members", `{"members":["alice"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp membersResults
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 1)
	assert.True(t, resp.Results[0].Success)
	assert.Equal(t, "alice", resp.Results[0].SubjectName, "falls back to the subject id")
}

func TestReplaceMembersReconciles(t *testing.T) {
	var captured []string
	app := newTestApp(&mockStore{
		replaceMembersFn: func(_ context.Context, _ string, ids []string, _ string) ([]model.MemberChange, error) {
			captured = ids
			return []model.MemberChange{
				{SubjectID: "bob", Type: model.MemberTypeUser},
				{SubjectID: "carol", Type: model.MemberTypeUser},
			}, nil
		},
	})

	rec := doRequest(app, http.MethodPut, "/groups/g1/members", `{"members":["alice","carol"]}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"alice", "carol"}, captured)

	var resp membersResults
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp.Results, 2, "only the differences are reported")
}

func TestSingleMemberOperations(t *testing.T) {
	t.Run("remove", func(t *testing.T) {
		called := false
		app := newTestApp(&mockStore{
			removeMembersFn: func(_ context.Context, groupID string, ids []string) ([]model.MemberChange, error) {
				called = true
				assert.Equal(t, "g1", groupID)
				assert.Equal(t, []string{"alice"}, ids)
				return changesFor(ids), nil
			},
		})

		rec := doRequest(app, http.MethodDelete, "/groups/g1/members/alice", "")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, called)
	})

	// A user the identity provider does not know is bad input, not a missing
	// group: the single-member add must answer 400.
	t.Run("add reports an unknown user as a 400", func(t *testing.T) {
		app := newTestApp(&mockStore{
			existingSubjectsFn: func(context.Context, []string) ([]string, error) {
				return nil, nil
			},
			addMembersFn: func(context.Context, string, []string, string) ([]model.MemberChange, error) {
				t.Error("an unknown user must be rejected before reaching the store")
				return nil, nil
			},
		})
		app.userinfo = &mockUserInfo{
			getManyFn: func(context.Context, []string) ([]model.Subject, error) {
				return nil, nil
			},
		}

		rec := doRequest(app, http.MethodPut, "/groups/g1/members/ghost", "")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	// A rejected single member is an error, not a 200 carrying a failed result.
	t.Run("add reports a rejected member as an error", func(t *testing.T) {
		app := newTestApp(&mockStore{
			addMembersFn: func(_ context.Context, _ string, ids []string, _ string) ([]model.MemberChange, error) {
				return []model.MemberChange{{SubjectID: ids[0], Err: store.ErrCycle}}, nil
			},
		})

		rec := doRequest(app, http.MethodPut, "/groups/g1/members/team-1", "")
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
}

// Consumers tell a user from a nested group by source_id, so a change whose kind
// the store could not report -- removing a subject that was not a member -- must
// carry none rather than be labelled a user.
func TestRemoveMembersReportsTheSubjectKind(t *testing.T) {
	tests := []struct {
		name         string
		changeType   string
		wantSourceID string
	}{
		{name: "a removed user", changeType: model.MemberTypeUser, wantSourceID: model.SourceUser},
		{name: "a removed nested group", changeType: model.MemberTypeGroup, wantSourceID: model.SourceGroup},
		{name: "a subject that was not a member", changeType: "", wantSourceID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(&mockStore{
				removeMembersFn: func(_ context.Context, _ string, ids []string) ([]model.MemberChange, error) {
					return []model.MemberChange{{SubjectID: ids[0], Type: tt.changeType}}, nil
				},
			})

			rec := doRequest(app, http.MethodPost, "/groups/g1/members/deleter", `{"members":["team-1"]}`)
			require.Equal(t, http.StatusOK, rec.Code)

			var resp membersResults
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Len(t, resp.Results, 1)
			assert.True(t, resp.Results[0].Success)
			assert.Equal(t, tt.wantSourceID, resp.Results[0].SourceID)
		})
	}
}

func TestBulkMembershipSizeLimit(t *testing.T) {
	app := newTestApp(&mockStore{})

	members := make([]string, maxBulkMembers+1)
	for i := range members {
		members[i] = "user"
	}
	body := `{"members":["` + strings.Join(members, `","`) + `"]}`

	rec := doRequest(app, http.MethodPost, "/groups/g1/members", body)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
