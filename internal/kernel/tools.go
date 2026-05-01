package kernel

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

type toolDirectory struct {
	mu     sync.RWMutex
	byPair map[string]map[string]domain.ToolSchema // tool_id -> runner_id -> schema
	logger *slog.Logger
}

func newToolDirectory(logger *slog.Logger) *toolDirectory {
	if logger == nil {
		logger = slog.Default()
	}
	return &toolDirectory{
		byPair: make(map[string]map[string]domain.ToolSchema),
		logger: logger,
	}
}

func (d *toolDirectory) Publish(runnerID string, schemas []domain.ToolSchema) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for toolID, perRunner := range d.byPair {
		delete(perRunner, runnerID)
		if len(perRunner) == 0 {
			delete(d.byPair, toolID)
		}
	}

	now := time.Now()
	for _, s := range schemas {
		s.RunnerID = runnerID
		s.RegisteredAt = now
		if d.byPair[s.ID] == nil {
			d.byPair[s.ID] = make(map[string]domain.ToolSchema)
		}
		d.byPair[s.ID][runnerID] = s
	}
}

func (d *toolDirectory) DropRunner(runnerID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for toolID, perRunner := range d.byPair {
		delete(perRunner, runnerID)
		if len(perRunner) == 0 {
			delete(d.byPair, toolID)
		}
	}
}

func (d *toolDirectory) List(prefix string) []domain.ToolSchema {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]domain.ToolSchema, 0, len(d.byPair))
	for toolID, perRunner := range d.byPair {
		if !matchPrefix(toolID, prefix) {
			continue
		}
		out = append(out, d.pickWinner(toolID, perRunner))
	}
	return out
}

func (d *toolDirectory) pickWinner(toolID string, perRunner map[string]domain.ToolSchema) domain.ToolSchema {
	var winner domain.ToolSchema
	schemas := make([]domain.ToolSchema, 0, len(perRunner))
	for _, s := range perRunner {
		schemas = append(schemas, s)
		if s.RegisteredAt.After(winner.RegisteredAt) {
			winner = s
		}
	}
	if len(schemas) > 1 && !schemasIdentical(schemas) {
		runnerIDs := make([]string, 0, len(schemas))
		for _, s := range schemas {
			runnerIDs = append(runnerIDs, s.RunnerID)
		}
		d.logger.Warn(
			"tool has divergent schemas across runners; using newest",
			slog.String("tool_id", toolID),
			slog.Any("runners", runnerIDs),
			slog.String("winner", winner.RunnerID),
		)
	}
	return winner
}

func matchPrefix(toolID, prefix string) bool {
	if prefix == "" {
		return true
	}
	return toolID == prefix || strings.HasPrefix(toolID, prefix+".")
}

func schemasIdentical(schemas []domain.ToolSchema) bool {
	first := schemas[0]
	for _, s := range schemas[1:] {
		if s.Description != first.Description {
			return false
		}
		if string(s.InputSchema) != string(first.InputSchema) {
			return false
		}
	}
	return true
}
