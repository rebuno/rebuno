package api

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

type KernelAPI struct {
	Inner *kernel.Kernel
}

var _ ClientKernel = (*KernelAPI)(nil)
var _ AgentKernel = (*KernelAPI)(nil)
var _ AdminKernel = (*KernelAPI)(nil)

func (k *KernelAPI) CreateExecution(ctx context.Context, agentID string, input json.RawMessage, version string) (domain.Execution, error) {
	return k.Inner.CreateExecution(ctx, agentID, input, version)
}

func (k *KernelAPI) GetExecution(ctx context.Context, id uuid.UUID) (domain.Execution, error) {
	return k.Inner.GetExecution(ctx, id)
}

func (k *KernelAPI) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) (domain.ExecutionPage, error) {
	return k.Inner.ListExecutions(ctx, filter)
}

func (k *KernelAPI) GetEvents(ctx context.Context, id uuid.UUID, afterSeq int64, limit int) ([]domain.Event, error) {
	return k.Inner.GetEvents(ctx, id, afterSeq, limit)
}

func (k *KernelAPI) CancelExecution(ctx context.Context, id uuid.UUID) error {
	return k.Inner.CancelExecution(ctx, id)
}

func (k *KernelAPI) GetStep(ctx context.Context, stepID string) (domain.Step, error) {
	return k.Inner.GetStep(ctx, stepID)
}

func (k *KernelAPI) ListSteps(ctx context.Context, execID uuid.UUID) ([]domain.Step, error) {
	return k.Inner.ListSteps(ctx, execID)
}

func (k *KernelAPI) SubmitStep(ctx context.Context, execID uuid.UUID, req kernel.SubmitStepRequest) (domain.StepDecision, error) {
	return k.Inner.SubmitStep(ctx, execID, req)
}

func (k *KernelAPI) CompleteStep(ctx context.Context, stepID string, req kernel.CompleteStepRequest) (domain.StepDecision, error) {
	return k.Inner.CompleteStep(ctx, stepID, req)
}

func (k *KernelAPI) FailStep(ctx context.Context, stepID string, req kernel.FailStepRequest) (domain.StepDecision, error) {
	return k.Inner.FailStep(ctx, stepID, req)
}

func (k *KernelAPI) Heartbeat(ctx context.Context, execID uuid.UUID) error {
	return k.Inner.Heartbeat(ctx, execID)
}

func (k *KernelAPI) CompleteExecution(ctx context.Context, execID uuid.UUID, output json.RawMessage) error {
	return k.Inner.CompleteExecution(ctx, execID, output)
}

func (k *KernelAPI) FailExecution(ctx context.Context, execID uuid.UUID, reason string) error {
	return k.Inner.FailExecution(ctx, execID, reason)
}

func (k *KernelAPI) Register(ctx context.Context, agent domain.Agent) error {
	return k.Inner.RegisterAgent(ctx, agent)
}

func (k *KernelAPI) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return k.Inner.GetAgent(ctx, id)
}

func (k *KernelAPI) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	return k.Inner.ListAgents(ctx)
}

func (k *KernelAPI) DeleteAgent(ctx context.Context, id string) error {
	return k.Inner.DeleteAgent(ctx, id)
}

func (k *KernelAPI) LoadPolicyBundle(ctx context.Context, agentID string, bundle string) error {
	return k.Inner.LoadPolicyBundle(ctx, agentID, bundle)
}

func (k *KernelAPI) ListPendingApprovals(ctx context.Context) ([]domain.Approval, error) {
	return k.Inner.ListPendingApprovals(ctx)
}

func (k *KernelAPI) GetApproval(ctx context.Context, id uuid.UUID) (domain.Approval, error) {
	return k.Inner.GetApproval(ctx, id)
}

func (k *KernelAPI) GrantApproval(ctx context.Context, id uuid.UUID, req kernel.GrantApprovalRequest) error {
	return k.Inner.GrantApproval(ctx, id, req)
}

func (k *KernelAPI) DenyApproval(ctx context.Context, id uuid.UUID, req kernel.DenyApprovalRequest) error {
	return k.Inner.DenyApproval(ctx, id, req)
}
