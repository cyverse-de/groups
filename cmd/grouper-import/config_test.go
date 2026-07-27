package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfig() config {
	return config{
		GrouperURI:  "postgres://grouper@grouper-db/grouper",
		Prefix:      "iplant:de:prod",
		DatabaseURI: "postgres://de@de-db/de",
		Schema:      "permissions",
		UserSuffix:  "@iplantcollaborative.org",
		Permissions: "http://permissions",
		Phase:       phaseAll,
	}
}

// TestConfigValidate covers failing fast on bad configuration rather than
// discovering it part-way through an import that has already written rows.
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config)
		wantErr string
	}{
		{name: "a complete configuration", mutate: func(*config) {}},
		{
			name:    "no Grouper database",
			mutate:  func(c *config) { c.GrouperURI = "" },
			wantErr: "grouper.uri",
		},
		{
			// Scoping to iplant:de:% instead would import another environment's
			// de-users, which collides with ours on (group_type, owner, name).
			name:    "no environment prefix",
			mutate:  func(c *config) { c.Prefix = "" },
			wantErr: "grouper.prefix",
		},
		{
			name:    "no DE database",
			mutate:  func(c *config) { c.DatabaseURI = "" },
			wantErr: "db.uri",
		},
		{
			name:    "no username suffix",
			mutate:  func(c *config) { c.UserSuffix = "" },
			wantErr: "users.suffix",
		},
		{
			// Grants go through the permissions HTTP API, which owns resource and
			// subject creation, so the permissions phase cannot run without it.
			name:    "no permissions service",
			mutate:  func(c *config) { c.Permissions = "" },
			wantErr: "permissions.base",
		},
		{
			name:    "an unknown phase",
			mutate:  func(c *config) { c.Phase = "everything" },
			wantErr: "phase",
		},
		{
			name:   "the groups phase alone needs no permissions service",
			mutate: func(c *config) { c.Phase = phaseGroups; c.Permissions = "" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)

			err := c.validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr,
				"the message must name the setting that is wrong")
		})
	}
}

// TestConfigPrefixIsTrimmed guards against a trailing colon in the configured
// prefix, which would otherwise make every name fail to parse.
func TestConfigPrefixIsTrimmed(t *testing.T) {
	c := validConfig()
	c.Prefix = "iplant:de:prod:"
	require.NoError(t, c.validate())
	assert.Equal(t, "iplant:de:prod", c.Prefix)
}
