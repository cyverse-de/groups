package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddMembersValidatesNewUsers covers the rule that a username with no
// subject row must exist in the identity provider before the service creates
// one for it. Subjects that already exist are never re-validated: they may have
// been imported from Grouper or may have left the directory since, and neither
// should block a membership change.
func TestAddMembersValidatesNewUsers(t *testing.T) {
	tests := []struct {
		name string
		// members is the request body.
		members []string
		// existing are the subject IDs that already have a subjects row.
		existing []string
		// known are the usernames the identity provider resolves.
		known []string
		// userinfoErr makes the directory lookup fail.
		userinfoErr error

		wantStatus int
		// wantToStore is what the handler is expected to hand to the store.
		wantToStore []string
		// wantRejected are members reported as failures without reaching the store.
		wantRejected []string
		// wantLookedUp are the usernames the directory should be asked about.
		wantLookedUp []string
	}{
		{
			name:         "unknown to the store and the directory is rejected",
			members:      []string{"ghost"},
			wantStatus:   http.StatusOK,
			wantToStore:  nil,
			wantRejected: []string{"ghost"},
			wantLookedUp: []string{"ghost"},
		},
		{
			name:         "unknown to the store but known to the directory is added",
			members:      []string{"alice"},
			known:        []string{"alice"},
			wantStatus:   http.StatusOK,
			wantToStore:  []string{"alice"},
			wantLookedUp: []string{"alice"},
		},
		{
			name:         "an existing subject is added without consulting the directory",
			members:      []string{"legacy"},
			existing:     []string{"legacy"},
			wantStatus:   http.StatusOK,
			wantToStore:  []string{"legacy"},
			wantLookedUp: nil,
		},
		{
			name:         "a nested group is added without consulting the directory",
			members:      []string{"team-1"},
			existing:     []string{"team-1"},
			wantStatus:   http.StatusOK,
			wantToStore:  []string{"team-1"},
			wantLookedUp: nil,
		},
		{
			name:         "valid members survive alongside a rejected one",
			members:      []string{"alice", "ghost", "legacy"},
			existing:     []string{"legacy"},
			known:        []string{"alice"},
			wantStatus:   http.StatusOK,
			wantToStore:  []string{"alice", "legacy"},
			wantRejected: []string{"ghost"},
			wantLookedUp: []string{"alice", "ghost"},
		},
		{
			name:         "a directory failure fails the request rather than creating subjects",
			members:      []string{"alice"},
			userinfoErr:  errors.New("keycloak is unreachable"),
			wantStatus:   http.StatusBadGateway,
			wantToStore:  nil,
			wantLookedUp: []string{"alice"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotToStore []string
			var gotLookedUp []string
			// renderResults consults the directory again after the change, so
			// only lookups made before the store was touched are validation.
			storeCalled := false

			s := &mockStore{
				existingSubjectsFn: func(_ context.Context, ids []string) ([]string, error) {
					return intersect(ids, tt.existing), nil
				},
				addMembersFn: func(_ context.Context, _ string, ids []string, _ string) ([]model.MemberChange, error) {
					storeCalled = true
					gotToStore = ids
					return changesFor(ids), nil
				},
			}
			app := newTestApp(s)
			app.userinfo = &mockUserInfo{
				getManyFn: func(_ context.Context, usernames []string) ([]model.Subject, error) {
					if !storeCalled {
						gotLookedUp = usernames
					}
					if tt.userinfoErr != nil {
						return nil, tt.userinfoErr
					}
					return subjectsFor(intersect(usernames, tt.known)), nil
				},
			}

			body, err := json.Marshal(membersRequest{Members: tt.members})
			require.NoError(t, err)

			rec := doRequest(app, http.MethodPost, "/groups/g1/members", string(body))
			require.Equal(t, tt.wantStatus, rec.Code, "body: %s", rec.Body.String())

			assert.ElementsMatch(t, tt.wantToStore, gotToStore,
				"only members that passed validation may reach the store")
			assert.ElementsMatch(t, tt.wantLookedUp, gotLookedUp,
				"only members without a subject row need validating")

			if tt.wantStatus != http.StatusOK {
				return
			}

			var resp membersResults
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

			var rejected []string
			for _, r := range resp.Results {
				if !r.Success {
					rejected = append(rejected, r.SubjectID)
					assert.NotEmpty(t, r.Error, "a rejected member must carry a reason")
				}
			}
			assert.ElementsMatch(t, tt.wantRejected, rejected)
		})
	}
}

// TestRemoveMembersSkipsValidation covers the asymmetry: a user who has left the
// identity provider, or who never existed there, must still be removable. The
// directory is still consulted afterwards to render display names, so this
// asserts on what reaches the store rather than on whether a lookup happened.
func TestRemoveMembersSkipsValidation(t *testing.T) {
	var gotToStore []string
	s := &mockStore{
		existingSubjectsFn: func(context.Context, []string) ([]string, error) {
			return nil, nil
		},
		removeMembersFn: func(_ context.Context, _ string, ids []string) ([]model.MemberChange, error) {
			gotToStore = ids
			return changesFor(ids), nil
		},
	}
	app := newTestApp(s)
	app.userinfo = &mockUserInfo{
		getManyFn: func(context.Context, []string) ([]model.Subject, error) {
			return nil, nil
		},
	}

	body, err := json.Marshal(membersRequest{Members: []string{"ghost"}})
	require.NoError(t, err)

	rec := doRequest(app, http.MethodPost, "/groups/g1/members/deleter", string(body))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"ghost"}, gotToStore,
		"removal must not require the directory to know the user")
}

// intersect returns the members of ids that also appear in allowed.
func intersect(ids, allowed []string) []string {
	set := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		set[a] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// subjectsFor builds directory results for the given usernames.
func subjectsFor(usernames []string) []model.Subject {
	subjects := make([]model.Subject, 0, len(usernames))
	for _, u := range usernames {
		subjects = append(subjects, model.Subject{ID: u, Name: u, SourceID: model.SourceUser})
	}
	return subjects
}
