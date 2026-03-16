package policy

import (
	"context"
	"log/slog"

	"github.com/rebuno/rebuno/internal/domain"
)

type PermissiveEngine struct {
	logger *slog.Logger
}

func NewPermissiveEngine(logger *slog.Logger) *PermissiveEngine {
	return &PermissiveEngine{logger: logger}
}

func (e *PermissiveEngine) Evaluate(_ context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	e.logger.Warn("no policy loaded — tool allowed by default",
		"tool_id", input.ToolID,
		"agent_id", input.AgentID,
		"execution_id", input.ExecutionID,
	)
	return domain.PolicyResult{
		Decision: domain.PolicyAllow,
		Reason:   "no policy configured (dev mode)",
		RuleID:   "permissive-default",
	}, nil
}
