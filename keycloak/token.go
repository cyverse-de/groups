package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tokenLeeway is subtracted from the lifetime reported by Keycloak so that
// the cached token is refreshed slightly before its real expiration.
const tokenLeeway = 30 * time.Second

type cachedToken struct {
	value  string
	expiry time.Time
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// accessToken returns a cached access token, fetching a new one via the
// client_credentials grant when none is available or the cached one is
// near expiration.
func (c *client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.cachedToken.value != "" && time.Now().Before(c.cachedToken.expiry) {
		return c.cachedToken.value, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)

	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.baseURL, url.PathEscape(c.realm))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: requesting access token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &APIError{
			Method:     http.MethodPost,
			Path:       "/realms/" + c.realm + "/protocol/openid-connect/token",
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("keycloak: decoding token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("keycloak: token response did not contain an access_token")
	}

	lifetime := time.Duration(tr.ExpiresIn) * time.Second
	if lifetime > tokenLeeway {
		lifetime -= tokenLeeway
	}
	c.cachedToken = cachedToken{
		value:  tr.AccessToken,
		expiry: time.Now().Add(lifetime),
	}
	return c.cachedToken.value, nil
}
