package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"strings"

	"github.com/rebuno/rebuno/internal/dispatcher"
	"github.com/rebuno/rebuno/internal/domain"
)

type agentLookup interface {
	GetAgent(ctx context.Context, id string) (domain.Agent, error)
}

func bearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			if !strings.EqualFold(strings.TrimPrefix(header, "Bearer "), token) {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerOrHMAC accepts a request authenticated either by bearer token (client)
// or by agent HMAC (Rebuno-Agent-Id + Rebuno-Signature). Used for routes an
// agent must reach without a bearer token, e.g. fetching execution input.
func bearerOrHMAC(token string, lookup agentLookup) func(http.Handler) http.Handler {
	bearer := bearerAuthMiddleware(token)
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

// hmacAuthMiddleware verifies inbound agent requests using the agent's
// webhook secret. Requests must provide Rebuno-Agent-Id and Rebuno-Signature
// headers; the signature is computed over the raw request body bytes using the
// same HMAC as outbound dispatch webhooks.
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
			wantSig := "sha256=" + dispatcher.SignPayload(agent.Secret, body)
			if subtle.ConstantTimeCompare([]byte(wantSig), []byte(gotSig)) != 1 {
				WriteError(w, domain.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
