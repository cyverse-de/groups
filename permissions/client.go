package permissions

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
	"time"
)

// ErrNotFound is returned when the permissions service responds with 404.
var ErrNotFound = errors.New("permissions: not found")

// httpClient is the HTTP-backed implementation of Client.
type httpClient struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client that talks to the permissions service at baseURL.
func NewClient(baseURL string) Client {
	return &httpClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *httpClient) EnsureResourceType(ctx context.Context, name, description string) error {
	var out struct {
		ResourceTypes []struct {
			Name string `json:"name"`
		} `json:"resource_types"`
	}
	query := url.Values{"resource_type_name": {name}}
	if err := c.do(ctx, http.MethodGet, "/resource_types", query, nil, &out); err != nil {
		return err
	}
	for _, rt := range out.ResourceTypes {
		if rt.Name == name {
			return nil
		}
	}

	body := map[string]string{"name": name, "description": description}
	return c.do(ctx, http.MethodPost, "/resource_types", nil, body, nil)
}

func (c *httpClient) Grant(ctx context.Context, resourceType, resourceName, subjectType, subjectID, level string) error {
	body := map[string]string{"permission_level": level}
	return c.do(ctx, http.MethodPut, grantPath(resourceType, resourceName, subjectType, subjectID), nil, body, nil)
}

func (c *httpClient) Revoke(ctx context.Context, resourceType, resourceName, subjectType, subjectID string) error {
	return c.do(ctx, http.MethodDelete, grantPath(resourceType, resourceName, subjectType, subjectID), nil, nil, nil)
}

func (c *httpClient) Check(ctx context.Context, subjectType, subjectID, resourceType, resourceName, minLevel string, lookup bool) (bool, error) {
	path := fmt.Sprintf("/permissions/subjects/%s/%s/%s/%s",
		url.PathEscape(subjectType), url.PathEscape(subjectID),
		url.PathEscape(resourceType), url.PathEscape(resourceName))

	query := url.Values{}
	if lookup {
		query.Set("lookup", "true")
	}
	if minLevel != "" {
		query.Set("min_level", minLevel)
	}

	var out struct {
		Permissions []Permission `json:"permissions"`
	}
	if err := c.do(ctx, http.MethodGet, path, query, nil, &out); err != nil {
		return false, err
	}
	return len(out.Permissions) > 0, nil
}

func (c *httpClient) ListResource(ctx context.Context, resourceType, resourceName string) ([]Permission, error) {
	path := fmt.Sprintf("/permissions/resources/%s/%s",
		url.PathEscape(resourceType), url.PathEscape(resourceName))

	var out struct {
		Permissions []Permission `json:"permissions"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &out); err != nil {
		return nil, err
	}
	return out.Permissions, nil
}

func (c *httpClient) DeleteResource(ctx context.Context, resourceType, resourceName string) error {
	query := url.Values{
		"resource_type_name": {resourceType},
		"resource_name":      {resourceName},
	}
	return c.do(ctx, http.MethodDelete, "/resources", query, nil, nil)
}

// grantPath builds the resource/subject path used to grant and revoke
// permissions.
func grantPath(resourceType, resourceName, subjectType, subjectID string) string {
	return fmt.Sprintf("/permissions/resources/%s/%s/subjects/%s/%s",
		url.PathEscape(resourceType), url.PathEscape(resourceName),
		url.PathEscape(subjectType), url.PathEscape(subjectID))
}

// do performs an HTTP request against the permissions service, JSON-encoding the
// body (if any) and decoding the response into out (if any). A 404 response is
// reported as ErrNotFound.
func (c *httpClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
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
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("permissions: %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
