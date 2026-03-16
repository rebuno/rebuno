package api

import (
	"context"
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

func writeSSEEvent(w http.ResponseWriter, eventType string, data []byte, ids ...string) error {
	if len(ids) > 0 && ids[0] != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", ids[0]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "\n")
	return err
}

const heartbeatInterval = 5 * time.Second

func initSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, domain.CodeInternalError, "streaming not supported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprintf(w, "retry: 5000\n\n")
	flusher.Flush()
	return flusher, true
}

type sseHandlers struct {
	hub    *hub.Hub
	kernel *kernel.Kernel
	logger *slog.Logger
}

func (h *sseHandlers) connect(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	consumerID := r.URL.Query().Get("consumer_id")

	if agentID == "" || consumerID == "" {
		writeErrorFromErr(w, fmt.Errorf("%w: agent_id and consumer_id query params are required", domain.ErrValidation))
		return
	}
	if err := validateStringLength("agent_id", agentID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}
	if err := validateStringLength("consumer_id", consumerID, 256); err != nil {
		writeErrorFromErr(w, err)
		return
	}

	flusher, ok := initSSE(w)
	if !ok {
		return
	}

	conn := h.hub.Register(agentID, consumerID)
	connGen := conn.Generation()
	defer func() {
		sessionID := h.hub.GetSessionID(agentID, consumerID, connGen)
		h.hub.Unregister(agentID, consumerID, connGen)
		if sessionID != "" {
			disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer disconnectCancel()
			h.kernel.HandleAgentDisconnect(disconnectCtx, sessionID)
		}
	}()

	h.logger.Info("SSE connection established",
		slog.String("agent_id", agentID),
		slog.String("consumer_id", consumerID),
	)

	h.kernel.AssignPendingExecutions(r.Context(), agentID)

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			h.logger.Info("SSE connection closed (client disconnected)",
				slog.String("agent_id", agentID),
				slog.String("consumer_id", consumerID),
			)
			return

		case msg, ok := <-conn.EventCh:
			if !ok {
				return
			}

			data, err := json.Marshal(msg.Payload)
			if err != nil {
				h.logger.Warn("failed to marshal SSE payload",
					slog.String("error", err.Error()),
				)
				continue
			}

			if err := writeSSEEvent(w, msg.Type, data); err != nil {
				h.logger.Info("SSE write failed, closing connection",
					slog.String("agent_id", agentID),
					slog.String("consumer_id", consumerID),
				)
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ":heartbeat\n\n"); err != nil {
				h.logger.Info("SSE heartbeat write failed, closing connection",
					slog.String("agent_id", agentID),
					slog.String("consumer_id", consumerID),
				)
				return
			}
			flusher.Flush()
		}
	}
}
