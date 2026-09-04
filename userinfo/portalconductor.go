package userinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cyverse-de/groups/model"
)

// PortalConductorConfig holds the settings needed to reach portal-conductor,
// which reads the same directory Keycloak federates from.
type PortalConductorConfig struct {
	BaseURL  string
	Username string
	Password string
}

// portalConductorClient reads user attributes through portal-conductor's LDAP
// API. Where Keycloak has no bulk lookup by username -- one HTTP call per user,
// so listing a large group costs one call per member -- portal-conductor answers
// a whole listing in one request. It also reports the institution, which reaches
// Keycloak only if the realm has an LDAP mapper for the `o` attribute.
type portalConductorClient struct {
	baseURL string
	cfg     PortalConductorConfig
	http    *http.Client
}

// NewPortalConductorClient returns a Client backed by portal-conductor.
func NewPortalConductorClient(cfg PortalConductorConfig) Client {
	return &portalConductorClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		cfg:     cfg,
		http:    &http.Client{Timeout: DefaultRequestTimeout},
	}
}

// userLDAPInfo is the subset of portal-conductor's LDAP user record this service
// reports. The fields it omits describe the POSIX account rather than the
// person. Every value is a pointer there, so a missing attribute is null rather
// than empty.
type userLDAPInfo struct {
	Username     string  `json:"username"`
	GivenName    *string `json:"given_name"`
	Surname      *string `json:"surname"`
	Email        *string `json:"email"`
	Organization *string `json:"organization"`
}

// toSubject converts a portal-conductor user record to a subject. The name is
// the username rather than the common name, matching what the Keycloak-backed
// client reports and what Grouper reported before it.
func (u userLDAPInfo) toSubject() model.Subject {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return model.Subject{
		ID:          u.Username,
		Name:        u.Username,
		FirstName:   deref(u.GivenName),
		LastName:    deref(u.Surname),
		Email:       deref(u.Email),
		Institution: deref(u.Organization),
		SourceID:    model.SourceUser,
	}
}

type userSearchResponse struct {
	Users []userLDAPInfo `json:"users"`
}

// Ping verifies that portal-conductor accepts this service's credentials and can
// reach the directory. A lookup of one name exercises both; an empty list would
// be answered without a directory query at all.
func (c *portalConductorClient) Ping(ctx context.Context) error {
	var out userSearchResponse
	return c.do(ctx, http.MethodPost, "/ldap/users/search",
		map[string][]string{"usernames": {"groups-service-ping"}}, &out)
}

func (c *portalConductorClient) Get(ctx context.Context, username string) (*model.Subject, error) {
	var out userLDAPInfo
	path := "/ldap/users/" + url.PathEscape(username)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	subject := out.toSubject()
	return &subject, nil
}

func (c *portalConductorClient) GetMany(ctx context.Context, usernames []string) ([]model.Subject, error) {
	if len(usernames) == 0 {
		return []model.Subject{}, nil
	}

	var out userSearchResponse
	body := map[string][]string{"usernames": usernames}
	if err := c.do(ctx, http.MethodPost, "/ldap/users/search", body, &out); err != nil {
		return nil, err
	}
	return subjectsOf(out.Users), nil
}

func (c *portalConductorClient) Search(ctx context.Context, search string) ([]model.Subject, error) {
	var out userSearchResponse
	path := "/ldap/users?search=" + url.QueryEscape(search)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return subjectsOf(out.Users), nil
}

func subjectsOf(users []userLDAPInfo) []model.Subject {
	subjects := make([]model.Subject, 0, len(users))
	for _, u := range users {
		subjects = append(subjects, u.toSubject())
	}
	return subjects
}

// do performs an authenticated request against portal-conductor, JSON-encoding
// the body (if any) and decoding the response into out. A 404 is reported as
// ErrNotFound.
func (c *portalConductorClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is portal-conductor's own error detail, not a directory
		// record, so it is safe to carry into the message.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("portal-conductor: %s %s returned %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
