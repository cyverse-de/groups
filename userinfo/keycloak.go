package userinfo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"
	"github.com/cyverse-de/groups/model"
)

// attrInstitution is the Keycloak user attribute holding the user's institution.
const attrInstitution = "o"

// Config holds the settings needed to reach a Keycloak realm as a confidential
// client using the client-credentials grant.
type Config struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
}

// keycloakClient is the gocloak-backed implementation of Client.
type keycloakClient struct {
	gc  *gocloak.GoCloak
	cfg Config

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewKeycloakClient constructs a Client backed by the Keycloak Admin REST API.
func NewKeycloakClient(cfg Config) Client {
	return &keycloakClient{gc: gocloak.NewClient(cfg.BaseURL), cfg: cfg}
}

// token returns a valid service-account access token, reusing the cached one
// while it has at least 30s of life left.
func (c *keycloakClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-30*time.Second)) {
		return c.accessToken, nil
	}

	jwt, err := c.gc.LoginClient(ctx, c.cfg.ClientID, c.cfg.ClientSecret, c.cfg.Realm)
	if err != nil {
		return "", fmt.Errorf("keycloak client login failed; check keycloak.client-id and keycloak.client-secret: %w", err)
	}

	c.accessToken = jwt.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(jwt.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

// wrapErr translates a gocloak API error into ErrNotFound where appropriate.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *gocloak.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 404 {
		return ErrNotFound
	}
	return err
}

func (c *keycloakClient) Ping(ctx context.Context) error {
	_, err := c.token(ctx)
	return err
}

func (c *keycloakClient) Get(ctx context.Context, username string) (*model.Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	u, err := c.lookupByUsername(ctx, token, username)
	if err != nil {
		return nil, err
	}
	s := toSubject(u)
	return &s, nil
}

// GetMany resolves each username in turn, deduplicating first. Keycloak has no
// bulk lookup by username, so this is a loop; it exists as one call so callers
// need not write the loop themselves and so a cache could later be added in one
// place.
func (c *keycloakClient) GetMany(ctx context.Context, usernames []string) ([]model.Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(usernames))
	subjects := make([]model.Subject, 0, len(usernames))
	for _, username := range usernames {
		if _, dup := seen[username]; dup {
			continue
		}
		seen[username] = struct{}{}

		u, err := c.lookupByUsername(ctx, token, username)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		subjects = append(subjects, toSubject(u))
	}
	return subjects, nil
}

func (c *keycloakClient) Search(ctx context.Context, search string) ([]model.Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	params := gocloak.GetUsersParams{}
	if search != "" {
		params.Search = gocloak.StringP(search)
	}

	users, err := c.gc.GetUsers(ctx, token, c.cfg.Realm, params)
	if err != nil {
		return nil, wrapErr(err)
	}

	subjects := make([]model.Subject, 0, len(users))
	for _, u := range users {
		subjects = append(subjects, toSubject(u))
	}
	return subjects, nil
}

// lookupByUsername returns the user with an exact username match, or ErrNotFound.
func (c *keycloakClient) lookupByUsername(ctx context.Context, token, username string) (*gocloak.User, error) {
	params := gocloak.GetUsersParams{
		Username: gocloak.StringP(username),
		Exact:    gocloak.BoolP(true),
	}
	users, err := c.gc.GetUsers(ctx, token, c.cfg.Realm, params)
	if err != nil {
		return nil, wrapErr(err)
	}
	for _, u := range users {
		if strings.EqualFold(gocloak.PString(u.Username), username) {
			return u, nil
		}
	}
	return nil, ErrNotFound
}

// firstAttr returns the first value of the named attribute, or "".
func firstAttr(attrs *map[string][]string, key string) string {
	if attrs == nil {
		return ""
	}
	if vals, ok := (*attrs)[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// toSubject converts a Keycloak user to a subject. SourceID is the constant
// "ldap" rather than Keycloak's federation link: consumers compare it against
// the Grouper values, and the federation link is an opaque provider UUID that
// none of them understand.
func toSubject(u *gocloak.User) model.Subject {
	if u == nil {
		return model.Subject{}
	}
	username := gocloak.PString(u.Username)
	return model.Subject{
		ID:          username,
		Name:        username,
		FirstName:   gocloak.PString(u.FirstName),
		LastName:    gocloak.PString(u.LastName),
		Email:       gocloak.PString(u.Email),
		Institution: firstAttr(u.Attributes, attrInstitution),
		SourceID:    model.SourceUser,
	}
}
