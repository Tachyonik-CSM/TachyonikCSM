// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Request middleware: authenticating the caller, and putting the identity it
// establishes — user id, role, and the set of users a shared workspace makes
// visible — into the request context where the handlers read it.
//
// Both kinds of caller are handled here: a browser presenting a session token,
// and another service presenting the internal service key. A token counts only
// as a session (not one of SystemManager's narrow single-purpose tokens) and
// only while its account is active (accounts.go). Request bodies are bounded,
// and the CORS answers are cache-safe.

package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"tachyonik/assetmanager/internal/config"
	"tachyonik/lib/logger"
)

// Context keys
type contextKey string

const userIDKey contextKey = "userID"
const userRoleKey contextKey = "userRole"
const internalSvcKey contextKey = "isInternalService"

// JWTClaims represents the JWT token claims
type JWTClaims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// The marks of SystemManager's narrow tokens (see ParseJWT); never set
	// on a session token.
	MFAPending            bool `json:"mfa_pending,omitempty"`
	MFASetupRequired      bool `json:"mfa_setup_required,omitempty"`
	PasswordSetupRequired bool `json:"password_setup_required,omitempty"`
	jwt.RegisteredClaims
}

// GetUserID extracts the user ID from the request context
func GetUserID(r *http.Request) (int64, bool) {
	userID, ok := r.Context().Value(userIDKey).(int64)
	return userID, ok
}

// IsInternalService reports whether the request authenticated with the
// internal service key (service-to-service) rather than a user JWT. Handlers
// that must never be reachable by an end user gate on this.
func IsInternalService(r *http.Request) bool {
	v, _ := r.Context().Value(internalSvcKey).(bool)
	return v
}

// internalKeyValid reports whether key is this service's internal service
// key. Never with no key configured; compared in constant time, so the time a
// wrong guess takes says nothing about how close it was.
func internalKeyValid(cfg *config.Config, key string) bool {
	want := cfg.Auth.InternalServiceKey
	return want != "" && key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(want)) == 1
}

// RequireInternalService authenticates ONLY with the internal-service key
// (no user-JWT fallback). Applied to /api/internal so service-to-service
// endpoints can never be reached with an ordinary user JWT. Sets the
// internal-service flag (so IsInternalService-guarded handlers still pass)
// and, from the impersonation headers, the user a service acts for.
func RequireInternalService(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !internalKeyValid(cfg, r.Header.Get("X-Internal-Service-Key")) {
				http.Error(w, "Internal service authentication required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(internalContext(r)))
		})
	}
}

// internalContext marks the request as a service's and takes the user it acts
// for from the impersonation headers, when given.
func internalContext(r *http.Request) context.Context {
	ctx := context.WithValue(r.Context(), internalSvcKey, true)
	if userIDStr := r.Header.Get("X-User-ID"); userIDStr != "" {
		if userID, err := strconv.ParseInt(userIDStr, 10, 64); err == nil {
			ctx = context.WithValue(ctx, userIDKey, userID)
		}
	}
	if role := r.Header.Get("X-User-Role"); role != "" {
		ctx = context.WithValue(ctx, userRoleKey, role)
	}
	return ctx
}

// AuthMiddleware authenticates the caller: a service by the internal service
// key, a user by a session token whose account is still active. accounts may
// be nil only in tests that do not exercise the account check.
func AuthMiddleware(cfg *config.Config, accounts *accountChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check for internal service key first (only if server has one configured)
			if internalKey := r.Header.Get("X-Internal-Service-Key"); internalKey != "" && cfg.Auth.InternalServiceKey != "" {
				if !internalKeyValid(cfg, internalKey) {
					http.Error(w, "Invalid internal service key", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r.WithContext(internalContext(r)))
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Authorization header required", http.StatusUnauthorized)
				return
			}
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, "Invalid authorization header format", http.StatusUnauthorized)
				return
			}

			claims, err := ParseJWT(cfg, parts[1])
			if err != nil {
				http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
				return
			}
			if status, msg := accountRefusal(accounts, claims.UserID); status != 0 {
				http.Error(w, msg, status)
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, userRoleKey, claims.Role)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// accountRefusal is the status and message to refuse a user's request with
// when their account may not use its session, or 0.
func accountRefusal(accounts *accountChecker, userID int64) (int, string) {
	if accounts == nil {
		return 0, ""
	}
	switch err := accounts.check(userID); {
	case errors.Is(err, errAccountInactive):
		logger.Warnf("Auth: user %d is deactivated or deleted — rejecting", userID)
		return http.StatusForbidden, "This account has been deactivated or deleted"
	case err != nil:
		logger.Warnf("Auth: could not verify user %d with SystemManager — rejecting", userID)
		return http.StatusServiceUnavailable, "The account could not be verified; please try again"
	}
	return 0, ""
}

// MaxRequestBytes bounds a request body; MaxCoverageRequestBytes the one
// exception, a scan coverage table, which lists every target a scanner has.
const (
	MaxRequestBytes         = 1 << 20  // 1 MiB
	MaxCoverageRequestBytes = 32 << 20 // 32 MiB
)

// BodyLimitMiddleware refuses a body over the limit with 413 when its length
// is declared, and cuts one off at the limit when it is not, so a single
// request cannot grow the service's memory without bound.
func BodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(MaxRequestBytes)
		if r.URL.Path == "/api/assets/scan-coverage" {
			limit = MaxCoverageRequestBytes
		}
		if r.ContentLength > limit {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// CORSMiddleware handles CORS headers
func CORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			for _, allowed := range allowedOrigins {
				if origin == allowed {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					break
				}
			}

			// The answer depends on the Origin, so a cache must not hand one
			// origin's answer to another.
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")

			// Handle preflight requests
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// LoggingMiddleware logs HTTP requests
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Debugf("%s %s %s", r.Method, r.RequestURI, r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}

// GetVisibleUserIDs returns user IDs whose data should be visible,
// resolving group membership via the SystemManager client.
func (h *Handler) GetVisibleUserIDs(r *http.Request) []int64 {
	userID, ok := GetUserID(r)
	if !ok {
		return nil
	}
	return h.VisibleUserIDsForUser(userID)
}

// VisibleUserIDsForUser resolves the set of user IDs whose data the given
// user may see (self + group members), via SystemManager. Split out from
// GetVisibleUserIDs so the WebSocket upgrade path — which authenticates from
// the token offered as its subprotocol rather than through AuthMiddleware —
// can reuse it.
func (h *Handler) VisibleUserIDsForUser(userID int64) []int64 {
	if h.systemManagerClient != nil {
		if ids, err := h.systemManagerClient.GetGroupMemberIDs(userID); err == nil && len(ids) > 0 {
			return ids
		}
	}
	return []int64{userID}
}

// ParseJWT validates a session token against the configured secret and
// returns its claims: HS256 only, with an expiry, and a session — not one of
// the narrow tokens SystemManager mints for completing MFA, enrolling MFA or
// setting a first password, which carry a user id but stand in for no
// session. Used by AuthMiddleware and by the WebSocket upgrade, where the
// token arrives as the subprotocol.
func ParseJWT(cfg *config.Config, tokenString string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(cfg.Auth.JWTSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, errInvalidToken
	}
	claims, ok := token.Claims.(*JWTClaims)
	if !ok {
		return nil, errInvalidToken
	}
	if claims.MFAPending || claims.MFASetupRequired || claims.PasswordSetupRequired {
		return nil, errNotASession
	}
	return claims, nil
}

var errNotASession = errors.New("not a session token")
var errInvalidToken = errors.New("invalid or expired token")
