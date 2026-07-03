package keycloak

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nerzal/gocloak/v13"
)

// attribute keys used to store group metadata that Keycloak groups don't model
// natively.
const (
	attrDescription      = "description"
	attrDisplayExtension = "display_extension"
	attrInstitution      = "o"
)

// Config holds the settings needed to connect to a Keycloak realm as a
// confidential client using the client-credentials grant.
type Config struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
	// ParentGroup, when set, is the name of a top-level group under which all
	// managed groups are created. Empty means groups are created at the realm's
	// top level.
	ParentGroup string
}

// goCloakClient is the gocloak-backed implementation of Client.
type goCloakClient struct {
	gc  *gocloak.GoCloak
	cfg Config

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time

	// parentGroupID is the resolved UUID of cfg.ParentGroup, cached after first
	// lookup. Empty when no parent group is configured.
	parentGroupID string
}

// NewClient constructs a Keycloak Client backed by gocloak.
func NewClient(cfg Config) Client {
	return &goCloakClient{
		gc:  gocloak.NewClient(cfg.BaseURL),
		cfg: cfg,
	}
}

// token returns a valid service-account access token, logging in or reusing a
// cached token as needed.
func (c *goCloakClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Reuse the cached token while it has at least 30s of life left.
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-30*time.Second)) {
		return c.accessToken, nil
	}

	jwt, err := c.gc.LoginClient(ctx, c.cfg.ClientID, c.cfg.ClientSecret, c.cfg.Realm)
	if err != nil {
		return "", fmt.Errorf("keycloak: client login failed: %w", err)
	}

	c.accessToken = jwt.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(jwt.ExpiresIn) * time.Second)
	return c.accessToken, nil
}

// wrapErr translates a gocloak API error into ErrNotFound where appropriate so
// callers can map missing entities to 404s.
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

func (c *goCloakClient) Ping(ctx context.Context) error {
	_, err := c.token(ctx)
	return err
}

// --- groups ---

func (c *goCloakClient) SearchGroups(ctx context.Context, search string) ([]Group, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	params := gocloak.GetGroupsParams{BriefRepresentation: gocloak.BoolP(false)}
	if search != "" {
		params.Search = gocloak.StringP(search)
	}

	kgroups, err := c.gc.GetGroups(ctx, token, c.cfg.Realm, params)
	if err != nil {
		return nil, wrapErr(err)
	}

	groups := make([]Group, 0, len(kgroups))
	for _, kg := range kgroups {
		groups = append(groups, toGroup(kg))
	}
	return groups, nil
}

func (c *goCloakClient) GetGroup(ctx context.Context, id string) (*Group, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	kg, err := c.gc.GetGroup(ctx, token, c.cfg.Realm, id)
	if err != nil {
		return nil, wrapErr(err)
	}
	g := toGroup(kg)
	return &g, nil
}

func (c *goCloakClient) CreateGroup(ctx context.Context, spec GroupSpec) (*Group, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	kg := gocloak.Group{
		Name:       gocloak.StringP(spec.Name),
		Attributes: specAttributes(spec),
	}

	var id string
	if c.cfg.ParentGroup != "" {
		parentID, perr := c.resolveParentGroupID(ctx, token)
		if perr != nil {
			return nil, perr
		}
		id, err = c.gc.CreateChildGroup(ctx, token, c.cfg.Realm, parentID, kg)
	} else {
		id, err = c.gc.CreateGroup(ctx, token, c.cfg.Realm, kg)
	}
	if err != nil {
		return nil, wrapErr(err)
	}

	return c.GetGroup(ctx, id)
}

func (c *goCloakClient) UpdateGroup(ctx context.Context, id string, spec GroupSpec) (*Group, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	kg := gocloak.Group{
		ID:         gocloak.StringP(id),
		Name:       gocloak.StringP(spec.Name),
		Attributes: specAttributes(spec),
	}
	if err := c.gc.UpdateGroup(ctx, token, c.cfg.Realm, kg); err != nil {
		return nil, wrapErr(err)
	}

	return c.GetGroup(ctx, id)
}

func (c *goCloakClient) DeleteGroup(ctx context.Context, id string) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	return wrapErr(c.gc.DeleteGroup(ctx, token, c.cfg.Realm, id))
}

// --- membership ---

func (c *goCloakClient) GroupMembers(ctx context.Context, id string) ([]Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	users, err := c.gc.GetGroupMembers(ctx, token, c.cfg.Realm, id, gocloak.GetGroupsParams{})
	if err != nil {
		return nil, wrapErr(err)
	}

	subjects := make([]Subject, 0, len(users))
	for _, u := range users {
		subjects = append(subjects, toSubject(u))
	}
	return subjects, nil
}

