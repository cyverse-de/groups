package main

import (
	"context"
	"sync"

	"github.com/cyverse-de/groups/eventing"
	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/permissions"
	"github.com/cyverse-de/groups/store"
	"github.com/cyverse-de/groups/userinfo"
	"github.com/labstack/echo/v4"
)

// mockStore is a configurable stub implementing both store.Store and store.Tx:
// handler tests exercise routing and authorization, and the real transaction
// semantics are covered by the pgstore integration tests. Each method delegates
// to its function field when set and otherwise returns a benign default.
type mockStore struct {
	getGroupFn          func(ctx context.Context, id string) (*model.Group, error)
	lookupGroupFn       func(ctx context.Context, ref model.Ref) (*model.Group, error)
	listGroupsFn        func(ctx context.Context, f store.ListFilter) ([]model.Group, error)
	listMembersFn       func(ctx context.Context, groupID string) ([]model.MemberRef, error)
	isEffectiveMemberFn func(ctx context.Context, groupID, username string) (bool, error)
	groupsForSubjectFn  func(ctx context.Context, username, groupType string) ([]model.Group, error)

	createGroupFn    func(ctx context.Context, spec model.GroupSpec) (*model.Group, error)
	updateGroupFn    func(ctx context.Context, id string, upd model.GroupUpdate) (*model.Group, error)
	deleteGroupFn    func(ctx context.Context, id string) error
	addMembersFn     func(ctx context.Context, groupID string, ids []string, addedBy string) ([]model.MemberChange, error)
	removeMembersFn  func(ctx context.Context, groupID string, ids []string) ([]model.MemberChange, error)
	replaceMembersFn func(ctx context.Context, groupID string, ids []string, addedBy string) ([]model.MemberChange, error)

	pingFn func(ctx context.Context) error

	// committed records whether the most recent WithTx reached its commit, so a
	// test can assert that a failure inside the transaction rolled it back.
	committed bool
}

var (
	_ store.Store = (*mockStore)(nil)
	_ store.Tx    = (*mockStore)(nil)
)

func (m *mockStore) WithTx(ctx context.Context, fn func(store.Tx) error) error {
	m.committed = false
	if err := fn(m); err != nil {
		return err
	}
	m.committed = true
	return nil
}

