package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func TestOneActiveExecutionPerConcurrencyKey(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	s := NewStore(pool)
	agentID := "key-test-agent-" + uuid.NewString()
	if err := s.RegisterAgent(ctx, domain.Agent{ID: agentID, WebhookURL: "http://localhost/wh", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	first := domain.Execution{
		ID: uuid.Must(uuid.NewV7()), AgentID: agentID, Input: json.RawMessage(`{}`),
		Status: domain.ExecutionPending, ConcurrencyKey: uuid.NewString(),
	}
	second := first
	second.ID = uuid.Must(uuid.NewV7())
	for _, exec := range []domain.Execution{first, second} {
		if err := s.CreateExecution(ctx, exec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpdateExecutionStatus(ctx, first.ID, domain.ExecutionBlocked, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateExecutionStatus(ctx, second.ID, domain.ExecutionRunning, nil, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second owner of the key: %v", err)
	}
	if err := s.UpdateExecutionStatus(ctx, first.ID, domain.ExecutionCompleted, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateExecutionStatus(ctx, second.ID, domain.ExecutionRunning, nil, ""); err != nil {
		t.Fatal(err)
	}
}
