package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/hub"
	"github.com/rebuno/rebuno/internal/kernel"
)

type runnerSSEHandlers struct {
	hub    *hub.RunnerHub
	kernel *kernel.Kernel
	logger *slog.Logger
}

func (h *runnerSSEHandlers) connect(w http.ResponseWriter, r *http.Request) {
	runnerID := r.URL.Query().Get("runner_id")
	consumerID := r.URL.Query().Get("consumer_id")
	capStr := r.URL.Query().Get("capabilities")

	if runnerID == "" || consumerID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: runner_id and consumer_id query params are required", domain.ErrValidation))
		return
	}
	if err := validateStringLength("runner_id", runnerID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if err := validateStringLength("consumer_id", consumerID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}

	var capabilities []string
	if capStr != "" {
		for _, c := range strings.Split(capStr, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				capabilities = append(capabilities, c)
			}
		}
	}

	flusher, ok := initSSE(w)
	if !ok {
		return
	}

	if err := h.kernel.RegisterRunner(r.Context(), domain.Runner{
		ID:            runnerID,
		Name:          runnerID,
		Capabilities:  capabilities,
		Status:        domain.RunnerStatusOnline,
		LastHeartbeat: time.Now(),
		RegisteredAt:  time.Now(),
	}); err != nil {
		h.logger.Warn("failed to persist runner metadata",
			slog.String("runner_id", runnerID),
			slog.String("error", err.Error()),
		)
	}

	conn := h.hub.Register(runnerID, consumerID, capabilities)
	connGen := conn.Generation()
	defer h.hub.Unregister(runnerID, consumerID, connGen)

	h.logger.Info("runner SSE connection established",
		slog.String("runner_id", runnerID),
		slog.String("consumer_id", consumerID),
		slog.Int("capabilities", len(capabilities)),
	)

	h.kernel.DispatchPendingJobs()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			h.logger.Info("runner SSE connection closed (client disconnected)",
				slog.String("runner_id", runnerID),
				slog.String("consumer_id", consumerID),
			)
			return

		case msg, ok := <-conn.EventCh:
			if !ok {
				return
			}

			data, err := json.Marshal(msg.Payload)
			if err != nil {
				h.logger.Warn("failed to marshal runner SSE payload",
					slog.String("error", err.Error()),
				)
				continue
			}

			if err := writeSSEEvent(w, msg.Type, data); err != nil {
				h.logger.Info("runner SSE write failed, closing connection",
					slog.String("runner_id", runnerID),
					slog.String("consumer_id", consumerID),
				)
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ":heartbeat\n\n"); err != nil {
				h.logger.Info("runner SSE heartbeat write failed, closing connection",
					slog.String("runner_id", runnerID),
					slog.String("consumer_id", consumerID),
				)
				return
			}
			flusher.Flush()
		}
	}
}
