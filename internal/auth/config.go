package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// UserInfo maps an auth token to a user's identity.
type UserInfo struct {
	Name   string `json:"name"`
	TeamID string `json:"team_id"`
}

// Config holds the authentication configuration.
type Config struct {
	Tokens map[string]UserInfo `json:"tokens"`
}

// TokenValidator resolves a bearer token to its Identity, returning nil when the
// token is unknown. It replaces the removed middleware.StaticTokens validator.
type TokenValidator func(token string) *Identity

// Authenticator adapts the static token validator to the Authenticator signature
// consumed by RequestContextFn.
func (tv TokenValidator) Authenticator() Authenticator {
	return func(_ context.Context, token string) (*Identity, error) {
		if id := tv(token); id != nil {
			return id, nil
		}
		return nil, fmt.Errorf("invalid token")
	}
}

// LoadConfig reads an auth config file and returns a static token validator.
func LoadConfig(path string) (TokenValidator, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read auth config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse auth config: %w", err)
	}

	tokens := make(map[string]*Identity, len(cfg.Tokens))
	for token, info := range cfg.Tokens {
		tokens[token] = &Identity{
			ID:   info.Name,
			Name: info.Name,
			Metadata: map[string]any{
				"team_id": info.TeamID,
			},
		}
	}

	return func(token string) *Identity {
		return tokens[token]
	}, nil
}
