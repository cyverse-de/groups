package permissions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrantSendsLevelAndPath(t *testing.T) {
	var gotPath, gotMethod, gotLevel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotLevel = body["permission_level"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := NewClient(srv.URL).Grant(context.Background(), "group", "g1", SubjectTypeUser, "alice", LevelOwn)
	require.NoError(t, err)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/permissions/resources/group/g1/subjects/user/alice", gotPath)
	assert.Equal(t, LevelOwn, gotLevel)
}

func TestCheckSendsQueryAndParsesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/permissions/subjects/user/alice/group/g1", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("lookup"))
		assert.Equal(t, LevelWrite, r.URL.Query().Get("min_level"))
		_, _ = io.WriteString(w, `{"permissions":[{"id":"p1","permission_level":"own"}]}`)
	}))
	defer srv.Close()

	ok, err := NewClient(srv.URL).Check(context.Background(), SubjectTypeUser, "alice", "group", "g1", LevelWrite, true)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCheckEmptyResultIsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"permissions":[]}`)
	}))
	defer srv.Close()

	ok, err := NewClient(srv.URL).Check(context.Background(), SubjectTypeUser, "bob", "group", "g1", LevelRead, true)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestEnsureResourceTypeSkipsWhenPresent(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			assert.Equal(t, "group", r.URL.Query().Get("resource_type_name"))
			_, _ = io.WriteString(w, `{"resource_types":[{"id":"1","name":"group"}]}`)
		case http.MethodPost:
			posted = true
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	require.NoError(t, NewClient(srv.URL).EnsureResourceType(context.Background(), "group", "desc"))
	assert.False(t, posted, "should not POST when the resource type already exists")
}

func TestEnsureResourceTypeCreatesWhenAbsent(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"resource_types":[]}`)
		case http.MethodPost:
			posted = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "group", body["name"])
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	require.NoError(t, NewClient(srv.URL).EnsureResourceType(context.Background(), "group", "desc"))
	assert.True(t, posted)
}

func TestListSubjectUsesTheAbbreviatedListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/permissions/abbreviated/subjects/user/alice/group", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("lookup"))
		_, _ = io.WriteString(w, `{"permissions":[
			{"id":"p1","resource_name":"g1","resource_type":"group","permission_level":"own"},
			{"id":"p2","resource_name":"g2","resource_type":"group","permission_level":"read"}]}`)
	}))
	defer srv.Close()

	perms, err := NewClient(srv.URL).ListSubject(context.Background(), SubjectTypeUser, "alice", "group", true)
	require.NoError(t, err)
	require.Len(t, perms, 2)
	assert.Equal(t, "g1", perms[0].ResourceName)
	assert.Equal(t, LevelOwn, perms[0].PermissionLevel)
	assert.Equal(t, "g2", perms[1].ResourceName)
	assert.Equal(t, LevelRead, perms[1].PermissionLevel)
}

func TestListSubjectOmitsLookupWhenNotRequested(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("lookup"))
		_, _ = io.WriteString(w, `{"permissions":[]}`)
	}))
	defer srv.Close()

	perms, err := NewClient(srv.URL).ListSubject(context.Background(), SubjectTypeUser, "bob", "group", false)
	require.NoError(t, err)
	assert.Empty(t, perms)
}

func TestDeleteResourceSendsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		assert.Equal(t, "/resources", r.URL.Path)
		assert.Equal(t, "group", r.URL.Query().Get("resource_type_name"))
		assert.Equal(t, "g1", r.URL.Query().Get("resource_name"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, NewClient(srv.URL).DeleteResource(context.Background(), "group", "g1"))
}

func TestNotFoundMapsToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := NewClient(srv.URL).Revoke(context.Background(), "group", "g1", SubjectTypeUser, "alice")
	assert.ErrorIs(t, err, ErrNotFound)
}
