// Package keycloak is a thin client for the subset of the Keycloak admin
// REST API used by CyVerse. New endpoints are added as they are needed
// rather than mirroring the full surface of the upstream API.
//
// All exported methods are gathered behind the [Client] interface so that
// consumers can mock the client in tests (for example with GoMock):
//
//	//go:generate mockgen -destination=mocks/keycloak.go -package=mocks github.com/cyverse-de/groups/keycloak Client
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Client is the interface implemented by a Keycloak admin API client. It is
// safe for concurrent use.
type Client interface {
	// ListUsers returns users in the configured realm matching the given
	// filter options. Pass nil to list users without any filtering.
	ListUsers(ctx context.Context, opts *ListUsersOptions) ([]UserRepresentation, error)
}

// New constructs a [Client] from the supplied configuration.
func New(cfg Config) (Client, error) {
	switch {
	case cfg.BaseURL == "":
		return nil, errors.New("keycloak: Config.BaseURL is required")
	case cfg.Realm == "":
		return nil, errors.New("keycloak: Config.Realm is required")
	case cfg.ClientID == "":
		return nil, errors.New("keycloak: Config.ClientID is required")
	case cfg.ClientSecret == "":
		return nil, errors.New("keycloak: Config.ClientSecret is required")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &client{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		realm:        cfg.Realm,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		httpClient:   httpClient,
	}, nil
}

// client is the concrete implementation of [Client].
type client struct {
	baseURL      string
	realm        string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	tokenMu     sync.Mutex
	cachedToken cachedToken
}

// APIError is returned when Keycloak responds with a non-2xx status. The
// Body is included verbatim so callers can inspect Keycloak's own error
// payload.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("keycloak: %s %s: status %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// doJSON performs a JSON request against the admin API at the given path.
// The path is relative to BaseURL (it should start with a slash). If body
// is non-nil it is marshalled as JSON and sent as the request body. If out
// is non-nil the response body is decoded into it.
func (c *client) doJSON(ctx context.Context, method, path string, query url.Values, body, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("keycloak: encoding %s %s body: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &APIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       string(b),
		}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("keycloak: decoding %s %s response: %w", method, path, err)
	}
	return nil
}
