package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

type fakeAgentStore struct {
	agent domain.Agent
	err   error
}

func (f fakeAgentStore) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	return f.agent, f.err
}
func (f fakeAgentStore) RegisterAgent(ctx context.Context, a domain.Agent) error { return nil }
func (f fakeAgentStore) ListAgents(ctx context.Context) ([]domain.Agent, error)  { return nil, nil }
func (f fakeAgentStore) DeleteAgent(ctx context.Context, id string) error        { return nil }

// A missing id fails in NewRuleEngine.
const uncompilableBundle = `rules:
  - when:
      target: write
    then:
      decision: allow
`

// A scalar where a list belongs fails in LoadBundle.
const unparsableBundle = `rules: "not-a-list"`

const allowWriteBundle = `rules:
  - id: allow-write
    when:
      target: write
    then:
      decision: allow
`

const denyWriteBundle = `rules:
  - id: deny-write
    when:
      target: write
    then:
      decision: deny
`

func TestBundleResolverFailsClosedOnGetAgentError(t *testing.T) {
	// Fallback would allow; a store error must still deny.
	r := NewBundleResolver(fakeAgentStore{err: errors.New("db down")}, PermissiveEngine{})
	res, err := r.Evaluate(context.Background(), domain.PolicyInput{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny {
		t.Fatalf("expected deny on store error, got %q", res.Decision)
	}
}

func TestBundleResolverFailsClosedOnUnparsableBundle(t *testing.T) {
	r := NewBundleResolver(fakeAgentStore{agent: domain.Agent{ID: "a", PolicyBundle: unparsableBundle}}, PermissiveEngine{})
	res, err := r.Evaluate(context.Background(), domain.PolicyInput{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny {
		t.Fatalf("expected deny on unparsable bundle, got %q", res.Decision)
	}
}

func TestBundleResolverFailsClosedOnUncompilableBundle(t *testing.T) {
	r := NewBundleResolver(fakeAgentStore{agent: domain.Agent{ID: "a", PolicyBundle: uncompilableBundle}}, PermissiveEngine{})
	res, err := r.Evaluate(context.Background(), domain.PolicyInput{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny {
		t.Fatalf("expected deny on uncompilable bundle, got %q", res.Decision)
	}
}

func TestBundleResolverEmptyBundleUsesFallback(t *testing.T) {
	r := NewBundleResolver(fakeAgentStore{agent: domain.Agent{ID: "a"}}, PermissiveEngine{})
	res, err := r.Evaluate(context.Background(), domain.PolicyInput{AgentID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionAllow {
		t.Fatalf("empty bundle should use fallback (allow), got %q", res.Decision)
	}
}

func TestBundleResolverPicksUpBundleChange(t *testing.T) {
	// Fallback denies, so the initial allow can only come from the bundle.
	fs := &fakeAgentStore{agent: domain.Agent{ID: "a", PolicyBundle: allowWriteBundle}}
	r := NewBundleResolver(fs, DenyAllEngine{})
	in := domain.PolicyInput{AgentID: "a", Target: "write"}

	res, err := r.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionAllow {
		t.Fatalf("expected allow from initial bundle, got %q", res.Decision)
	}

	fs.agent.PolicyBundle = denyWriteBundle
	res, err = r.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny {
		t.Fatalf("expected deny after bundle change, got %q", res.Decision)
	}
}

func TestBundleResolverValidBundleEvaluates(t *testing.T) {
	// Fallback denies, so an allow proves the agent's own bundle was evaluated.
	r := NewBundleResolver(fakeAgentStore{agent: domain.Agent{ID: "a", PolicyBundle: allowWriteBundle}}, DenyAllEngine{})
	res, err := r.Evaluate(context.Background(), domain.PolicyInput{AgentID: "a", Target: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionAllow {
		t.Fatalf("valid bundle should evaluate to allow, got %q", res.Decision)
	}
}
