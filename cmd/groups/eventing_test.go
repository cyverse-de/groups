package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateGroupPublishesEvent(t *testing.T) {
	app := newTestApp(&mockStore{})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPost, "/groups", `{"group_type":"team","name":"Ecology"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{"g-new"}, rp.ids())
}

func TestAddMemberPublishesEvent(t *testing.T) {
	app := newTestApp(&mockStore{})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPut, "/groups/g1/members/alice", "")
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{"g1"}, rp.ids())
}

func TestFailedCreateDoesNotPublish(t *testing.T) {
	app := newTestApp(&mockStore{
		createGroupFn: func(context.Context, model.GroupSpec) (*model.Group, error) {
			return nil, assert.AnError
		},
	})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPost, "/groups", `{"group_type":"team","name":"Ecology"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	assert.Empty(t, rp.ids())
}

// The event announces a committed change, so a transaction that rolled back
// must not have published one.
func TestRolledBackMembershipDoesNotPublish(t *testing.T) {
	app := newTestApp(&mockStore{
		addMembersFn: func(context.Context, string, []string, string) ([]model.MemberChange, error) {
			return nil, assert.AnError
		},
	})
	rp := &recordingPublisher{}
	app.events = rp

	rec := doRequest(app, http.MethodPost, "/groups/g1/members", `{"members":["alice"]}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	assert.Empty(t, rp.ids())
}