func (m *mockStore) Ping(ctx context.Context) error {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func (m *mockStore) Close() error { return nil }

func (m *mockStore) GetGroup(ctx context.Context, id string) (*model.Group, error) {
	if m.getGroupFn != nil {
		return m.getGroupFn(ctx, id)
	}
	return &model.Group{ID: id, GroupType: model.GroupTypeTeam, Name: id}, nil
}

func (m *mockStore) LookupGroup(ctx context.Context, ref model.Ref) (*model.Group, error) {
	if m.lookupGroupFn != nil {
		return m.lookupGroupFn(ctx, ref)
	}
	return &model.Group{ID: "g1", GroupType: ref.GroupType, Owner: ref.Owner, Name: ref.Name}, nil
}

func (m *mockStore) ListGroups(ctx context.Context, f store.ListFilter) ([]model.Group, error) {
	if m.listGroupsFn != nil {
		return m.listGroupsFn(ctx, f)
	}
	return []model.Group{}, nil
}

func (m *mockStore) ListMembers(ctx context.Context, groupID string) ([]model.MemberRef, error) {
	if m.listMembersFn != nil {
		return m.listMembersFn(ctx, groupID)
	}
	return []model.MemberRef{}, nil
}

func (m *mockStore) IsEffectiveMember(ctx context.Context, groupID, username string) (bool, error) {
	if m.isEffectiveMemberFn != nil {
		return m.isEffectiveMemberFn(ctx, groupID, username)
	}
	return false, nil
}

func (m *mockStore) GroupsForSubject(ctx context.Context, username, groupType string) ([]model.Group, error) {
	if m.groupsForSubjectFn != nil {
		return m.groupsForSubjectFn(ctx, username, groupType)
	}
	return []model.Group{}, nil
}

func (m *mockStore) CreateGroup(ctx context.Context, spec model.GroupSpec) (*model.Group, error) {
	if m.createGroupFn != nil {
		return m.createGroupFn(ctx, spec)
	}
	return &model.Group{
		ID: "g-new", GroupType: spec.GroupType, Owner: spec.Owner,
		Name: spec.Name, Description: spec.Description,
	}, nil
}

func (m *mockStore) UpdateGroup(ctx context.Context, id string, upd model.GroupUpdate) (*model.Group, error) {
	if m.updateGroupFn != nil {
		return m.updateGroupFn(ctx, id, upd)
	}
	g := &model.Group{ID: id, GroupType: model.GroupTypeTeam, Name: id}
	if upd.Name != nil {
		g.Name = *upd.Name
	}
	return g, nil
}

func (m *mockStore) DeleteGroup(ctx context.Context, id string) error {
	if m.deleteGroupFn != nil {
		return m.deleteGroupFn(ctx, id)
	}
	return nil
}

func (m *mockStore) AddMembers(ctx context.Context, groupID string, ids []string, addedBy string) ([]model.MemberChange, error) {
	if m.addMembersFn != nil {
		return m.addMembersFn(ctx, groupID, ids, addedBy)
	}
	return changesFor(ids), nil
}

func (m *mockStore) RemoveMembers(ctx context.Context, groupID string, ids []string) ([]model.MemberChange, error) {
	if m.removeMembersFn != nil {
		return m.removeMembersFn(ctx, groupID, ids)
	}
	return changesFor(ids), nil
}

func (m *mockStore) ReplaceMembers(ctx context.Context, groupID string, ids []string, addedBy string) ([]model.MemberChange, error) {
	if m.replaceMembersFn != nil {
		return m.replaceMembersFn(ctx, groupID, ids, addedBy)
	}
	return changesFor(ids), nil
}

// changesFor reports every id as a successful user membership change.
func changesFor(ids []string) []model.MemberChange {
	changes := make([]model.MemberChange, 0, len(ids))
	for _, id := range ids {
		changes = append(changes, model.MemberChange{SubjectID: id, Type: model.MemberTypeUser})
	}
	return changes
}

// mockUserInfo is a configurable stub implementation of userinfo.Client.
type mockUserInfo struct {
	pingFn    func(ctx context.Context) error
	getFn     func(ctx context.Context, username string) (*model.Subject, error)
	getManyFn func(ctx context.Context, usernames []string) ([]model.Subject, error)
	searchFn  func(ctx context.Context, search string) ([]model.Subject, error)
}

var _ userinfo.Client = (*mockUserInfo)(nil)

func (m *mockUserInfo) Ping(ctx context.Context) error {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func (m *mockUserInfo) Get(ctx context.Context, username string) (*model.Subject, error) {
	if m.getFn != nil {
		return m.getFn(ctx, username)
	}
	return &model.Subject{ID: username, Name: username, SourceID: model.SourceUser}, nil
}

func (m *mockUserInfo) GetMany(ctx context.Context, usernames []string) ([]model.Subject, error) {
	if m.getManyFn != nil {
		return m.getManyFn(ctx, usernames)
	}
	subjects := make([]model.Subject, 0, len(usernames))
	for _, u := range usernames {
		subjects = append(subjects, model.Subject{ID: u, Name: u, SourceID: model.SourceUser})
	}
	return subjects, nil
}

func (m *mockUserInfo) Search(ctx context.Context, search string) ([]model.Subject, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, search)
	}
	return []model.Subject{}, nil
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

// denyAllPermissions returns a mock permissions client that denies every check,
// so a request that still succeeds did so through membership or the admin bypass.
func denyAllPermissions() *mockPermissions {
	return &mockPermissions{
		checkFn: func(context.Context, string, string, string, string, string, bool) (bool, error) {
			return false, nil
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

// newTestApp builds an App backed by the given store and an allow-all
// permissions mock, with all routes registered.
func newTestApp(s store.Store) *App {
	return newTestAppWith(s, allowAllPermissions())
}

// newTestAppWith builds an App backed by the given store and permissions client,
// with a stub user directory and a no-op event publisher.
func newTestAppWith(s store.Store, perms permissions.Client) *App {
	app := &App{
		router:      echo.New(),
		store:       s,
		userinfo:    &mockUserInfo{},
		permissions: perms,
		events:      eventing.NoopPublisher{},
	}
	app.registerRoutes()
	return app
}
