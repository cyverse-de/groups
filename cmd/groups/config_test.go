package main

import (
	"testing"
	"time"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A pool setting that is present has to be usable: silently falling back to the
// default runs the pool at a size nobody asked for, and a pool that is too small
// stalls every request behind the slowest one.
func TestPoolFromConfig(t *testing.T) {
	tests := []struct {
		name         string
		settings     map[string]any
		wantErr      bool
		wantOpen     int
		wantIdle     int
		wantLifetime time.Duration
	}{
		{
			name:     "absent settings leave the store's defaults in place",
			settings: map[string]any{},
		},
		{
			name: "configured settings are passed through",
			settings: map[string]any{
				"db.max-open-conns":    50,
				"db.max-idle-conns":    10,
				"db.conn-max-lifetime": "10m",
			},
			wantOpen:     50,
			wantIdle:     10,
			wantLifetime: 10 * time.Minute,
		},
		{
			name:     "a non-positive connection count is rejected",
			settings: map[string]any{"db.max-open-conns": 0},
			wantErr:  true,
		},
		{
			name:     "an unparsable lifetime is rejected",
			settings: map[string]any{"db.conn-max-lifetime": "soon"},
			wantErr:  true,
		},
		{
			name: "more idle than open connections is rejected",
			settings: map[string]any{
				"db.max-open-conns": 5,
				"db.max-idle-conns": 10,
			},
			wantErr: true,
		},
		{
			name:     "idle connections are compared against the default open limit",
			settings: map[string]any{"db.max-idle-conns": 1000},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := koanf.New(".")
			require.NoError(t, k.Load(confmap.Provider(tt.settings, "."), nil))

			cfg, err := poolFromConfig(k)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOpen, cfg.MaxOpenConns)
			assert.Equal(t, tt.wantIdle, cfg.MaxIdleConns)
			assert.Equal(t, tt.wantLifetime, cfg.ConnMaxLifetime)
		})
	}
}

// The backend selector decides where every display name comes from, so an
// unrecognized value must fail startup rather than quietly picking one.
func TestUserinfoFromConfig(t *testing.T) {
	keycloak := map[string]any{
		"keycloak.base-url":      "https://kc.example.org",
		"keycloak.realm":         "CyVerse",
		"keycloak.client-id":     "groups",
		"keycloak.client-secret": "secret",
	}
	portalConductor := map[string]any{
		"portal-conductor.base-url": "https://portal-conductor",
		"portal-conductor.username": "groups",
		"portal-conductor.password": "secret",
	}
	merge := func(maps ...map[string]any) map[string]any {
		out := map[string]any{}
		for _, m := range maps {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}

	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string
	}{
		{"defaults to portal-conductor", portalConductor, ""},
		{"keycloak named explicitly", merge(keycloak, map[string]any{"userinfo.backend": "keycloak"}), ""},
		{"portal-conductor named explicitly", merge(portalConductor, map[string]any{"userinfo.backend": "portal-conductor"}), ""},
		{"unknown backend", merge(portalConductor, map[string]any{"userinfo.backend": "ldap"}), "userinfo.backend"},
		{"no backend and no portal-conductor settings", keycloak, "portal-conductor.base-url"},
		{
			"portal-conductor without a base url",
			map[string]any{"portal-conductor.username": "groups", "portal-conductor.password": "secret"},
			"portal-conductor.base-url",
		},
		{
			"portal-conductor without credentials",
			map[string]any{"portal-conductor.base-url": "https://portal-conductor"},
			"portal-conductor.username",
		},
		{"keycloak without a realm", map[string]any{"userinfo.backend": "keycloak", "keycloak.base-url": "https://kc.example.org"}, "keycloak.realm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := koanf.New(".")
			require.NoError(t, k.Load(confmap.Provider(tt.settings, "."), nil))

			client, err := userinfoFromConfig(k)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, client)
		})
	}
}
