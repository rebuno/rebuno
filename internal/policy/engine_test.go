package policy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestArgPredicateRegex(t *testing.T) {
	bundle := `
rules:
  - id: allow-prod
    priority: 1
    when:
      arguments:
        env:
          regex: "^prod-.*"
    then:
      decision: allow
default_action: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"matching prod", "prod-123", domain.DecisionAllow},
		{"non-matching staging", "staging-123", domain.DecisionDeny},
		{"no prefix", "prod", domain.DecisionDeny},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"env": tc.env})
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Args: args})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, res.Decision)
			}
		})
	}
}

func TestThenApprovalConfigAndRateLimitFromBundle(t *testing.T) {
	bundle := `
default_action: deny
rules:
  - id: approve-writes
    priority: 10
    when:
      target: fs_write
    then:
      decision: require_approval
      reason: writes need a human
      approval_config:
        approvers: [alice, bob]
        timeout: 5m
        message: please review
  - id: limit-search
    priority: 20
    when:
      target: web_search
    then:
      decision: allow
      rate_limit:
        max_calls: 5
        window: 1m
        per_what: agent
        on_limiter_error: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("approval_config", func(t *testing.T) {
		res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Target: "fs_write"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != domain.DecisionRequireApproval {
			t.Fatalf("expected %s, got %s", domain.DecisionRequireApproval, res.Decision)
		}
		got := res.ApprovalConfig
		if len(got.Approvers) != 2 || got.Approvers[0] != "alice" || got.Approvers[1] != "bob" {
			t.Errorf("approvers: got %v, want [alice bob]", got.Approvers)
		}
		if got.Timeout != 5*time.Minute {
			t.Errorf("timeout: got %v, want 5m", got.Timeout)
		}
		if got.Message != "please review" {
			t.Errorf("message: got %q, want %q", got.Message, "please review")
		}
	})

	t.Run("rate_limit", func(t *testing.T) {
		res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Target: "web_search"})
		if err != nil {
			t.Fatal(err)
		}
		got := res.RateLimit
		if got.MaxCalls != 5 {
			t.Errorf("max_calls: got %d, want 5", got.MaxCalls)
		}
		if got.Window != time.Minute {
			t.Errorf("window: got %v, want 1m", got.Window)
		}
		if got.PerWhat != "agent" {
			t.Errorf("per_what: got %q, want agent", got.PerWhat)
		}
		if got.OnLimiterError != domain.LimiterErrorDeny {
			t.Errorf("on_limiter_error: got %q, want %q", got.OnLimiterError, domain.LimiterErrorDeny)
		}
	})
}

func TestEmptyArgPredicateIsRejectedAtLoad(t *testing.T) {
	// An empty predicate and equals:"" unmarshal to the same zero struct, so
	// both must be rejected — otherwise the rule silently matches any command.
	for _, bundle := range []string{
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: {}\n    then: { decision: allow }\n",
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: { equals: \"\" }\n    then: { decision: allow }\n",
	} {
		if _, err := NewRuleEngineFromBundle(bundle); err == nil {
			t.Errorf("expected load to fail for empty predicate, bundle:\n%s", bundle)
		}
	}

	// A predicate with a real constraint alongside an empty field still loads —
	// the empty field is just ignored, the constraint carries the rule.
	if _, err := NewRuleEngineFromBundle(
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: { contains: rm, equals: \"\" }\n    then: { decision: deny }\n",
	); err != nil {
		t.Fatalf("predicate with a real constraint should load: %v", err)
	}
}

func TestRuleIDIsNotSettableFromBundle(t *testing.T) {
	engine, err := NewRuleEngineFromBundle(`
rules:
  - id: real-id
    priority: 1
    then:
      decision: allow
      ruleid: forged
      rule_id: forged
`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Evaluate(context.Background(), domain.PolicyInput{Target: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuleID != "real-id" {
		t.Fatalf("rule_id: got %q, want real-id", res.RuleID)
	}
}
