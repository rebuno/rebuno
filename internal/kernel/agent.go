package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

const minLeaseTimeout = 10 * time.Second

func (k *Kernel) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	if err := validatePolicyBundle(agent.PolicyBundle); err != nil {
		return err
	}
	if agent.LeaseTimeoutSeconds < minLeaseTimeout.Seconds() {
		agent.LeaseTimeoutSeconds = 0
	}
	return k.d.Agents.RegisterAgent(ctx, agent)
}

func validatePolicyBundle(bundle string) error {
	if bundle == "" {
		return nil
	}
	if _, err := policy.NewRuleEngineFromBundle(bundle); err != nil {
		return fmt.Errorf("%w: invalid policy bundle: %v", domain.ErrValidation, err)
	}
	return nil
}

func (k *Kernel) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return k.d.Agents.GetAgent(ctx, id)
}

func (k *Kernel) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	return k.d.Agents.ListAgents(ctx)
}

func (k *Kernel) DeleteAgent(ctx context.Context, id string) error {
	return k.d.Agents.DeleteAgent(ctx, id)
}

func (k *Kernel) LoadPolicyBundle(ctx context.Context, agentID string, bundle string) error {
	if err := validatePolicyBundle(bundle); err != nil {
		return fmt.Errorf("load policy bundle: %w", err)
	}
	agent, err := k.d.Agents.GetAgent(ctx, agentID)
	if err != nil {
		return fmt.Errorf("load policy bundle: %w", err)
	}
	agent.PolicyBundle = bundle
	if err := k.d.Agents.RegisterAgent(ctx, agent); err != nil {
		return fmt.Errorf("load policy bundle: %w", err)
	}
	return nil
}
