package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetMembers(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		groupMembersFn: func(_ context.Context, id string) ([]keycloak.Subject, error) {
			assert.Equal(t, "g1", id)
			return []keycloak.Subject{{ID: "alice"}, {ID: "bob"}}, nil
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups/g1/members", "")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp membersResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Members, 2)
}

func TestAddMembersBulkCollectsResults(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		addMemberFn: func(_ context.Context, _ string, username string) (keycloak.Subject, error) {
			if username == "bad" {
				return keycloak.Subject{}, errors.New("nope")
			}
			return keycloak.Subject{ID: username, Name: username, SourceID: "ldap"}, nil
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
	assert.Equal(t, "ldap", byID["alice"].SourceID)
	assert.Equal(t, "alice", byID["alice"].SubjectName)
	assert.False(t, byID["bad"].Success)
	assert.Equal(t, "nope", byID["bad"].Error)
	assert.True(t, byID["carol"].Success)
	assert.Equal(t, "ldap", byID["carol"].SourceID)
}

func TestReplaceMembersReconciles(t *testing.T) {
	var (
		mu      sync.Mutex
		added   []string
		removed []string
	)
	app := newTestApp(&mockKeycloak{
		groupMembersFn: func(_ context.Context, _ string) ([]keycloak.Subject, error) {
			return []keycloak.Subject{{ID: "alice"}, {ID: "bob"}}, nil
		},
		addMemberFn: func(_ context.Context, _ string, username string) (keycloak.Subject, error) {
			mu.Lock()
			defer mu.Unlock()
			added = append(added, username)
			return keycloak.Subject{ID: username}, nil
		},
		removeMemberFn: func(_ context.Context, _ string, username string) (keycloak.Subject, error) {
			mu.Lock()
			defer mu.Unlock()
			removed = append(removed, username)
			return keycloak.Subject{ID: username}, nil
		},
	})

	// Desired membership {alice, carol}: keep alice, remove bob, add carol.
	rec := doRequest(app, http.MethodPut, "/groups/g1/members",
		`{"members":["alice","carol"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	sort.Strings(added)
	sort.Strings(removed)
	assert.Equal(t, []string{"carol"}, added)
	assert.Equal(t, []string{"bob"}, removed)
}

func TestRemoveSingleMember(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		removeMemberFn: func(_ context.Context, groupID, username string) (keycloak.Subject, error) {
			assert.Equal(t, "g1", groupID)
			assert.Equal(t, "alice", username)
			return keycloak.Subject{ID: username}, nil
		},
	})

	rec := doRequest(app, http.MethodDelete, "/groups/g1/members/alice", "")
	assert.Equal(t, http.StatusOK, rec.Code)
}
