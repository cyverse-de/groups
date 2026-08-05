package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The status endpoint is the readiness probe target, so a dead database must
// turn into a 503 that takes the pod out of rotation. Keycloak staying up is
// not required to serve group requests, so its failure reports degraded but
// still answers 200.
func TestStatusHandler(t *testing.T) {
	tests := []struct {
		name         string
		dbErr, kcErr error
		wantCode     int
		wantDB       bool
		wantKC       bool
	}{
		{name: "healthy", wantCode: http.StatusOK, wantDB: true, wantKC: true},
		{name: "database down", dbErr: assert.AnError, wantCode: http.StatusServiceUnavailable, wantKC: true},
		{name: "keycloak down", kcErr: assert.AnError, wantCode: http.StatusOK, wantDB: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(&mockStore{
				pingFn: func(context.Context) error { return tt.dbErr },
			})
			app.userinfo = &mockUserInfo{
				pingFn: func(context.Context) error { return tt.kcErr },
			}

			rec := doRequestAs(app, http.MethodGet, "/", "", "")
			assert.Equal(t, tt.wantCode, rec.Code)

			var resp StatusResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantDB, resp.Database)
			assert.Equal(t, tt.wantKC, resp.Keycloak)
		})
	}
}
