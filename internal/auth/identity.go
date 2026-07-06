package auth

import (
	"context"
	"net/http"
	"strings"

	mcpmw "go.klarlabs.de/mcp/middleware"
	"go.klarlabs.de/mcp/protocol"
)

// Identity represents an authenticated caller.
//
// mcp-go removed its in-library Identity type in v1.19.0 when authentication
// became the caller's responsibility ("callers inject http.Client"). This type
// is heartbeat's own replacement: the request-context hook (RequestContextFn)
// derives it from the incoming HTTP request and stashes it in the context, and
// RequireAuth enforces its presence for protected methods.
type Identity struct {
	ID       string
	Name     string
	Metadata map[string]any
}

// TokenClaims holds the subset of OIDC claims heartbeat validates. It replaces
// the removed middleware.TokenClaims.
type TokenClaims struct {
	Subject  string
	Audience []string
	Issuer   string
}

type identityContextKey struct{}

// ContextWithIdentity returns a copy of ctx carrying the authenticated identity.
func ContextWithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// IdentityFromContext returns the authenticated identity, or nil when the
// request was not authenticated.
func IdentityFromContext(ctx context.Context) *Identity {
	id, _ := ctx.Value(identityContextKey{}).(*Identity)
	return id
}

// Authenticator turns a bearer token into an authenticated Identity. It returns
// an error when the token is missing, invalid, or expired.
type Authenticator func(ctx context.Context, token string) (*Identity, error)

// RequestContextFn returns an mcp transport request-context hook
// (transport.WithRequestContextFn) that extracts a bearer token from the
// incoming HTTP request, authenticates it, and stashes the resulting Identity in
// the context for downstream handlers and middleware.
//
// An absent or invalid token leaves the context unauthenticated rather than
// rejecting the request outright: enforcement lives in RequireAuth, so the MCP
// handshake methods (initialize, ping) still pass through unauthenticated —
// preserving the pre-v1.19 WithAuthSkipMethods behavior.
func RequestContextFn(authFn Authenticator) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		token := bearerToken(r.Header.Get("Authorization"))
		if token == "" {
			return ctx
		}
		id, err := authFn(ctx, token)
		if err != nil || id == nil {
			return ctx
		}
		return ContextWithIdentity(ctx, id)
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// RequireAuth returns middleware that rejects requests lacking an authenticated
// identity with a JSON-RPC unauthorized error. Methods listed in skip are always
// allowed through unauthenticated — used for the MCP handshake (initialize,
// ping). This replaces the removed middleware.Auth + WithAuthSkipMethods, with
// token extraction/validation now performed by RequestContextFn at the transport
// layer.
func RequireAuth(skip ...string) mcpmw.Middleware {
	skipSet := make(map[string]struct{}, len(skip))
	for _, m := range skip {
		skipSet[m] = struct{}{}
	}
	return func(next mcpmw.HandlerFunc) mcpmw.HandlerFunc {
		return func(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
			if _, ok := skipSet[req.Method]; ok {
				return next(ctx, req)
			}
			if IdentityFromContext(ctx) == nil {
				return &protocol.Response{
					JSONRPC: protocol.JSONRPCVersion,
					ID:      req.ID,
					Error:   protocol.NewUnauthorized("authentication required"),
				}, nil
			}
			return next(ctx, req)
		}
	}
}
