// Package store defines the persistence interface for groups and membership,
// along with the error values callers translate into HTTP status codes.
package store

import (
	"context"
	"errors"

	"github.com/cyverse-de/groups/model"
)

var (
	// ErrNotFound reports that a group does not exist.
	ErrNotFound = errors.New("store: not found")

	// ErrConflict reports that a write would collide with an existing group's
	// identity: the same name under the same type and owner.
	ErrConflict = errors.New("store: conflict")

	// ErrCycle reports that adding a group as a member would make it contain
	// itself, directly or through nesting.
	ErrCycle = errors.New("store: membership cycle")

	// ErrOwnerRequired reports that a group's type disagrees with the presence of
	// an owner: teams and collaborator lists need one, communities and system
	// groups must not have one.
	ErrOwnerRequired = errors.New("store: owner does not match group type")
)

// ListFilter narrows a group listing. Empty fields are not applied. Member is a
// username, and matches groups the user belongs to through any depth of nesting.
type ListFilter struct {
	GroupType string
	Owner     string
	Member    string
	Search    string
	Limit     int
	Offset    int
}

// Reader is the read-only half of the store, available both inside and outside a
// transaction.
type Reader interface {
	// GetGroup looks a group up by its external ID.
	GetGroup(ctx context.Context, id string) (*model.Group, error)

	// LookupGroup looks a group up by its structured identity.
	LookupGroup(ctx context.Context, ref model.Ref) (*model.Group, error)

	// ListGroups returns the groups matching the filter.
	ListGroups(ctx context.Context, filter ListFilter) ([]model.Group, error)

	// ListMembers returns a group's direct members, which may include groups.
	ListMembers(ctx context.Context, groupID string) ([]model.MemberRef, error)

	// IsEffectiveMember reports whether the user belongs to the group through any
	// depth of nesting.
	IsEffectiveMember(ctx context.Context, groupID, username string) (bool, error)

	// GroupsForSubject returns the groups a user belongs to, through any depth of
	// nesting. An empty groupType returns every type.
	GroupsForSubject(ctx context.Context, username, groupType string) ([]model.Group, error)

	// ExistingSubjects returns the subset of the given identifiers that already
	// have a subject row, in unspecified order. Callers use it to find the
	// identifiers that would cause a new subject to be created.
	ExistingSubjects(ctx context.Context, ids []string) ([]string, error)
}

// Tx is a read-write transaction. Every mutation lives here so that a caller
// cannot write outside one.
type Tx interface {
	Reader

	CreateGroup(ctx context.Context, spec model.GroupSpec) (*model.Group, error)
	UpdateGroup(ctx context.Context, id string, upd model.GroupUpdate) (*model.Group, error)
	DeleteGroup(ctx context.Context, id string) error

	// AddMembers adds each subject to the group. A subject ID that names an
	// existing group is added as a nested group; anything else is treated as a
	// username and given a subject row if it lacks one.
	AddMembers(ctx context.Context, groupID string, subjectIDs []string, addedBy string) ([]model.MemberChange, error)

	// RemoveMembers removes each subject from the group. Removing a subject that
	// is not a member succeeds.
	RemoveMembers(ctx context.Context, groupID string, subjectIDs []string) ([]model.MemberChange, error)

	// ReplaceMembers makes the group's membership exactly the given set,
	// reporting only the additions and removals it performed.
	ReplaceMembers(ctx context.Context, groupID string, subjectIDs []string, addedBy string) ([]model.MemberChange, error)
}

// Store is the persistence entry point.
type Store interface {
	Reader

	// WithTx runs fn inside a transaction, committing when it returns nil and
	// rolling back otherwise.
	WithTx(ctx context.Context, fn func(Tx) error) error

	// Ping verifies connectivity.
	Ping(ctx context.Context) error

	// Close releases the underlying resources.
	Close() error
}
