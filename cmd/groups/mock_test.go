package main

import (
	"context"
	"sync"

	"github.com/cyverse-de/groups/eventing"
	"github.com/cyverse-de/groups/keycloak"
	"github.com/cyverse-de/groups/permissions"
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

// mockPermissions is a configurable stub implementation of permissions.Client.
type mockPermissions struct {
	ensureResourceTypeFn func(ctx context.Context, name, description string) error
	grantFn              func(ctx context.Context, resourceType, resourceName, subjectType, subjectID, level string) error
	revokeFn             func(ctx context.Context, resourceType, resourceName, subjectType, subjectID string) error
	checkFn              func(ctx context.Context, subjectType, subjectID, resourceType, resourceName, minLevel string, lookup bool) (bool, error)
	listResourceFn       func(ctx context.Context, resourceType, resourceName string) ([]permissions.Permission, error)
	deleteResourceFn     func(ctx context.Context, resourceType, resourceName string) error
}

var _ permissions.Client = (*mockPermissions)(nil)

func (m *mockPermissions) EnsureResourceType(ctx context.Context, name, description string) error {
	if m.ensureResourceTypeFn != nil {
		return m.ensureResourceTypeFn(ctx, name, description)
	}
	return nil
}

func (m *mockPermissions) Grant(ctx context.Context, resourceType, resourceName, subjectType, subjectID, level string) error {
	if m.grantFn != nil {
		return m.grantFn(ctx, resourceType, resourceName, subjectType, subjectID, level)
	}
	return nil
}

func (m *mockPermissions) Revoke(ctx context.Context, resourceType, resourceName, subjectType, subjectID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, resourceType, resourceName, subjectType, subjectID)
	}
	return nil
}

func (m *mockPermissions) Check(ctx context.Context, subjectType, subjectID, resourceType, resourceName, minLevel string, lookup bool) (bool, error) {
	if m.checkFn != nil {
		return m.checkFn(ctx, subjectType, subjectID, resourceType, resourceName, minLevel, lookup)
	}
	return false, nil
}

func (m *mockPermissions) ListResource(ctx context.Context, resourceType, resourceName string) ([]permissions.Permission, error) {
	if m.listResourceFn != nil {
		return m.listResourceFn(ctx, resourceType, resourceName)
	}
	return nil, nil
}

func (m *mockPermissions) DeleteResource(ctx context.Context, resourceType, resourceName string) error {
	if m.deleteResourceFn != nil {
		return m.deleteResourceFn(ctx, resourceType, resourceName)
	}
	return nil
}

// allowAllPermissions returns a mock permissions client that authorizes every
// check, for tests that aren't exercising authorization itself.
func allowAllPermissions() *mockPermissions {
	return &mockPermissions{
		checkFn: func(context.Context, string, string, string, string, string, bool) (bool, error) {
			return true, nil
		},
	}
}

// recordingPublisher records the IDs of groups it was asked to publish changes
// for, so tests can assert that events were emitted.
type recordingPublisher struct {
	mu      sync.Mutex
	changed []string
}

func (p *recordingPublisher) GroupChanged(_ context.Context, groupID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.changed = append(p.changed, groupID)
	return nil
}

func (p *recordingPublisher) Close() error { return nil }

func (p *recordingPublisher) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.changed...)
}

// newTestApp builds an App backed by the given Keycloak mock and an allow-all
// permissions mock, with all routes registered.
func newTestApp(kc keycloak.Client) *App {
	return newTestAppWith(kc, allowAllPermissions())
}

// newTestAppWith builds an App backed by the given mock clients and a no-op
// event publisher.
func newTestAppWith(kc keycloak.Client, perms permissions.Client) *App {
	app := &App{
		router:      echo.New(),
		keycloak:    kc,
		permissions: perms,
		events:      eventing.NoopPublisher{},
	}
	app.registerRoutes()
	return app
}
