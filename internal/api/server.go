package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
)

type Server struct {
	router *chi.Mux
	kernel *kernel.Kernel
	pool   *pgxpool.Pool
	logger *slog.Logger
	server *http.Server
}

type ServerDeps struct {
	Kernel      *kernel.Kernel
	Pool        *pgxpool.Pool
	Hub         *hub.Hub
	RunnerHub   *hub.RunnerHub
	Logger      *slog.Logger
	BearerToken string
	CORSOrigins string
}

func NewServer(deps ServerDeps) *Server {
	s := &Server{
		router: chi.NewRouter(),
		kernel: deps.Kernel,
		pool:   deps.Pool,
		logger: deps.Logger,
	}
	s.routes(deps.Hub, deps.RunnerHub, deps.BearerToken, deps.CORSOrigins)
	return s
}

func (s *Server) routes(h *hub.Hub, rh *hub.RunnerHub, bearerToken string, corsOrigins string) {
	r := s.router

	r.Use(requestIDMiddleware)
	r.Use(loggingMiddleware(s.logger))
	r.Use(recoveryMiddleware(s.logger))
	r.Use(bodySizeLimitMiddleware)
	if corsOrigins != "" {
		r.Use(corsMiddleware(corsOrigins))
	}

	r.Get("/v0/health", handleHealth)
	r.Get("/v0/ready", handleReady(s.pool))

	exec := &executionHandlers{kernel: s.kernel}
	agent := &agentHandlers{kernel: s.kernel}
	runner := &runnerHandlers{kernel: s.kernel, hub: rh}
	sse := &sseHandlers{hub: h, kernel: s.kernel, logger: s.logger}
	runnerSSE := &runnerSSEHandlers{hub: rh, kernel: s.kernel, logger: s.logger}

	r.Group(func(r chi.Router) {
		if bearerToken != "" {
			r.Use(bearerAuthMiddleware(bearerToken))
		}

		r.Handle("/metrics", promhttp.Handler())

		r.Route("/v0/executions", func(r chi.Router) {
			r.Post("/", exec.create)
			r.Get("/", exec.list)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", exec.get)
				r.Post("/cancel", exec.cancel)
				r.Post("/signal", exec.sendSignal)
				r.Get("/events", exec.getEvents)
				r.Get("/stream", exec.streamEvents)
			})
		})

		r.Route("/v0/agents", func(r chi.Router) {
			r.Get("/stream", sse.connect)
			r.Post("/intent", agent.submitIntent)
			r.Post("/step-result", agent.stepResult)
		})

		r.Route("/v0/runners", func(r chi.Router) {
			r.Get("/stream", runnerSSE.connect)
			r.Post("/steps/{stepId}/started", runner.stepStarted)
			r.Route("/{id}", func(r chi.Router) {
				r.Post("/results", runner.submitResult)
				r.Post("/capabilities", runner.updateCapabilities)
				r.Delete("/", runner.unregister)
			})
		})
	})
}

func (s *Server) newHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadTimeout:       60 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // SSE connections need unlimited write time
		IdleTimeout:       120 * time.Second,
	}
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) ListenAndServe(addr string) error {
	s.server = s.newHTTPServer(addr)
	s.logger.Info("server starting", "addr", addr)
	return s.server.ListenAndServe()
}

func (s *Server) ListenAndServeTLS(addr, certFile, keyFile string) error {
	s.server = s.newHTTPServer(addr)
	s.logger.Info("server starting with TLS", "addr", addr)
	return s.server.ListenAndServeTLS(certFile, keyFile)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	s.logger.Info("server shutting down")
	return s.server.Shutdown(ctx)
}

func corsMiddleware(origins string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool)
	for _, o := range strings.Split(origins, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			allowed[o] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(allowed) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			var allowOrigin string
			if allowed["*"] {
				allowOrigin = "*"
			} else if origin != "" && allowed[origin] {
				allowOrigin = origin
			}

			if allowOrigin != "" {
				w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
