package main

import (
	"context"

	"github.com/cyverse-de/groups/keycloak"
	"github.com/labstack/echo/v4"
)

// mockKeycloak is a configurable stub implementation of keycloak.Client. Each
// method delegates to the corresponding function field when set, and otherwise
// returns zero values.
type mockKeycloak struct {
	pingFn           func(ctx context.Context) error
	searchGroupsFn   func(ctx context.Context, search string) ([]keycloak.Group, error)
	getGroupFn       func(ctx context.Context, id string) (*keycloak.Group, error)
	createGroupFn    func(ctx context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error)
	updateGroupFn    func(ctx context.Context, id string, spec keycloak.GroupSpec) (*keycloak.Group, error)
	deleteGroupFn    func(ctx context.Context, id string) error
	groupMembersFn   func(ctx context.Context, id string) ([]keycloak.Subject, error)
	addMemberFn      func(ctx context.Context, groupID, username string) error
	removeMemberFn   func(ctx context.Context, groupID, username string) error
	searchSubjectsFn func(ctx context.Context, search string) ([]keycloak.Subject, error)
	getSubjectFn     func(ctx context.Context, username string) (*keycloak.Subject, error)
	subjectGroupsFn  func(ctx context.Context, username string) ([]keycloak.Group, error)
}

var _ keycloak.Client = (*mockKeycloak)(nil)

func (m *mockKeycloak) Ping(ctx context.Context) error {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func (m *mockKeycloak) SearchGroups(ctx context.Context, search string) ([]keycloak.Group, error) {
	if m.searchGroupsFn != nil {
		return m.searchGroupsFn(ctx, search)
	}
	return nil, nil
}

func (m *mockKeycloak) GetGroup(ctx context.Context, id string) (*keycloak.Group, error) {
	if m.getGroupFn != nil {
		return m.getGroupFn(ctx, id)
	}
	return nil, nil
}

func (m *mockKeycloak) CreateGroup(ctx context.Context, spec keycloak.GroupSpec) (*keycloak.Group, error) {
	if m.createGroupFn != nil {
		return m.createGroupFn(ctx, spec)
	}
	return nil, nil
}

func (m *mockKeycloak) UpdateGroup(ctx context.Context, id string, spec keycloak.GroupSpec) (*keycloak.Group, error) {
	if m.updateGroupFn != nil {
		return m.updateGroupFn(ctx, id, spec)
	}
	return nil, nil
}

func (m *mockKeycloak) DeleteGroup(ctx context.Context, id string) error {
	if m.deleteGroupFn != nil {
		return m.deleteGroupFn(ctx, id)
	}
	return nil
}

func (m *mockKeycloak) GroupMembers(ctx context.Context, id string) ([]keycloak.Subject, error) {
	if m.groupMembersFn != nil {
		return m.groupMembersFn(ctx, id)
	}
	return nil, nil
}

func (m *mockKeycloak) AddMember(ctx context.Context, groupID, username string) error {
	if m.addMemberFn != nil {
		return m.addMemberFn(ctx, groupID, username)
	}
	return nil
}

func (m *mockKeycloak) RemoveMember(ctx context.Context, groupID, username string) error {
	if m.removeMemberFn != nil {
		return m.removeMemberFn(ctx, groupID, username)
	}
	return nil
}

func (m *mockKeycloak) SearchSubjects(ctx context.Context, search string) ([]keycloak.Subject, error) {
	if m.searchSubjectsFn != nil {
		return m.searchSubjectsFn(ctx, search)
	}
	return nil, nil
}

func (m *mockKeycloak) GetSubject(ctx context.Context, username string) (*keycloak.Subject, error) {
	if m.getSubjectFn != nil {
		return m.getSubjectFn(ctx, username)
	}
	return nil, nil
}

func (m *mockKeycloak) SubjectGroups(ctx context.Context, username string) ([]keycloak.Group, error) {
	if m.subjectGroupsFn != nil {
		return m.subjectGroupsFn(ctx, username)
	}
	return nil, nil
}

// newTestApp builds an App backed by the given mock client with all routes
// registered, ready to serve test requests.
func newTestApp(kc keycloak.Client) *App {
	app := &App{
		router:   echo.New(),
		keycloak: kc,
	}
	app.registerRoutes()
	return app
}
