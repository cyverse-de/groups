package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cyverse-de/groups/model"
	"github.com/cyverse-de/groups/store"
	"github.com/labstack/echo/v4"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The error handler is the last line of defense against leaking a downstream
// service's response body or topology to API clients: anything that is not an
// *echo.HTTPError gets logged for operators and replaced with a generic body.
func TestErrorHandler(t *testing.T) {
	downstream := fmt.Errorf("permissions: GET /permissions/abc returned 500: %s",
		"secret response body from permissions.cyverse.internal")

	tests := []struct {
		name        string
		err         error
		wantCode    int
		wantBody    string
		mustNotHold []string
		wantLogged  bool
	}{
		{
			name:        "a downstream error is logged, not echoed",
			err:         downstream,
			wantCode:    http.StatusInternalServerError,
			wantBody:    `{"error":"internal error"}`,
			mustNotHold: []string{"secret response body", "permissions.cyverse.internal"},
			wantLogged:  true,
		},
		{
			name:     "a wrapped HTTPError keeps its status",
			err:      fmt.Errorf("looking up the group: %w", echo.ErrNotFound),
			wantCode: http.StatusNotFound,
		},
	}

	hook := logtest.NewLocal(log.Logger)
	defer hook.Reset()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			app := newTestApp(&mockStore{})

			req := httptest.NewRequest(http.MethodGet, "/groups/g1?user=alice", nil)
			rec := httptest.NewRecorder()
			c := app.router.NewContext(req, rec)

			app.errorHandler(tt.err, c)

			assert.Equal(t, tt.wantCode, rec.Code)
			if tt.wantBody != "" {
				assert.JSONEq(t, tt.wantBody, rec.Body.String())
			}
			for _, secret := range tt.mustNotHold {
				assert.NotContains(t, rec.Body.String(), secret)
			}

			if !tt.wantLogged {
				return
			}
			require.NotEmpty(t, hook.Entries, "the real error must be logged for operators")
			entry := hook.LastEntry()
			assert.Contains(t, entry.Message, "secret response body")
			assert.Equal(t, http.MethodGet, entry.Data["method"])
			assert.Equal(t, "/groups/g1", entry.Data["path"])
		})
	}
}

// An unrecognized store error must reach the client as exactly "internal
// error", with the driver's message confined to the logs.
func TestStoreErrorDefaultArmHidesDetails(t *testing.T) {
	app := newTestApp(&mockStore{
		getGroupFn: func(context.Context, string) (*model.Group, error) {
			return nil, errors.New(`pq: connection refused to "db-host.internal"`)
		},
	})

	rec := doRequest(app, http.MethodGet, "/groups/g1", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"internal error"}`, rec.Body.String())
}

// The sentinel errors carry a "store: " prefix that names an internal layer;
// clients get the clean rule instead.
func TestStoreErrorMessagesAreClean(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, `{"error":"not found"}`},
		{"conflict", store.ErrConflict, http.StatusConflict, `{"error":"conflict"}`},
		{
			"conflict with detail",
			fmt.Errorf("%w: a group of that type, owner, and name already exists", store.ErrConflict),
			http.StatusConflict,
			`{"error":"conflict: a group of that type, owner, and name already exists"}`,
		},
		{
			"invalid value with detail",
			fmt.Errorf("%w: a group name cannot be blank", store.ErrInvalid),
			http.StatusBadRequest,
			`{"error":"invalid value: a group name cannot be blank"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err
			app := newTestApp(&mockStore{
				getGroupFn: func(context.Context, string) (*model.Group, error) {
					return nil, err
				},
			})

			rec := doRequest(app, http.MethodGet, "/groups/g1", "")
			assert.Equal(t, tt.wantCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
		})
	}
}
