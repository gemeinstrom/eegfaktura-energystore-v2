package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

const (
	BasicSchema  = "Basic "
	BearerSchema = "Bearer "
)

// HandlerFunc receives the verified claims and the upper-cased tenant
// header value. Same shape as v1 middleware.JWTHandlerFunc.
type HandlerFunc func(w http.ResponseWriter, r *http.Request, claims *PlatformClaims, tenant string)

// Verifier abstracts the parts of *KeycloakClient that Middleware uses, so
// tests can substitute a no-discovery fake.
type Verifier interface {
	Verify(ctx context.Context, raw string) (idTokenClaimsExtractor, error)
}

// idTokenClaimsExtractor mirrors *oidc.IDToken's Claims method so the
// fake verifier doesn't need to construct a real IDToken.
type idTokenClaimsExtractor interface {
	Claims(target any) error
}

// passwordVerifier is the subset of *KeycloakClient that ProtectAPI uses.
type passwordVerifier interface {
	AuthenticateUserWithPassword(ctx context.Context, user, pass string) (idTokenClaimsExtractor, error)
}

// Middleware bundles the verifiers and logger used by Protect* / GQL.
type Middleware struct {
	appVerifier Verifier
	apiPassword passwordVerifier
	logger      *slog.Logger
}

// Options groups optional dependencies.
type Options struct {
	Logger *slog.Logger
}

// New constructs a Middleware. appVerifier verifies frontend bearer
// tokens; apiPassword runs the password grant for Basic-Auth bridge. Both
// may be nil if the corresponding flow is unused (Protect* will then 503).
func New(appVerifier Verifier, apiPassword passwordVerifier, opts Options) *Middleware {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Middleware{
		appVerifier: appVerifier,
		apiPassword: apiPassword,
		logger:      logger.With("component", "auth"),
	}
}

// FromKeycloak adapts a (frontend, api) *KeycloakClient pair into a
// Middleware. This is the canonical constructor in production.
func FromKeycloak(app, api *KeycloakClient, opts Options) *Middleware {
	var appV Verifier
	var apiP passwordVerifier
	if app != nil {
		appV = appVerifierAdapter{app}
	}
	if api != nil {
		apiP = apiPasswordAdapter{api}
	}
	return New(appV, apiP, opts)
}

type appVerifierAdapter struct{ kc *KeycloakClient }

func (a appVerifierAdapter) Verify(ctx context.Context, raw string) (idTokenClaimsExtractor, error) {
	tok, err := a.kc.Verify(ctx, raw)
	if err != nil {
		return nil, err
	}
	return tok, nil
}

type apiPasswordAdapter struct{ kc *KeycloakClient }

func (a apiPasswordAdapter) AuthenticateUserWithPassword(ctx context.Context, user, pass string) (idTokenClaimsExtractor, error) {
	tok, err := a.kc.AuthenticateUserWithPassword(ctx, user, pass)
	if err != nil {
		return nil, err
	}
	return tok, nil
}

// tenantCtxKey is used by GQL middleware to stash the verified tenant on
// the request context so resolvers can read it via ForContextTenant.
type ctxKey int

const tenantCtxKey ctxKey = 1

// ProtectApp wraps a handler with Bearer-JWT verification. The
// `X-Tenant` header is required and must be present in the JWT's tenant
// claim array.
func (m *Middleware) ProtectApp(next HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			m.respondAuthError(w, err)
			return
		}
		if m.appVerifier == nil {
			m.logger.Error("ProtectApp called without app verifier")
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		tok, err := m.appVerifier.Verify(r.Context(), raw)
		if err != nil {
			m.logger.Warn("token verify failed", "err", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		claims := &PlatformClaims{}
		if err := tok.Claims(claims); err != nil {
			m.logger.Warn("claims decode failed", "err", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		tenant := r.Header.Get("X-Tenant")
		if !claims.HasTenant(tenant) {
			m.logger.Warn("tenant not in claims", "tenant", tenant)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next(w, r, claims, strings.ToUpper(tenant))
	}
}

// ProtectAPI wraps a handler with Basic-Auth → Keycloak password grant.
// Same flow as v1 ProtectApi.
func (m *Middleware) ProtectAPI(next HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, err := extractBasic(r)
		if err != nil {
			m.respondAuthError(w, err)
			return
		}
		if m.apiPassword == nil {
			m.logger.Error("ProtectAPI called without password verifier")
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		tok, err := m.apiPassword.AuthenticateUserWithPassword(r.Context(), user, pass)
		if err != nil {
			m.logger.Warn("password grant failed", "user", user, "err", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		claims := &PlatformClaims{}
		if err := tok.Claims(claims); err != nil {
			m.logger.Warn("claims decode failed", "err", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		tenant := r.Header.Get("X-Tenant")
		if !claims.HasTenant(tenant) {
			m.logger.Warn("tenant not in claims", "tenant", tenant)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next(w, r, claims, strings.ToUpper(tenant))
	}
}

// GQL wraps an http.Handler with Bearer-JWT verification. Mirrors v1
// GQLMiddleware: supports both `tenant` and `X-Tenant` headers (the
// customer-web sends `tenant` for GraphQL, REST routes use `X-Tenant`).
// Verified tenant is stored on the context for resolvers via
// ForContextTenant.
func (m *Middleware) GQL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			m.respondAuthError(w, err)
			return
		}
		if m.appVerifier == nil {
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		tok, err := m.appVerifier.Verify(r.Context(), raw)
		if err != nil {
			m.logger.Warn("GQL: token verify failed", "err", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		claims := &PlatformClaims{}
		if err := tok.Claims(claims); err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		tenant := r.Header.Get("tenant")
		if tenant == "" {
			tenant = r.Header.Get("X-Tenant")
		}
		if !claims.HasTenant(tenant) {
			m.logger.Warn("GQL: tenant not in claims", "tenant", tenant)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), tenantCtxKey, strings.ToUpper(tenant))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ForContextTenant returns the verified tenant stored on the request
// context by GQL, or "" if none.
func ForContextTenant(ctx context.Context) string {
	v, _ := ctx.Value(tenantCtxKey).(string)
	return v
}

var (
	errMissingAuth   = errors.New("missing Authorization header")
	errBadSchema     = errors.New("expected Bearer schema")
	errBadBasic      = errors.New("expected Basic schema")
	errBadBasicCreds = errors.New("malformed Basic credentials")
)

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", errMissingAuth
	}
	if !strings.HasPrefix(h, BearerSchema) {
		return "", errBadSchema
	}
	return h[len(BearerSchema):], nil
}

func extractBasic(r *http.Request) (user, pass string, err error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", "", errMissingAuth
	}
	if !strings.HasPrefix(h, BasicSchema) {
		return "", "", errBadBasic
	}
	dec, derr := base64.StdEncoding.DecodeString(h[len(BasicSchema):])
	if derr != nil {
		// v1 used URLEncoding; fall back to that.
		dec, derr = base64.URLEncoding.DecodeString(h[len(BasicSchema):])
	}
	if derr != nil {
		return "", "", errBadBasicCreds
	}
	parts := strings.SplitN(string(dec), ":", 2)
	if len(parts) != 2 {
		return "", "", errBadBasicCreds
	}
	return parts[0], parts[1], nil
}

func (m *Middleware) respondAuthError(w http.ResponseWriter, err error) {
	switch err {
	case errMissingAuth:
		w.WriteHeader(http.StatusForbidden)
	case errBadSchema, errBadBasic, errBadBasicCreds:
		w.WriteHeader(http.StatusBadRequest)
	default:
		w.WriteHeader(http.StatusForbidden)
	}
}
