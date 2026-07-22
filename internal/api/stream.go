package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/stream"
)

const maxDeltaBytes = 7000

const streamKeepAlive = 15 * time.Second

// Streamer is the live side channel the router needs: publish agent deltas and
// subscribe SSE clients. *stream.Hub satisfies it. A nil Streamer disables the
// feature (endpoints degrade gracefully).
type Streamer interface {
	Publish(ctx context.Context, execID uuid.UUID, d stream.Delta) error
	Subscribe(execID uuid.UUID) (<-chan stream.Delta, func())
}

type StreamDeltaRequest struct {
	Seq  int64  `json:"seq"`
	Data string `json:"data"`
}

func (rt *Router) streamStepDelta(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	stepID := chi.URLParam(r, "step_id")
	var req StreamDeltaRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if len(req.Data) > maxDeltaBytes {
		WriteError(w, domain.ErrValidation)
		return
	}
	if rt.stream != nil {
		_ = rt.stream.Publish(r.Context(), id, stream.Delta{StepID: stepID, Seq: req.Seq, Data: req.Data})
	}
	WriteNoContent(w)
}

func (rt *Router) streamExecution(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, domain.ErrValidation)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok || rt.stream == nil {
		WriteError(w, domain.ErrValidation)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := rt.stream.Subscribe(id)
	defer cancel()

	ka := time.NewTicker(streamKeepAlive)
	defer ka.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ka.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case d := <-ch:
			payload, err := json.Marshal(d)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if _, err := w.Write(payload); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
