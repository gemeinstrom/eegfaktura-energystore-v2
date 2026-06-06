// Package auth ports the v1 middleware/* OIDC + JWT + tenant-claim
// authentication stack to energystore-v2.
//
// The wire shape is preserved: callers from eegfaktura-web /
// eegfaktura-admin send `Authorization: Bearer <jwt>` and a tenant
// header. The JWT is verified against the Keycloak realm via OIDC
// discovery + JWKS; the tenant header value must appear in the JWT's
// `tenant` claim array (case-insensitive).
//
// Differences from v1:
//   - logger is stdlib slog (v1 mixed logrus + glog)
//   - config comes from internal/config (env-driven) instead of a
//     keycloak.json on disk
//   - all state lives on the Middleware struct, no init() side effects
package auth

import (
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// PlatformClaims mirrors v1 middleware.PlatformClaims wire-identically so
// existing tokens continue to verify.
type PlatformClaims struct {
	Tenants  []string `json:"tenant"`
	Username string   `json:"preferred_username"`
	Email    string   `json:"email"`
	jwt.RegisteredClaims
}

// HasTenant reports whether the given tenant appears in Tenants
// case-insensitively. v1 used the same comparison.
func (c *PlatformClaims) HasTenant(tenant string) bool {
	tt := strings.ToUpper(tenant)
	for _, v := range c.Tenants {
		if strings.ToUpper(v) == tt {
			return true
		}
	}
	return false
}
