package policy

import (
	"context"

	"github.com/rebuno/kernel/internal/domain"
	"github.com/rebuno/kernel/internal/store"
)

// BundleResolver selects the right policy engine for an agent by loading the
// agent's registered policy bundle. An agent with no bundle is unrestricted by
// design and defers to the configured fallback engine. Any other failure —
// the agent lookup erroring, or a bundle that won't parse or compile — fails
// closed with a deny, so a broken configuration can never silently weaken
// enforcement.
//
// Compiled engines are memoized per agent (keyed on bundle content), so the
// common path avoids re-parsing YAML and recompiling regexes on every step.
type BundleResolver struct {
	agents   store.AgentStore
	fallback Engine
	cache    *bundleCache
}

// bundleDeny is the fail-closed result returned when a bundle is present but
// unusable. RuleID "bundle-error" lets the denial be told apart in the event
// log from a rule-driven deny.
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

// compileBundle parses and compiles a raw bundle into a RuleEngine.
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
		// The store is unavailable (or the agent is gone); the rest of the
		// step path needs that same store, so allowing the step buys nothing.
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
