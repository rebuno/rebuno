package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
)

type agentLookup interface {
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
}

func bearerAuthMiddleware(token string, keys APIKeyKernel, scopes ...domain.Scope) func(http.Handler) http.Handler {
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
				if token != "" && subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) == 1 {
					ctx = auth.WithAdmin(ctx)
				} else {
					key, err := keys.AuthenticateAPIKey(ctx, supplied)
					if err != nil {
						WriteError(w, err)
						return
					}
					ctx = auth.WithClient(ctx, key.Scopes)
				}
			}
			for _, scope := range scopes {
				if !auth.HasScope(ctx, scope) {
					WriteError(w, domain.ErrForbidden)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// For routes an agent must reach without a bearer token, e.g. fetching input.
func bearerOrHMAC(token string, lookup AdminKernel) func(http.Handler) http.Handler {
	bearer := bearerAuthMiddleware(token, lookup, domain.ScopeExecutionsRead)
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

func hmacAuthMiddleware(lookup agentLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			agentID := r.Header.Get("Rebuno-Agent-Id")
			gotSig := r.Header.Get("Rebuno-Signature")
			if agentID == "" || gotSig == "" {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			agent, err := lookup.GetAgent(r.Context(), agentID)
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
