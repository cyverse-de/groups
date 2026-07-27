// Package userinfo looks up the user attributes the groups service reports:
// name, email, and institution. Users stay in the identity provider even though
// groups and membership live in the DE database, so a directory outage degrades
// display data rather than authorization -- except when adding a member with no
// subject row, where this package also verifies that the username exists.
package userinfo

import (
	"context"
	"errors"

	"github.com/cyverse-de/groups/model"
)

// ErrNotFound reports that no user matches the given username.
var ErrNotFound = errors.New("userinfo: not found")

// Client reads user attributes from the identity provider.
type Client interface {
	// Ping verifies connectivity and that the service account can authenticate.
	Ping(ctx context.Context) error

	// Get returns one user by username, or ErrNotFound.
	Get(ctx context.Context, username string) (*model.Subject, error)

	// GetMany returns the users matching the given usernames. Usernames that do
	// not resolve are omitted rather than reported, matching iplant-groups.
	GetMany(ctx context.Context, usernames []string) ([]model.Subject, error)

	// Search returns the users matching a search term across username, name, and
	// email.
	Search(ctx context.Context, search string) ([]model.Subject, error)
}
