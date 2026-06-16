// Package permissions provides a client for the DE permissions service, which
// the groups service uses to record and check which subjects may manage which
// groups. Each group is a resource of type "group"; group-management rights are
// expressed as permission levels granted to user subjects.
package permissions

import "context"

// Subject types recognized by the permissions service.
const (
	SubjectTypeUser  = "user"
	SubjectTypeGroup = "group"
)

// Permission levels, ordered from most to least permissive. Note the service's
// precedence ordering places admin between write and read.
const (
	LevelOwn   = "own"
	LevelWrite = "write"
	LevelAdmin = "admin"
	LevelRead  = "read"
)

// Subject identifies a user or group in the permissions service.
type Subject struct {
	ID              string `json:"id,omitempty"`
	SubjectID       string `json:"subject_id"`
	SubjectType     string `json:"subject_type"`
	SubjectSourceID string `json:"subject_source_id,omitempty"`
}

// Resource identifies a permission-controlled resource.
type Resource struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
}

// Permission is a grant of a permission level to a subject on a resource.
type Permission struct {
	ID              string   `json:"id"`
	Subject         Subject  `json:"subject"`
	Resource        Resource `json:"resource"`
	PermissionLevel string   `json:"permission_level"`
}

// Client is the set of permissions-service operations the groups service needs.
type Client interface {
	// EnsureResourceType registers the named resource type if it does not
	// already exist. It is safe to call repeatedly.
	EnsureResourceType(ctx context.Context, name, description string) error

	// Grant gives a subject the given permission level on a resource, creating
	// the resource and subject if necessary.
	Grant(ctx context.Context, resourceType, resourceName, subjectType, subjectID, level string) error

	// Revoke removes a subject's permission on a resource.
	Revoke(ctx context.Context, resourceType, resourceName, subjectType, subjectID string) error

	// Check reports whether the subject holds at least minLevel on the resource.
	// When lookup is true and the subject is a user, permissions inherited via
	// group membership are also considered.
	Check(ctx context.Context, subjectType, subjectID, resourceType, resourceName, minLevel string, lookup bool) (bool, error)

	// ListResource returns all permissions granted on a resource.
	ListResource(ctx context.Context, resourceType, resourceName string) ([]Permission, error)

	// DeleteResource removes a resource and all permissions associated with it.
	DeleteResource(ctx context.Context, resourceType, resourceName string) error
}
