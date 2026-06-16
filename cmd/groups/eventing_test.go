package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateGroupPublishesEvent(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		createGroupFn: func(_ context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error) {
			return &keycloak.Group{ID: "g-new", Name: spec.Name}, nil
		},
	})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPost, "/groups", `{"name":"team-a"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{"g-new"}, rp.ids())
}

func TestAddMemberPublishesEvent(t *testing.T) {
	app := newTestApp(&mockKeycloak{})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPut, "/groups/g1/members/alice", "")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{"g1"}, rp.ids())
}

func TestFailedCreateDoesNotPublish(t *testing.T) {
	app := newTestApp(&mockKeycloak{
		createGroupFn: func(_ context.Context, _ keycloak.GroupSpec) (*keycloak.Group, error) {
			return nil, assert.AnError
		},
	})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPost, "/groups", `{"name":"team-a"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	assert.Empty(t, rp.ids())
}
