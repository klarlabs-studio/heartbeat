package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/mcp/protocol"

	"github.com/felixgeelhaar/heartbeat/internal/auth"
)

func TestContextWithIdentityRoundTrip(t *testing.T) {
	if got := auth.IdentityFromContext(context.Background()); got != nil {
		t.Fatalf("expected nil identity on bare context, got %+v", got)
	}

	id := &auth.Identity{ID: "u1", Name: "Alice", Metadata: map[string]any{"team_id": "t1"}}
	ctx := auth.ContextWithIdentity(context.Background(), id)

	got := auth.IdentityFromContext(ctx)
	if got == nil || got.Name != "Alice" {
		t.Fatalf("expected Alice, got %+v", got)
	}
	if tid, _ := got.Metadata["team_id"].(string); tid != "t1" {
		t.Errorf("expected team_id t1, got %q", tid)
	}
}

func TestTokenValidatorAuthenticator(t *testing.T) {
	tv := auth.TokenValidator(func(token string) *auth.Identity {
		if token == "good" {
			return &auth.Identity{ID: "u1", Name: "Alice"}
		}
		return nil
	})
	authFn := tv.Authenticator()

	id, err := authFn(context.Background(), "good")
	if err != nil || id == nil || id.Name != "Alice" {
		t.Fatalf("expected Alice for good token, got id=%+v err=%v", id, err)
	}

	if _, err := authFn(context.Background(), "bad"); err == nil {
		t.Error("expected error for unknown token")
	}
}

func TestRequestContextFn(t *testing.T) {
	authFn := auth.Authenticator(func(_ context.Context, token string) (*auth.Identity, error) {
		if token == "good" {
			return &auth.Identity{ID: "u1", Name: "Alice"}, nil
		}
		return nil, http.ErrNoCookie // arbitrary non-nil error
	})
	fn := auth.RequestContextFn(authFn)

	cases := []struct {
		name       string
		header     string
		wantAuthed bool
	}{
		{"valid bearer", "Bearer good", true},
		{"case-insensitive scheme", "bearer good", true},
		{"invalid token", "Bearer bad", false},
		{"missing header", "", false},
		{"non-bearer", "Basic abc", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			ctx := fn(context.Background(), r)
			id := auth.IdentityFromContext(ctx)
			if tc.wantAuthed && id == nil {
				t.Error("expected authenticated context")
			}
			if !tc.wantAuthed && id != nil {
				t.Errorf("expected unauthenticated context, got %+v", id)
			}
		})
	}
}

func TestOIDCValidatorAuthenticator(t *testing.T) {
	server := newMockOIDCServer()
	defer server.Close()

	validator, err := auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:   server.URL,
		ClientID: "test-client",
	})
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	authFn := validator.Authenticator()

	id, err := authFn(context.Background(), "valid-token")
	if err != nil {
		t.Fatalf("authenticate valid token: %v", err)
	}
	if id.ID != "user-123" {
		t.Errorf("expected id user-123, got %q", id.ID)
	}
	if iss, _ := id.Metadata["issuer"].(string); iss != server.URL {
		t.Errorf("expected issuer %s, got %q", server.URL, iss)
	}

	if _, err := authFn(context.Background(), "invalid-token"); err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestRequireAuth(t *testing.T) {
	called := false
	next := func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
		called = true
		return &protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID, Result: "ok"}, nil
	}
	mw := auth.RequireAuth("initialize", "ping")

	// Skipped method passes through even without identity.
	called = false
	resp, err := mw(next)(context.Background(), &protocol.Request{Method: "initialize"})
	if err != nil || !called || resp.Error != nil {
		t.Fatalf("skip method should pass through: called=%v resp=%+v err=%v", called, resp, err)
	}

	// Protected method without identity is rejected with unauthorized.
	called = false
	resp, err = mw(next)(context.Background(), &protocol.Request{Method: "tools/call"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if called {
		t.Error("handler should not run for unauthenticated protected method")
	}
	if resp.Error == nil || resp.Error.Code != protocol.CodeUnauthorized {
		t.Errorf("expected unauthorized error, got %+v", resp.Error)
	}

	// Protected method with identity is allowed.
	called = false
	ctx := auth.ContextWithIdentity(context.Background(), &auth.Identity{ID: "u1", Name: "Alice"})
	resp, err = mw(next)(ctx, &protocol.Request{Method: "tools/call"})
	if err != nil || !called || resp.Error != nil {
		t.Fatalf("authenticated protected method should pass: called=%v resp=%+v err=%v", called, resp, err)
	}
}