func (c *goCloakClient) AddMember(ctx context.Context, groupID, username string) (Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return Subject{}, err
	}
	// lookupUserByUsername already fetches the full user, so the resolved subject
	// (source ID, name, etc.) is returned to the caller without an extra Keycloak call.
	u, err := c.lookupUserByUsername(ctx, token, username)
	if err != nil {
		return Subject{}, err
	}
	if err := c.gc.AddUserToGroup(ctx, token, c.cfg.Realm, gocloak.PString(u.ID), groupID); err != nil {
		return Subject{}, wrapErr(err)
	}
	return toSubject(u), nil
}

func (c *goCloakClient) RemoveMember(ctx context.Context, groupID, username string) (Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return Subject{}, err
	}
	u, err := c.lookupUserByUsername(ctx, token, username)
	if err != nil {
		return Subject{}, err
	}
	if err := c.gc.DeleteUserFromGroup(ctx, token, c.cfg.Realm, gocloak.PString(u.ID), groupID); err != nil {
		return Subject{}, wrapErr(err)
	}
	return toSubject(u), nil
}

// --- subjects ---

func (c *goCloakClient) SearchSubjects(ctx context.Context, search string) ([]Subject, error) {
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

	subjects := make([]Subject, 0, len(users))
	for _, u := range users {
		subjects = append(subjects, toSubject(u))
	}
	return subjects, nil
}

func (c *goCloakClient) GetSubject(ctx context.Context, username string) (*Subject, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	u, err := c.lookupUserByUsername(ctx, token, username)
	if err != nil {
		return nil, err
	}
	s := toSubject(u)
	return &s, nil
}

func (c *goCloakClient) SubjectGroups(ctx context.Context, username string) ([]Group, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := c.resolveUserID(ctx, token, username)
	if err != nil {
		return nil, err
	}

	kgroups, err := c.gc.GetUserGroups(ctx, token, c.cfg.Realm, userID, gocloak.GetGroupsParams{})
	if err != nil {
		return nil, wrapErr(err)
	}

	groups := make([]Group, 0, len(kgroups))
	for _, kg := range kgroups {
		groups = append(groups, toGroup(kg))
	}
	return groups, nil
}

// --- helpers ---

// lookupUserByUsername returns the Keycloak user with an exact username match,
// or ErrNotFound.
func (c *goCloakClient) lookupUserByUsername(ctx context.Context, token, username string) (*gocloak.User, error) {
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

func (c *goCloakClient) resolveUserID(ctx context.Context, token, username string) (string, error) {
	u, err := c.lookupUserByUsername(ctx, token, username)
	if err != nil {
		return "", err
	}
	return gocloak.PString(u.ID), nil
}

// resolveParentGroupID looks up (and caches) the UUID of the configured parent
// group by exact name among the realm's top-level groups.
func (c *goCloakClient) resolveParentGroupID(ctx context.Context, token string) (string, error) {
	c.mu.Lock()
	if c.parentGroupID != "" {
		id := c.parentGroupID
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	params := gocloak.GetGroupsParams{
		Search: gocloak.StringP(c.cfg.ParentGroup),
		Exact:  gocloak.BoolP(true),
	}
	kgroups, err := c.gc.GetGroups(ctx, token, c.cfg.Realm, params)
	if err != nil {
		return "", wrapErr(err)
	}
	for _, kg := range kgroups {
		if gocloak.PString(kg.Name) == c.cfg.ParentGroup {
			id := gocloak.PString(kg.ID)
			c.mu.Lock()
			c.parentGroupID = id
			c.mu.Unlock()
			return id, nil
		}
	}
	return "", fmt.Errorf("keycloak: configured parent group %q not found", c.cfg.ParentGroup)
}

// specAttributes builds the Keycloak attribute map for the non-native group
// fields, omitting empty values. Returns nil when there is nothing to store.
func specAttributes(spec GroupSpec) *map[string][]string {
	attrs := map[string][]string{}
	if spec.Description != "" {
		attrs[attrDescription] = []string{spec.Description}
	}
	if spec.DisplayExtension != "" {
		attrs[attrDisplayExtension] = []string{spec.DisplayExtension}
	}
	if len(attrs) == 0 {
		return nil
	}
	return &attrs
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

func toGroup(kg *gocloak.Group) Group {
	if kg == nil {
		return Group{}
	}
	return Group{
		ID:               gocloak.PString(kg.ID),
		Name:             gocloak.PString(kg.Name),
		Description:      firstAttr(kg.Attributes, attrDescription),
		DisplayExtension: firstAttr(kg.Attributes, attrDisplayExtension),
	}
}

func toSubject(u *gocloak.User) Subject {
	if u == nil {
		return Subject{}
	}
	username := gocloak.PString(u.Username)
	return Subject{
		ID:          username,
		Name:        username,
		FirstName:   gocloak.PString(u.FirstName),
		LastName:    gocloak.PString(u.LastName),
		Email:       gocloak.PString(u.Email),
		Institution: firstAttr(u.Attributes, attrInstitution),
		SourceID:    gocloak.PString(u.FederationLink),
	}
}
