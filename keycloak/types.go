package keycloak

// UserRepresentation is the subset of Keycloak's UserRepresentation type
// that we currently consume. Fields are added as we need them to keep the
// surface area of this library small.
//
// See https://www.keycloak.org/docs-api/latest/rest-api/index.html#UserRepresentation.
type UserRepresentation struct {
	ID                         string              `json:"id,omitempty"`
	Username                   string              `json:"username,omitempty"`
	FirstName                  string              `json:"firstName,omitempty"`
	LastName                   string              `json:"lastName,omitempty"`
	Email                      string              `json:"email,omitempty"`
	EmailVerified              *bool               `json:"emailVerified,omitempty"`
	Attributes                 map[string][]string `json:"attributes,omitempty"`
	Enabled                    *bool               `json:"enabled,omitempty"`
	Self                       string              `json:"self,omitempty"`
	Origin                     string              `json:"origin,omitempty"`
	CreatedTimestamp           int64               `json:"createdTimestamp,omitempty"`
	Totp                       *bool               `json:"totp,omitempty"`
	FederationLink             string              `json:"federationLink,omitempty"`
	ServiceAccountClientID     string              `json:"serviceAccountClientId,omitempty"`
	DisableableCredentialTypes []string            `json:"disableableCredentialTypes,omitempty"`
	RequiredActions            []string            `json:"requiredActions,omitempty"`
	RealmRoles                 []string            `json:"realmRoles,omitempty"`
	ClientRoles                map[string][]string `json:"clientRoles,omitempty"`
	NotBefore                  int32               `json:"notBefore,omitempty"`
	Groups                     []string            `json:"groups,omitempty"`
	Access                     map[string]bool     `json:"access,omitempty"`
}
