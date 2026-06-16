// Package keycloak provides a thin, mockable wrapper around the Keycloak Admin
// REST API for the operations the groups service needs: group CRUD, group
// membership, and user (subject) lookups.
package keycloak

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a requested Keycloak entity does not exist. It
// lets the HTTP layer translate missing entities into 404 responses without
// depending on the underlying client library.
var ErrNotFound = errors.New("keycloak: not found")

// Group is the groups service's view of a Keycloak group. The canonical
// identifier is the Keycloak group UUID; description and display_extension are
// stored as Keycloak group attributes.
type Group struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	DisplayExtension string `json:"display_extension,omitempty"`
}

// GroupSpec carries the mutable fields used when creating or updating a group.
type GroupSpec struct {
	Name             string `json:"name"`
	Description      string `json:"description,omitempty"`
	DisplayExtension string `json:"display_extension,omitempty"`
}

// Subject is the groups service's view of a Keycloak user. The canonical
// identifier (ID) is the Keycloak username, matching the subject IDs used by
// iplant-groups.
type Subject struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Institution string `json:"institution,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
}

// Client is the set of Keycloak operations the groups service depends on. It is
// an interface so handlers can be tested against a mock implementation.
type Client interface {
	// Ping verifies connectivity and that the service account can authenticate.
	Ping(ctx context.Context) error

	// Groups.
	SearchGroups(ctx context.Context, search string) ([]Group, error)
	GetGroup(ctx context.Context, id string) (*Group, error)
	CreateGroup(ctx context.Context, spec GroupSpec) (*Group, error)
	UpdateGroup(ctx context.Context, id string, spec GroupSpec) (*Group, error)
	DeleteGroup(ctx context.Context, id string) error

	// Membership. Members are identified by username (subject ID).
	GroupMembers(ctx context.Context, id string) ([]Subject, error)
	AddMember(ctx context.Context, groupID, username string) error
	RemoveMember(ctx context.Context, groupID, username string) error

	// Subjects (users).
	SearchSubjects(ctx context.Context, search string) ([]Subject, error)
	GetSubject(ctx context.Context, username string) (*Subject, error)
	SubjectGroups(ctx context.Context, username string) ([]Group, error)
}
