package userinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPortalConductor(t *testing.T, handler http.HandlerFunc) Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewPortalConductorClient(PortalConductorConfig{
		BaseURL: srv.URL, Username: "groups", Password: "secret",
	})
}

func TestPortalConductorGetMany(t *testing.T) {
	t.Run("resolves every user in one request", func(t *testing.T) {
		var (
			requests int
			gotBody  map[string][]string
			gotAuth  bool
		)
		c := testPortalConductor(t, func(w http.ResponseWriter, r *http.Request) {
			requests++
			user, pass, ok := r.BasicAuth()
			gotAuth = ok && user == "groups" && pass == "secret"
			assert.Equal(t, "/ldap/users/search", r.URL.Path)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			_, _ = io.WriteString(w, `{"users":[
				{"username":"alice","given_name":"Alice","surname":"A","email":"alice@example.org","organization":"UA"},
				{"username":"bob","given_name":null,"surname":null,"email":null,"organization":null}]}`)
		})

		subjects, err := c.GetMany(context.Background(), []string{"alice", "bob"})
		require.NoError(t, err)
		assert.Equal(t, 1, requests, "a bulk lookup must cost one request, not one per user")
		assert.True(t, gotAuth, "the request must carry the service credentials")
		assert.Equal(t, []string{"alice", "bob"}, gotBody["usernames"])

		require.Len(t, subjects, 2)
		assert.Equal(t, model.Subject{
			ID: "alice", Name: "alice", FirstName: "Alice", LastName: "A",
			Email: "alice@example.org", Institution: "UA", SourceID: model.SourceUser,
		}, subjects[0])
		// A directory record with no optional attributes must not carry nulls
		// into the response.
		assert.Equal(t, model.Subject{ID: "bob", Name: "bob", SourceID: model.SourceUser}, subjects[1])
	})

	t.Run("an unknown username is omitted, not an error", func(t *testing.T) {
		c := testPortalConductor(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"users":[{"username":"alice"}]}`)
		})

		subjects, err := c.GetMany(context.Background(), []string{"alice", "ghost"})
		require.NoError(t, err)
		require.Len(t, subjects, 1)
		assert.Equal(t, "alice", subjects[0].ID)
	})

	t.Run("an empty list costs no request", func(t *testing.T) {
		requests := 0
		c := testPortalConductor(t, func(w http.ResponseWriter, _ *http.Request) {
			requests++
			_, _ = io.WriteString(w, `{"users":[]}`)
		})

		subjects, err := c.GetMany(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, subjects)
		assert.Zero(t, requests)
	})

	t.Run("a failure is reported rather than silently short", func(t *testing.T) {
		c := testPortalConductor(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"detail":"LDAP unreachable"}`)
		})

		_, err := c.GetMany(context.Background(), []string{"alice"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "LDAP unreachable")
	})
}

func TestPortalConductorGet(t *testing.T) {
	t.Run("returns the subject", func(t *testing.T) {
		c := testPortalConductor(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/ldap/users/alice", r.URL.Path)
			_, _ = io.WriteString(w, `{"username":"alice","organization":"UA"}`)
		})

		subject, err := c.Get(context.Background(), "alice")
		require.NoError(t, err)
		assert.Equal(t, "alice", subject.ID)
		assert.Equal(t, "UA", subject.Institution)
	})

	t.Run("a missing user is ErrNotFound", func(t *testing.T) {
		c := testPortalConductor(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})

		_, err := c.Get(context.Background(), "ghost")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("a username with a slash stays one path segment", func(t *testing.T) {
		var gotPath string
		c := testPortalConductor(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.EscapedPath()
			_, _ = io.WriteString(w, `{"username":"a/b"}`)
		})

		_, err := c.Get(context.Background(), "a/b")
		require.NoError(t, err)
		assert.Equal(t, "/ldap/users/a%2Fb", gotPath)
	})
}

func TestPortalConductorSearch(t *testing.T) {
	var gotQuery string
	c := testPortalConductor(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/ldap/users", r.URL.Path)
		gotQuery = r.URL.Query().Get("search")
		_, _ = io.WriteString(w, `{"users":[{"username":"alice"}]}`)
	})

	subjects, err := c.Search(context.Background(), "ali ce")
	require.NoError(t, err)
	assert.Equal(t, "ali ce", gotQuery)
	require.Len(t, subjects, 1)
	assert.Equal(t, model.SourceUser, subjects[0].SourceID)
}

func TestPortalConductorPing(t *testing.T) {
	t.Run("succeeds when the directory answers", func(t *testing.T) {
		var method, path string
		c := testPortalConductor(t, func(w http.ResponseWriter, r *http.Request) {
			method, path = r.Method, r.URL.Path
			_, _ = io.WriteString(w, `{"users":[]}`)
		})

		require.NoError(t, c.Ping(context.Background()))
		assert.Equal(t, http.MethodPost, method)
		assert.Equal(t, "/ldap/users/search", path)
	})

	t.Run("fails when the credentials are rejected", func(t *testing.T) {
		c := testPortalConductor(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})

		assert.Error(t, c.Ping(context.Background()))
	})
}
