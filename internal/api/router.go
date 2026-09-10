package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/observe"
)

const maxRequestBodyBytes = 10 << 20 // 10 MiB

// Global, not per-handler: the HMAC middleware io.ReadAlls the body first.
func bodyLimit(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

type Router struct {
	client ClientKernel
	agent  AgentKernel
	admin  AdminKernel
	stream Streamer
	auth   string
}

func NewRouter(client ClientKernel, agent AgentKernel, admin AdminKernel, authTok string, stream Streamer, ready func(context.Context) error, observer ...*observe.Observer) http.Handler {
	r := &Router{client: client, agent: agent, admin: admin, stream: stream, auth: authTok}
	obs := observe.Default()
	if len(observer) > 0 && observer[0] != nil {
		obs = observer[0]
	}
	mux := chi.NewRouter()

	mux.Use(obs.MetricsMiddleware())
	mux.Use(middleware.RequestID)
	mux.Use(middleware.Recoverer)
	mux.Use(bodyLimit(maxRequestBodyBytes))

	bearer := func(scopes ...domain.Scope) func(http.Handler) http.Handler {
		return bearerAuthMiddleware(authTok, admin, scopes...)
	}
	hmac := hmacAuthMiddleware(admin)
	dual := bearerOrHMAC(authTok, admin)

	mux.Get("/metrics", promhttp.HandlerFor(obs.Registry(), promhttp.HandlerOpts{}).ServeHTTP)

	mux.With(bearer(domain.ScopeExecutionsWrite)).Post("/v0/executions", r.createExecution)
	mux.With(bearer(domain.ScopeExecutionsRead)).Get("/v0/executions", r.listExecutions)
	mux.With(dual).Get("/v0/executions/{id}", r.getExecution)
	mux.With(dual).Get("/v0/executions/{id}/events", r.getEvents)
	mux.With(bearer(domain.ScopeExecutionsRead)).Get("/v0/executions/{id}/stream", r.streamExecution)
	mux.With(bearer(domain.ScopeExecutionsWrite)).Post("/v0/executions/{id}/cancel", r.cancelExecution)

	mux.With(dual).Get("/v0/executions/{id}/steps", r.listSteps)
	mux.With(dual).Get("/v0/executions/{id}/steps/{step_id}", r.getStep)
	mux.With(hmac).Post("/v0/executions/{id}/steps", r.submitStep)
	mux.With(hmac).Post("/v0/executions/{id}/steps/{step_id}/complete", r.completeStep)
	mux.With(hmac).Post("/v0/executions/{id}/steps/{step_id}/fail", r.failStep)
	mux.With(hmac).Post("/v0/executions/{id}/steps/{step_id}/stream", r.streamStepDelta)
	mux.With(hmac).Post("/v0/executions/{id}/heartbeat", r.heartbeat)
	mux.With(hmac).Post("/v0/executions/{id}/complete", r.agentCompleteExecution)
	mux.With(hmac).Post("/v0/executions/{id}/fail", r.agentFailExecution)

	mux.With(bearer(domain.ScopeAgentsWrite)).Post("/v0/agents", r.registerAgent)
	mux.With(bearer(domain.ScopeAgentsRead)).Get("/v0/agents", r.listAgents)
	mux.With(bearer(domain.ScopeAgentsRead)).Get("/v0/agents/{id}", r.getAgent)
	mux.With(bearer(domain.ScopeAgentsWrite)).Delete("/v0/agents/{id}", r.deleteAgent)
	mux.With(bearer(domain.ScopePoliciesWrite)).Post("/v0/policies/{agent_id}", r.loadPolicy)
	mux.With(bearer(domain.ScopePoliciesWrite, domain.ScopeExecutionsRead)).Post("/v0/policies/{agent_id}/test", r.testPolicy)
	mux.With(bearer(domain.ScopeApprovalsRead)).Get("/v0/approvals", r.listApprovals)
	mux.With(bearer(domain.ScopeApprovalsRead)).Get("/v0/approvals/{id}", r.getApproval)
	mux.With(bearer(domain.ScopeApprovalsWrite)).Post("/v0/approvals/{id}/grant", r.grantApproval)
	mux.With(bearer(domain.ScopeApprovalsWrite)).Post("/v0/approvals/{id}/deny", r.denyApproval)

	mux.With(bearer(domain.ScopeAPIKeysManage)).Post("/v0/api-keys", r.createAPIKey)
	mux.With(bearer(domain.ScopeAPIKeysManage)).Get("/v0/api-keys", r.listAPIKeys)
	mux.With(bearer(domain.ScopeAPIKeysManage)).Post("/v0/api-keys/{id}/rotate", r.rotateAPIKey)
	mux.With(bearer(domain.ScopeAPIKeysManage)).Delete("/v0/api-keys/{id}", r.revokeAPIKey)

	mux.Get("/v0/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, map[string]string{"status": "ok"}, http.StatusOK)
	})
	mux.Get("/v0/ready", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				WriteJSON(w, map[string]string{"status": "unavailable"}, http.StatusServiceUnavailable)
				return
			}
		}
		WriteJSON(w, map[string]string{"status": "ready"}, http.StatusOK)
	})

	return mux
}
