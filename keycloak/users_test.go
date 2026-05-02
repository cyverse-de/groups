package keycloak

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestServer wires up an httptest server that mocks the Keycloak token
// endpoint plus a single user-listing path. The handler argument is invoked
// for the user-listing request so each test can assert on the request and
// craft its own response.
func newTestServer(t *testing.T, realm string, handler http.HandlerFunc) (Client, *httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/"+realm+"/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		log.tokenRequests++
		_ = r.ParseForm()
		log.lastTokenForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"expires_in":   300,
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/admin/realms/"+realm+"/users", handler)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c, err := New(Config{
		BaseURL:      srv.URL,
		Realm:        realm,
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return c, srv, log
}

type requestLog struct {
	tokenRequests int
	lastTokenForm url.Values
}

func TestListUsers(t *testing.T) {
	tests := []struct {
		name      string
		opts      *ListUsersOptions
		wantQuery url.Values
	}{
		{
			name:      "nil options sends no query parameters",
			opts:      nil,
			wantQuery: url.Values{},
		},
		{
			name:      "empty options struct sends no query parameters",
			opts:      &ListUsersOptions{},
			wantQuery: url.Values{},
		},
		{
			name: "options serialise to expected query parameters",
			opts: &ListUsersOptions{
				BriefRepresentation: Bool(true),
				Email:               "user@example.com",
				EmailVerified:       Bool(false),
				Enabled:             Bool(true),
				Exact:               Bool(true),
				First:               Int(20),
				Max:                 Int(50),
				Search:              "alice",
				Username:            "alice",
			},
			wantQuery: url.Values{
				"briefRepresentation": {"true"},
				"email":               {"user@example.com"},
				"emailVerified":       {"false"},
				"enabled":             {"true"},
				"exact":               {"true"},
				"first":               {"20"},
				"max":                 {"50"},
				"search":              {"alice"},
				"username":            {"alice"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth, gotMethod, gotPath string
			var gotQuery url.Values
			c, _, _ := newTestServer(t, "myrealm", func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"id":"u1","username":"alice","enabled":true}]`))
			})

			users, err := c.ListUsers(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("ListUsers error: %v", err)
			}

			if gotMethod != http.MethodGet {
				t.Errorf("method = %q, want GET", gotMethod)
			}
			if gotPath != "/admin/realms/myrealm/users" {
				t.Errorf("path = %q", gotPath)
			}
			if gotAuth != "Bearer test-access-token" {
				t.Errorf("Authorization = %q", gotAuth)
			}
			if !equalValues(gotQuery, tc.wantQuery) {
				t.Errorf("query = %v, want %v", gotQuery, tc.wantQuery)
			}
			if len(users) != 1 || users[0].ID != "u1" || users[0].Username != "alice" {
				t.Errorf("unexpected users: %#v", users)
			}
			if users[0].Enabled == nil || !*users[0].Enabled {
				t.Errorf("Enabled = %v, want pointer to true", users[0].Enabled)
			}
		})
	}
}

func TestListUsersAPIError(t *testing.T) {
	c, _, _ := newTestServer(t, "myrealm", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})

	_, err := c.ListUsers(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "forbidden") {
		t.Errorf("body = %q, want it to contain \"forbidden\"", apiErr.Body)
	}
}

func TestTokenIsCachedAcrossCalls(t *testing.T) {
	c, _, log := newTestServer(t, "myrealm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	for range 3 {
		if _, err := c.ListUsers(context.Background(), nil); err != nil {
			t.Fatalf("ListUsers error: %v", err)
		}
	}

	if log.tokenRequests != 1 {
		t.Errorf("token endpoint hit %d times, want 1", log.tokenRequests)
	}
	if got := log.lastTokenForm.Get("grant_type"); got != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", got)
	}
	if got := log.lastTokenForm.Get("client_id"); got != "test-client" {
		t.Errorf("client_id = %q, want test-client", got)
	}
	if got := log.lastTokenForm.Get("client_secret"); got != "test-secret" {
		t.Errorf("client_secret = %q, want test-secret", got)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	full := Config{BaseURL: "http://x", Realm: "r", ClientID: "c", ClientSecret: "s"}
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"missing BaseURL", func(c *Config) { c.BaseURL = "" }},
		{"missing Realm", func(c *Config) { c.Realm = "" }},
		{"missing ClientID", func(c *Config) { c.ClientID = "" }},
		{"missing ClientSecret", func(c *Config) { c.ClientSecret = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := full
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func equalValues(a, b url.Values) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
	}
	return true
}
