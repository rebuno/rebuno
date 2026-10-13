package kernel_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

// A bundle whose rule is missing an id fails to compile in NewRuleEngine.
const invalidBundle = `rules:
  - when:
      target: write
    then:
      decision: allow
`

const validBundle = `rules:
  - id: allow-write
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

func TestSetAgentMetadataRejectsInvalidSchema(t *testing.T) {
	k, ctx := setup(t)

	for _, schema := range []string{`{"type": 5}`, `{"$ref": "file:///etc/hostname"}`} {
		err := k.SetAgentMetadata(ctx, "agent-1", domain.AgentMetadata{InputSchema: json.RawMessage(schema)})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("schema %s: expected ErrValidation, got %v", schema, err)
		}
	}
}

func TestCreateExecutionValidatesInputSchema(t *testing.T) {
	k, ctx := setup(t)

	if err := k.SetAgentMetadata(ctx, "agent-1", domain.AgentMetadata{
		InputSchema: json.RawMessage(`{"type": "object", "required": ["topic"], "properties": {"topic": {"type": "string"}}}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{"topic": 1}`)); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
	if _, err := k.CreateExecution(ctx, "agent-1", nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected ErrValidation for missing input, got %v", err)
	}
	if _, err := k.CreateExecution(ctx, "agent-1", json.RawMessage(`{"topic": "go"}`)); err != nil {
		t.Fatalf("matching input should be accepted, got %v", err)
	}
}
