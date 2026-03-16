package policy

import (
	"context"
	"log/slog"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestPermissiveEngine_Evaluate_AlwaysAllows(t *testing.T) {
	logger := slog.Default()
	engine := NewPermissiveEngine(logger)

	input := domain.PolicyInput{
		Action:      "tool.invoke",
		ToolID:      "some-tool",
		AgentID:     "agent-123",
		ExecutionID: "exec-456",
	}

	result, err := engine.Evaluate(context.Background(), input)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected decision %q, got %q", domain.PolicyAllow, result.Decision)
	}
}
