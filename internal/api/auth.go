package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
)

type agentLookup interface {
	LookupAgent(ctx context.Context, id string) (domain.Agent, error)
}

type deniedLogger interface {
	LogDenied(ctx context.Context, method, path string, status int)
}

func logDenials(l deniedLogger, authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			ctx := r.Context()
			authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx = r.Context()
				next.ServeHTTP(w, r)
			})).ServeHTTP(ww, r)
			if s := ww.Status(); s == http.StatusUnauthorized || s == http.StatusForbidden {
				l.LogDenied(ctx, r.Method, r.URL.Path, s)
			}
		})
	}
}

func bearerAuthMiddleware(token string, keys interface {
	APIKeyKernel
	deniedLogger
}, oidc *auth.OIDC, scopes ...domain.Scope) func(http.Handler) http.Handler {
	authenticate := logDenials(keys, bearerAuthentication(token, keys, oidc))
	return func(next http.Handler) http.Handler {
		return authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, scope := range scopes {
				if !auth.HasScope(r.Context(), scope) {
					WriteError(w, domain.ErrForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func bearerAuthentication(token string, keys APIKeyKernel, oidc *auth.OIDC) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			ctx := r.Context()
			if token == "" && header == "" {
				ctx = auth.WithAdmin(ctx)
			} else {
				if !strings.HasPrefix(header, "Bearer ") {
					WriteError(w, domain.ErrUnauthorized)
					return
				}
				supplied := strings.TrimPrefix(header, "Bearer ")
				switch {
				case token != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1:
					ctx = auth.WithAdmin(ctx)
				case oidc != nil && !strings.HasPrefix(supplied, "rbk_"):
					userCtx, err := oidc.Authenticate(ctx, supplied)
					if err != nil {
						WriteError(w, err)
						return
					}
					ctx = userCtx
				default:
					key, err := keys.AuthenticateAPIKey(ctx, supplied)
					if err != nil {
						WriteError(w, err)
						return
					}
					ctx = auth.WithClient(ctx, key.ID, key.Scopes)
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// For routes an agent must reach without a bearer token, e.g. fetching input.
func bearerOrHMAC(token string, lookup AdminKernel, oidc *auth.OIDC) func(http.Handler) http.Handler {
	bearer := bearerAuthMiddleware(token, lookup, oidc, domain.ScopeExecutionsRead)
	hmacMW := hmacAuthMiddleware(lookup)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Rebuno-Agent-Id") != "" || r.Header.Get("Rebuno-Signature") != "" {
				hmacMW(next).ServeHTTP(w, r)
				return
			}
			bearer(next).ServeHTTP(w, r)
		})
	}
}

func hmacAuthMiddleware(lookup interface {
	agentLookup
	deniedLogger
}) func(http.Handler) http.Handler {
	return logDenials(lookup, hmacAuthentication(lookup))
}

func hmacAuthentication(lookup agentLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			agentID := r.Header.Get("Rebuno-Agent-Id")
			gotSig := r.Header.Get("Rebuno-Signature")
			if agentID == "" || gotSig == "" {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			agent, err := lookup.LookupAgent(r.Context(), agentID)
			if err != nil {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if !auth.VerifyRequest(agent.Secret, r, body, time.Now()) {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithAgent(r.Context(), agent.ID)))
		})
	}
}
