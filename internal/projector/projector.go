package projector

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/store"
)

type State struct {
	Execution        domain.Execution
	Steps            []domain.Step
	PendingApprovals []domain.Approval
}

type Projector struct {
	steps store.StepStore
}

func New(steps store.StepStore) *Projector {
	return &Projector{steps: steps}
}

func (p *Projector) ExecutionState(ctx context.Context, exec domain.Execution) (State, error) {
	steps, err := p.steps.ListByExecution(ctx, exec.ID)
	if err != nil {
		return State{}, err
	}
	return State{Execution: exec, Steps: steps}, nil
}

func StepPayload(stepID string, kind domain.StepKind, target, ruleID string) map[string]any {
	m := map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
		"target":    target,
	}
	if ruleID != "" {
		m["rule_id"] = ruleID
	}
	return m
}

func StepResultPayload(stepID string, kind domain.StepKind) map[string]any {
	return map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
	}
}

func StepErrorPayload(stepID string, kind domain.StepKind, err []byte) map[string]any {
	m := map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
	}
	if len(err) > 0 {
		m["error"] = json.RawMessage(err)
	}
	return m
}

func ExecutionPayload(execID uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) map[string]any {
	m := map[string]any{
		"execution_id": execID.String(),
		"status":       string(status),
	}
	if len(output) > 0 {
		m["output"] = json.RawMessage(output)
	}
	if reason != "" {
		m["reason"] = reason
	}
	return m
}

func ApprovalPayload(approvalID uuid.UUID, stepID string, execID uuid.UUID, status domain.ApprovalStatus, decidedBy, rationale string) map[string]any {
	m := map[string]any{
		"approval_id":  approvalID.String(),
		"step_id":      stepID,
		"execution_id": execID.String(),
		"status":       string(status),
	}
	if decidedBy != "" {
		m["decided_by"] = decidedBy
	}
	if rationale != "" {
		m["rationale"] = rationale
	}
	return m
}

func DispatchPayload(dispatchID, execID uuid.UUID, status domain.DispatchStatus, attempt int) map[string]any {
	return map[string]any{
		"dispatch_id":  dispatchID.String(),
		"execution_id": execID.String(),
		"status":       string(status),
		"attempt":      attempt,
	}
}
