package keycloak

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// ListUsersOptions controls filtering and pagination for [Client.ListUsers].
// All fields are optional; the zero value lists every user (subject to the
// server's default page size).
//
// Pointer fields distinguish "not set" from a meaningful zero value. Use
// [Bool] and [Int] to populate them concisely.
type ListUsersOptions struct {
	// BriefRepresentation, when true, asks Keycloak to omit attributes,
	// roles and other expensive fields from each returned user.
	BriefRepresentation *bool

	// CreatedAfter and CreatedBefore filter by creation time. Either an
	// ISO-8601 date (yyyy-MM-dd) or epoch milliseconds is accepted.
	CreatedAfter  string
	CreatedBefore string

	// Email filters by email. By default the server matches substrings;
	// set Exact to require an exact match.
	Email string

	// EmailVerified filters by email-verification status.
	EmailVerified *bool

	// Enabled filters by enabled/disabled state.
	Enabled *bool

	// Exact toggles whether Username, Email, FirstName and LastName
	// require an exact match instead of substring matching.
	Exact *bool

	// First is the pagination offset (0-based).
	First *int

	// FirstName, LastName and Username are filters on the corresponding
	// user fields. Substring by default; see Exact.
	FirstName string
	LastName  string
	Username  string

	// IDPAlias and IDPUserID filter on a linked identity provider.
	IDPAlias  string
	IDPUserID string

	// Max is the maximum number of users to return. The server defaults
	// to 100 when this is unset.
	Max *int

	// Q is a custom-attribute query in the form "key1:value1 key2:value2".
	Q string

	// Search matches a substring in username, first/last name or email.
	// Defaults to a prefix match; wrap in '*' for an infix match or in
	// double quotes for an exact match.
	Search string
}

func (o *ListUsersOptions) values() url.Values {
	v := url.Values{}
	if o == nil {
		return v
	}
	setBool := func(key string, val *bool) {
		if val != nil {
			v.Set(key, strconv.FormatBool(*val))
		}
	}
	setInt := func(key string, val *int) {
		if val != nil {
			v.Set(key, strconv.Itoa(*val))
		}
	}
	setStr := func(key, val string) {
		if val != "" {
			v.Set(key, val)
		}
	}
	setBool("briefRepresentation", o.BriefRepresentation)
	setStr("createdAfter", o.CreatedAfter)
	setStr("createdBefore", o.CreatedBefore)
	setStr("email", o.Email)
	setBool("emailVerified", o.EmailVerified)
	setBool("enabled", o.Enabled)
	setBool("exact", o.Exact)
	setInt("first", o.First)
	setStr("firstName", o.FirstName)
	setStr("idpAlias", o.IDPAlias)
	setStr("idpUserId", o.IDPUserID)
	setStr("lastName", o.LastName)
	setInt("max", o.Max)
	setStr("q", o.Q)
	setStr("search", o.Search)
	setStr("username", o.Username)
	return v
}

// ListUsers calls GET /admin/realms/{realm}/users.
func (c *client) ListUsers(ctx context.Context, opts *ListUsersOptions) ([]UserRepresentation, error) {
	path := fmt.Sprintf("/admin/realms/%s/users", url.PathEscape(c.realm))
	var users []UserRepresentation
	if err := c.doJSON(ctx, http.MethodGet, path, opts.values(), nil, &users); err != nil {
		return nil, err
	}
	return users, nil
}
