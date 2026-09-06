package payload

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/usage"
)

func Step(stepID string, kind domain.StepKind, target, ruleID string) map[string]any {
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

func StepResult(stepID string, kind domain.StepKind, target string, tokens usage.Tokens) map[string]any {
	payload := map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
		"target":    target,
	}
	if tokens.Found() {
		payload["usage"] = map[string]any{
			"input_tokens":  tokens.Input,
			"output_tokens": tokens.Output,
		}
	}
	return payload
}

func StepError(stepID string, kind domain.StepKind, target string, err []byte) map[string]any {
	m := map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
		"target":    target,
	}
	if len(err) > 0 {
		m["error"] = json.RawMessage(err)
	}
	return m
}

func StepDenied(stepID string, kind domain.StepKind, target, ruleID string, err []byte) map[string]any {
	m := map[string]any{
		"step_id":   stepID,
		"step_type": string(kind),
	}
	if target != "" {
		m["target"] = target
	}
	if ruleID != "" {
		m["rule_id"] = ruleID
	}
	if len(err) > 0 {
		m["error"] = json.RawMessage(err)
	}
	return m
}

func Execution(execID uuid.UUID, status domain.ExecutionStatus, output []byte, reason string) map[string]any {
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

func Approval(approvalID uuid.UUID, stepID string, execID uuid.UUID, status domain.ApprovalStatus, decidedBy, rationale string) map[string]any {
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

func Dispatch(dispatchID, execID uuid.UUID, status domain.DispatchStatus, attempt int) map[string]any {
	return map[string]any{
		"dispatch_id":  dispatchID.String(),
		"execution_id": execID.String(),
		"status":       string(status),
		"attempt":      attempt,
	}
}
