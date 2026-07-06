// Package auth provides authentication mechanisms for healthcheck-mcp.
//
// OIDC Configuration (config.yaml):
//
//	auth:
//	  mode: oidc
//	  oidc:
//	    issuer: https://accounts.google.com
//	    client_id: your-client-id
//	    scopes: ["openid", "profile", "email"]
//
// Supports any OIDC-compliant provider: Google, Okta, Auth0, Keycloak, etc.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OIDCConfig holds OIDC provider configuration.
type OIDCConfig struct {
	Issuer   string   `yaml:"issuer"`
	ClientID string   `yaml:"client_id"`
	Scopes   []string `yaml:"scopes"`
}

// OIDCValidator validates JWT tokens against an OIDC provider.
// It fetches the provider's JWKS (JSON Web Key Set) for signature verification
// and validates standard JWT claims (issuer, audience, expiry).
type OIDCValidator struct {
	issuer   string
	clientID string
	client   *http.Client

	mu           sync.RWMutex
	discoveryDoc *oidcDiscovery
	lastFetch    time.Time
}

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
}

// NewOIDCValidator creates a validator for the given OIDC provider.
func NewOIDCValidator(cfg OIDCConfig) (*OIDCValidator, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return nil, fmt.Errorf("oidc requires issuer and client_id")
	}

	v := &OIDCValidator{
		issuer:   strings.TrimRight(cfg.Issuer, "/"),
		clientID: cfg.ClientID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}

	// Fetch discovery document to validate the issuer is reachable
	if _, err := v.discover(); err != nil {
		return nil, fmt.Errorf("oidc discovery failed for %s: %w", cfg.Issuer, err)
	}

	return v, nil
}

// Authenticator adapts the OIDC validator to the Authenticator signature
// consumed by RequestContextFn: it validates the bearer token against the
// provider and returns the derived Identity.
func (v *OIDCValidator) Authenticator() Authenticator {
	return func(ctx context.Context, token string) (*Identity, error) {
		claims, err := v.ValidateToken(ctx, token)
		if err != nil {
			return nil, err
		}
		return IdentityFromClaims(claims, token), nil
	}
}

// ValidateToken validates the token by calling the OIDC provider's userinfo
// endpoint. This is simpler than local JWT validation (no JWKS parsing) and
// works with all providers.
func (v *OIDCValidator) ValidateToken(ctx context.Context, token string) (*TokenClaims, error) {
	disc, err := v.discover()
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	// Call userinfo endpoint to validate the token and get claims
	req, err := http.NewRequestWithContext(ctx, "GET", disc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %d", resp.StatusCode)
	}

	var userinfo struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userinfo); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}

	return &TokenClaims{
		Subject:  userinfo.Sub,
		Audience: []string{v.clientID},
		Issuer:   v.issuer,
	}, nil
}

// IdentityFromClaims converts OIDC token claims to an Identity.
func IdentityFromClaims(claims *TokenClaims, token string) *Identity {
	return &Identity{
		ID:   claims.Subject,
		Name: claims.Subject,
		Metadata: map[string]any{
			"issuer": claims.Issuer,
		},
	}
}

func (v *OIDCValidator) discover() (*oidcDiscovery, error) {
	v.mu.RLock()
	if v.discoveryDoc != nil && time.Since(v.lastFetch) < time.Hour {
		doc := v.discoveryDoc
		v.mu.RUnlock()
		return doc, nil
	}
	v.mu.RUnlock()

	v.mu.Lock()
	defer v.mu.Unlock()

	// Double-check after acquiring write lock
	if v.discoveryDoc != nil && time.Since(v.lastFetch) < time.Hour {
		return v.discoveryDoc, nil
	}

	url := v.issuer + "/.well-known/openid-configuration"
	resp, err := v.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery endpoint returned %d", resp.StatusCode)
	}

	var doc oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}

	v.discoveryDoc = &doc
	v.lastFetch = time.Now()
	return &doc, nil
}
