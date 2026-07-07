package kernel_test

import (
	"errors"
	"testing"

	"github.com/rebuno/kernel/internal/domain"
)

// A bundle whose rule is missing an id fails to compile in NewRuleEngine.
const invalidBundle = `rules:
  - priority: 1
    when:
      target: write
    then:
      decision: allow
`

const validBundle = `rules:
  - id: allow-write
    priority: 1
    when:
      target: write
    then:
      decision: allow
`

func TestLoadPolicyBundleRejectsInvalid(t *testing.T) {
	k, ctx := setup(t)

	err := k.LoadPolicyBundle(ctx, "agent-1", invalidBundle)
	if err == nil {
		t.Fatal("expected error loading an uncompilable bundle")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}

	// A rejected bundle must not be persisted, so enforcement is never weakened.
	agent, gerr := k.GetAgent(ctx, "agent-1")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if agent.PolicyBundle != "" {
		t.Fatalf("invalid bundle should not be persisted, got %q", agent.PolicyBundle)
	}
}

func TestLoadPolicyBundleAcceptsValid(t *testing.T) {
	k, ctx := setup(t)

	if err := k.LoadPolicyBundle(ctx, "agent-1", validBundle); err != nil {
		t.Fatalf("valid bundle should load, got %v", err)
	}
	agent, err := k.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if agent.PolicyBundle != validBundle {
		t.Fatal("valid bundle should be persisted")
	}
}

func TestRegisterAgentRejectsInvalidBundle(t *testing.T) {
	k, ctx := setup(t)

	err := k.RegisterAgent(ctx, domain.Agent{
		ID:           "agent-2",
		WebhookURL:   "http://localhost",
		Secret:       "secret",
		PolicyBundle: invalidBundle,
	})
	if err == nil {
		t.Fatal("expected error registering agent with an uncompilable bundle")
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}

	if _, gerr := k.GetAgent(ctx, "agent-2"); gerr == nil {
		t.Fatal("agent with invalid bundle should not be persisted")
	}
}
