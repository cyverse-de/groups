package userinfo

import (
	"testing"

	"github.com/Nerzal/gocloak/v13"
	"github.com/cyverse-de/groups/model"
	"github.com/stretchr/testify/assert"
)

func TestToSubject(t *testing.T) {
	tests := []struct {
		name string
		in   *gocloak.User
		want model.Subject
	}{
		{
			name: "nil user",
			in:   nil,
			want: model.Subject{},
		},
		{
			name: "full user",
			in: &gocloak.User{
				ID:        gocloak.StringP("uuid-1"),
				Username:  gocloak.StringP("jdoe"),
				FirstName: gocloak.StringP("Jane"),
				LastName:  gocloak.StringP("Doe"),
				Email:     gocloak.StringP("jane@example.org"),
				Attributes: &map[string][]string{
					attrInstitution: {"CyVerse"},
				},
			},
			want: model.Subject{
				ID:          "jdoe",
				Name:        "jdoe",
				FirstName:   "Jane",
				LastName:    "Doe",
				Email:       "jane@example.org",
				Institution: "CyVerse",
				SourceID:    model.SourceUser,
			},
		},
		{
			// The federation link is deliberately not used as the source: callers
			// compare source IDs against the Grouper values, and the link is an
			// opaque provider UUID none of them understand.
			name: "federated user still reports the ldap source",
			in: &gocloak.User{
				Username:       gocloak.StringP("jdoe"),
				FederationLink: gocloak.StringP("f7a1-provider-uuid"),
			},
			want: model.Subject{
				ID:       "jdoe",
				Name:     "jdoe",
				SourceID: model.SourceUser,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toSubject(tt.in))
		})
	}
}

// Without a timeout an unresponsive Keycloak would pin request goroutines
// indefinitely -- and the token mutex with them.
func TestNewKeycloakClientSetsATimeout(t *testing.T) {
	c, ok := NewKeycloakClient(Config{BaseURL: "http://keycloak.example.org"}).(*keycloakClient)
	if !ok {
		t.Fatal("NewKeycloakClient no longer returns a *keycloakClient")
	}
	assert.Equal(t, DefaultRequestTimeout, c.gc.RestyClient().GetClient().Timeout)
}

func TestFirstAttr(t *testing.T) {
	attrs := &map[string][]string{
		"k":     {"v1", "v2"},
		"empty": {},
	}

	assert.Equal(t, "v1", firstAttr(attrs, "k"))
	assert.Equal(t, "", firstAttr(attrs, "empty"))
	assert.Equal(t, "", firstAttr(attrs, "missing"))
	assert.Equal(t, "", firstAttr(nil, "k"))
}
