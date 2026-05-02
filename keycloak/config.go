package keycloak

import "net/http"

// Config holds the settings needed to connect to a Keycloak realm as an
// administrator using a confidential client with the client_credentials
// grant. The associated service account must have whatever realm-management
// roles are required by the API calls being made.
type Config struct {
	// BaseURL is the root URL of the Keycloak server, for example
	// "https://keycloak.example.com". Trailing slashes are tolerated.
	BaseURL string

	// Realm is the realm the client will administer. The same realm is
	// used to obtain access tokens via the client_credentials grant.
	Realm string

	// ClientID is the client_id of the confidential client whose service
	// account will be used to authenticate.
	ClientID string

	// ClientSecret is the secret for the confidential client.
	ClientSecret string

	// HTTPClient, if non-nil, is used for all outbound requests. Callers
	// can supply this to configure timeouts, transports, or to inject a
	// test double. When nil, http.DefaultClient is used.
	HTTPClient *http.Client
}
