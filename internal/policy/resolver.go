package policy

import (
	"context"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

type BundleResolver struct {
	agents   store.AgentStore
	fallback Engine
	cache    *bundleCache
}

// Fail closed: a broken configuration must not silently weaken enforcement.
// The "bundle-error" RuleID separates these denials from rule-driven ones in
// the event log.
func bundleDeny(reason string) domain.PolicyResult {
	return domain.PolicyResult{Decision: domain.DecisionDeny, Reason: reason, RuleID: "bundle-error"}
}

func NewBundleResolver(agents store.AgentStore, fallback Engine) *BundleResolver {
	return &BundleResolver{
		agents:   agents,
		fallback: fallback,
		cache:    newBundleCache(defaultBundleCacheSize),
	}
}

func compileBundle(bundle string) (*RuleEngine, error) {
	cfg, err := LoadBundle(bundle)
	if err != nil {
		return nil, err
	}
	return NewRuleEngine(cfg)
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

	engine, err := r.cache.getOrCompile(input.AgentID, agent.PolicyBundle, compileBundle)
	if err != nil {
		return bundleDeny("policy_bundle_invalid"), nil
	}

	return engine.Evaluate(ctx, input)
}
