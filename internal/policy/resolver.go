package policy

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type BundleResolver struct {
	agents   store.AgentStore
	fallback Engine
	judge    *Judge
	cache    *bundleCache
}

// Fail closed: a broken configuration must not silently weaken enforcement.
// The "bundle-error" RuleID separates these denials from rule-driven ones in
// the event log.
func bundleDeny(reason string) domain.PolicyResult {
	return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: reason, RuleID: "bundle-error"}
}

func NewBundleResolver(agents store.AgentStore, fallback Engine, judge *Judge) *BundleResolver {
	return &BundleResolver{
		agents:   agents,
		fallback: fallback,
		judge:    judge,
		cache:    newBundleCache(defaultBundleCacheSize),
	}
}

func (r *BundleResolver) compile(bundle string) (*RuleEngine, error) {
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		return nil, err
	}
	engine.Judge = r.judge
	return engine, nil
}

func (r *BundleResolver) Evaluate(ctx context.Context, input domain.PolicyInput) (domain.PolicyResult, error) {
	if r.fallback == nil {
		r.fallback = PermissiveEngine{}
	}

	agent, err := r.agents.GetAgent(ctx, input.AgentID)
	if err != nil {
		return bundleDeny("agent_lookup_failed"), nil
	}
	if agent.PolicyBundle == "" {
		return r.fallback.Evaluate(ctx, input)
	}

	engine, err := r.cache.getOrCompile(input.AgentID, agent.PolicyBundle, r.compile)
	if err != nil {
		return bundleDeny("policy_bundle_invalid"), nil
	}

	return engine.Evaluate(ctx, input)
}
