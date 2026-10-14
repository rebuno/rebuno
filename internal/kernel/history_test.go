package kernel_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
)

func submitWithArgs(t *testing.T, k *kernel.Kernel, ctx context.Context, id uuid.UUID, target, args string) domain.StepDecision {
	t.Helper()
	dec, err := k.SubmitStep(ctx, id, kernel.SubmitStepRequest{
		Kind: domain.StepKindTool, Target: target, Args: json.RawMessage(args), Lease: leaseOf(t, k, id),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dec
}

func TestSessionHistoryPolicySpansExecutions(t *testing.T) {
	for _, backend := range []string{"memory", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			base, _, ctx := sessionKernels(t, backend)
			pe, err := policy.NewRuleEngineFromBundle(`
default_action: allow
rules:
  - id: no-email-after-fetch
    when:
      target: send_email
      history:
        - scope: session
          target: web_fetch
    then: { decision: deny }
`)
			if err != nil {
				t.Fatal(err)
			}
			deps := base.Deps()
			deps.Policy = pe
			k := kernel.New(kernel.DefaultConfig(), deps)

			first := createInSession(t, k, ctx, "agent-1", "session-a")
			second := createInSession(t, k, ctx, "agent-1", "session-a")
			other := createInSession(t, k, ctx, "agent-1", "session-b")
			drainSessions(t, k, ctx)

			if dec := submitWithArgs(t, k, ctx, first.ID, "send_email", `{"n":1}`); dec.Decision != "proceed" {
				t.Fatalf("email before fetch = %s, want proceed", dec.Decision)
			}
			fetch := submitWithArgs(t, k, ctx, first.ID, "web_fetch", `{}`)
			if fetch.Decision != "proceed" {
				t.Fatalf("fetch = %s, want proceed", fetch.Decision)
			}
			if dec := submitWithArgs(t, k, ctx, first.ID, "send_email", `{"n":2}`); dec.Decision != "denied" || dec.RuleID != "no-email-after-fetch" {
				t.Fatalf("email while fetch in flight = %+v, want denied by no-email-after-fetch", dec)
			}
			if _, err := k.CompleteStep(ctx, fetch.StepID, kernel.CompleteStepRequest{Result: json.RawMessage(`{}`), Lease: leaseOf(t, k, first.ID)}); err != nil {
				t.Fatal(err)
			}
			if err := k.CompleteExecution(ctx, first.ID, leaseOf(t, k, first.ID), json.RawMessage(`{}`), nil); err != nil {
				t.Fatal(err)
			}
			drainSessions(t, k, ctx)

			if dec := submitWithArgs(t, k, ctx, second.ID, "send_email", `{}`); dec.Decision != "denied" {
				t.Fatalf("email later in session = %s, want denied", dec.Decision)
			}
			if dec := submitWithArgs(t, k, ctx, other.ID, "send_email", `{}`); dec.Decision != "proceed" {
				t.Fatalf("email in another session = %s, want proceed", dec.Decision)
			}
		})
	}
}
