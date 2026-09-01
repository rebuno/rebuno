package kernel

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

const decisionEventPage = 500

type PolicyTestRequest struct {
	Bundle      string        `json:"bundle,omitempty"`       // empty: the agent's stored bundle
	Cases       []policy.Case `json:"cases,omitempty"`        // ignored when ExecutionID is set
	ExecutionID *uuid.UUID    `json:"execution_id,omitempty"` // replay this execution's steps
}

func (k *Kernel) TestPolicy(ctx context.Context, agentID string, req PolicyTestRequest) (policy.Report, error) {
	agent, err := k.d.Agents.GetAgent(ctx, agentID)
	if err != nil {
		return policy.Report{}, fmt.Errorf("test policy: %w", err)
	}
	bundle := req.Bundle
	if bundle == "" {
		if agent.PolicyBundle == "" {
			return policy.Report{}, fmt.Errorf("%w: agent %q has no policy bundle", domain.ErrValidation, agentID)
		}
		bundle = agent.PolicyBundle
	}
	engine, err := policy.NewRuleEngineFromBundle(bundle)
	if err != nil {
		return policy.Report{}, fmt.Errorf("%w: invalid policy bundle: %v", domain.ErrValidation, err)
	}

	cases := req.Cases
	if req.ExecutionID != nil {
		cases, err = k.replayCases(ctx, *req.ExecutionID)
		if err != nil {
			return policy.Report{}, err
		}
	}
	if err := policy.NormalizeCases(cases, agentID); err != nil {
		return policy.Report{}, fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}
	return policy.Run(ctx, engine, cases)
}

func (k *Kernel) replayCases(ctx context.Context, execID uuid.UUID) ([]policy.Case, error) {
	if _, err := k.d.Executions.GetExecution(ctx, execID); err != nil {
		return nil, fmt.Errorf("test policy: %w", err)
	}
	steps, err := k.d.Steps.ListByExecution(ctx, execID)
	if err != nil {
		return nil, fmt.Errorf("test policy: %w", err)
	}
	decisions, err := k.decisionEvents(ctx, execID)
	if err != nil {
		return nil, fmt.Errorf("test policy: %w", err)
	}
	return policy.ReplayCases(steps, decisions), nil
}

func (k *Kernel) decisionEvents(ctx context.Context, execID uuid.UUID) ([]domain.Event, error) {
	var (
		out   []domain.Event
		after int64
	)
	for {
		page, err := k.d.Events.GetEvents(ctx, execID, after, decisionEventPage)
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			if policy.IsDecisionEvent(e.Type) {
				out = append(out, e)
			}
		}
		if len(page) < decisionEventPage {
			return out, nil
		}
		after = page[len(page)-1].EventSeq
	}
}
