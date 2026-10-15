package policy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestArgPredicateRegex(t *testing.T) {
	bundle := `
rules:
  - id: allow-prod
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
	// both must be rejected: an unconstrained rule matches any command.
	for _, bundle := range []string{
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: {}\n    then: { decision: allow }\n",
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: { equals: \"\" }\n    then: { decision: allow }\n",
	} {
		if _, err := NewRuleEngineFromBundle(bundle); err == nil {
			t.Errorf("expected load to fail for empty predicate, bundle:\n%s", bundle)
		}
	}

	if _, err := NewRuleEngineFromBundle(
		"rules:\n  - id: a\n    when:\n      arguments:\n        command: { contains: rm, equals: \"\" }\n    then: { decision: deny }\n",
	); err != nil {
		t.Fatalf("predicate with a real constraint should load: %v", err)
	}
}

func TestRuleIDIsNotSettableFromBundle(t *testing.T) {
	if _, err := NewRuleEngineFromBundle(`
rules:
  - id: real-id
    then:
      decision: allow
      rule_id: forged
`); err == nil {
		t.Fatal("a bundle setting rule_id should be rejected")
	}

	engine, err := NewRuleEngineFromBundle("rules:\n  - id: real-id\n    then: { decision: allow }\n")
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

func TestLocalStepsBypassDefaultActionButNotRules(t *testing.T) {
	bundle := `
default_action: deny
rules:
  - id: no-charging
    when:
      target: charge_card
    then:
      decision: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		kind   domain.StepKind
		target string
		want   string
	}{
		{"unmatched local step", domain.StepKindLocal, "brief_ref", domain.DecisionAllow},
		{"unmatched tool call", domain.StepKindTool, "send_email", domain.DecisionDeny},
		{"local step a rule names", domain.StepKindLocal, "charge_card", domain.DecisionDeny},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{StepKind: tc.kind, Target: tc.target})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, res.Decision)
			}
		})
	}
}

func TestTargetGlobsMatchAcrossSlash(t *testing.T) {
	bundle := `
default_action: allow
rules:
  - id: no-mcp-delete
    when:
      target: "mcp/*delete"
    then:
      decision: deny
  - id: no-openai
    when:
      targets: ["openai/gpt-?.*"]
    then:
      decision: deny
  - id: no-bracket
    when:
      target: "fs_[dr]m"
    then:
      decision: deny
  - id: no-escaped-range
    when:
      target: 'x[a\-z]'
    then:
      decision: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		target string
		want   string
	}{
		{"mcp/fs_delete", domain.DecisionDeny},
		{"mcp/nested/fs_delete", domain.DecisionDeny},
		{"mcp/fs_read", domain.DecisionAllow},
		{"openai/gpt-5.4", domain.DecisionDeny},
		{"openai/gpt-54", domain.DecisionAllow},
		{"fs_rm", domain.DecisionDeny},
		{"fs_xm", domain.DecisionAllow},
		{"x-", domain.DecisionDeny},
		{"xb", domain.DecisionAllow},
	}
	for _, tc := range cases {
		res, err := engine.Evaluate(context.Background(), domain.PolicyInput{StepKind: domain.StepKindTool, Target: tc.target})
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != tc.want {
			t.Errorf("%s: expected %s, got %s", tc.target, tc.want, res.Decision)
		}
	}
}

func TestSubjectsMatchIdentityOrGroup(t *testing.T) {
	engine, err := NewRuleEngineFromBundle(`
default_action: deny
rules:
  - id: platform-deploys
    when:
      target: deploy
      subjects: ["group:platform", "user:alice@corp.com"]
    then:
      decision: allow
`)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		subject string
		groups  []string
		want    string
	}{
		{"listed user", "user:alice@corp.com", nil, domain.DecisionAllow},
		{"member of a listed group", "user:bob@corp.com", []string{"platform"}, domain.DecisionAllow},
		{"unlisted user", "user:bob@corp.com", []string{"support"}, domain.DecisionDeny},
		{"no subject", "", nil, domain.DecisionDeny},
	}
	for _, tc := range cases {
		res, err := engine.Evaluate(context.Background(), domain.PolicyInput{StepKind: domain.StepKindTool, Target: "deploy", Subject: tc.subject, SubjectGroups: tc.groups})
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, res.Decision, tc.want)
		}
	}

	if _, err := NewRuleEngineFromBundle(`
rules:
  - id: bad
    when:
      subjects: ["alice"]
    then:
      decision: allow
`); err == nil {
		t.Error("a subject without a prefix loaded")
	}
}

func TestMalformedTargetGlobIsRejectedAtLoad(t *testing.T) {
	for _, bundle := range []string{
		"rules:\n  - id: a\n    when: { target: \"fs_[\" }\n    then: { decision: deny }\n",
		"rules:\n  - id: a\n    when: { targets: [ok, \"fs_\\\\\"] }\n    then: { decision: deny }\n",
	} {
		if _, err := NewRuleEngineFromBundle(bundle); err == nil {
			t.Errorf("expected load to fail for malformed glob, bundle:\n%s", bundle)
		}
	}
}

func TestNonStringArgumentsCompareAsJSONText(t *testing.T) {
	bundle := `
default_action: allow
rules:
  - id: large-amount
    when:
      arguments:
        amount:
          regex: '^[0-9]{7,}$'
    then:
      decision: deny
  - id: nested
    when:
      arguments:
        opts:
          equals: '{"force":true}'
    then:
      decision: deny
  - id: null-owner
    when:
      arguments:
        owner:
          equals: "null"
    then:
      decision: deny
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args string
		want string
	}{
		{`{"amount": 5000000}`, domain.DecisionDeny},
		{`{"amount": 12345678901234567}`, domain.DecisionDeny},
		{`{"amount": "5000000"}`, domain.DecisionDeny},
		{`{"amount": 999999}`, domain.DecisionAllow},
		{`{"opts": { "force": true }}`, domain.DecisionDeny},
		{`{"owner": null}`, domain.DecisionDeny},
	}
	for _, tc := range cases {
		res, err := engine.Evaluate(context.Background(), domain.PolicyInput{StepKind: domain.StepKindTool, Args: json.RawMessage(tc.args)})
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != tc.want {
			t.Errorf("%s: expected %s, got %s", tc.args, tc.want, res.Decision)
		}
	}
}

func TestDottedArgumentPathsMatchNestedFields(t *testing.T) {
	bundle := `
default_action: deny
rules:
  - id: staging-deploys
    when:
      arguments:
        config.environment:
          equals: staging
        config.region.name:
          one_of: [us-east, eu-west]
    then:
      decision: allow
  - id: nested-object
    when:
      arguments:
        input.opts:
          equals: '{"force":true}'
    then:
      decision: require_approval
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args string
		want string
	}{
		{"nested match", `{"config": {"environment": "staging", "region": {"name": "us-east"}}}`, domain.DecisionAllow},
		{"nested mismatch", `{"config": {"environment": "production", "region": {"name": "us-east"}}}`, domain.DecisionDeny},
		{"missing leaf", `{"config": {"environment": "staging"}}`, domain.DecisionDeny},
		{"intermediate not an object", `{"config": "staging"}`, domain.DecisionDeny},
		{"intermediate is an array", `{"config": [{"environment": "staging"}]}`, domain.DecisionDeny},
		{"intermediate is null", `{"config": null}`, domain.DecisionDeny},
		{"dotted name takes precedence", `{"config.environment": "production", "config": {"environment": "staging", "region": {"name": "us-east"}}}`, domain.DecisionDeny},
		{"dotted name matched as a whole", `{"config.environment": "staging", "config.region.name": "eu-west"}`, domain.DecisionAllow},
		{"object leaf compares as JSON text", `{"input": {"opts": { "force": true }}}`, domain.DecisionRequireApproval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{StepKind: domain.StepKindTool, Args: json.RawMessage(tc.args)})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want {
				t.Errorf("expected %s, got %s", tc.want, res.Decision)
			}
		})
	}
}

func priorStep(target, args, result string, ago time.Duration, now time.Time) domain.Step {
	started := now.Add(-ago)
	return domain.Step{
		Kind: domain.StepKindTool, Target: target, Args: json.RawMessage(args),
		Result: json.RawMessage(result), Status: domain.StepSucceeded, StartedAt: &started,
	}
}

func TestHistoryConditions(t *testing.T) {
	bundle := `
default_action: deny
rules:
  - id: sell-after-approval
    when:
      target: sell
      history:
        - target: approve_sale
          within: 1h
          arguments:
            stock: { equals_arg: stock }
          result:
            approved: { equals: "true" }
        - target: check_price
          arguments:
            stock: { equals_arg: stock }
    then: { decision: allow }
  - id: no-email-after-fetch
    when:
      target: send_email
      history:
        - scope: session
          target: web_fetch
    then: { decision: deny }
  - id: email
    when: { target: send_email }
    then: { decision: allow }
  - id: transfer-under-cap
    when:
      target: transfer
      history:
        - target: transfer
          sum: amount
          at_most: 100
    then: { decision: allow }
  - id: few-recipients
    when:
      target: notify
      history:
        - target: notify
          distinct: user
          at_most: 2
    then: { decision: allow }
`
	engine, err := NewRuleEngineFromBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	approved := func(stock string, ago time.Duration) domain.Step {
		return priorStep("approve_sale", `{"stock":"`+stock+`"}`, `{"approved":true}`, ago, now)
	}
	priced := priorStep("check_price", `{"stock":"AMZN"}`, `{}`, time.Minute, now)

	cases := []struct {
		name   string
		target string
		args   string
		prior  []domain.Step
		want   string
	}{
		{"sell with matching approval and price check", "sell", `{"stock":"AMZN"}`, []domain.Step{approved("AMZN", time.Minute), priced}, domain.DecisionAllow},
		{"sell missing one prerequisite", "sell", `{"stock":"AMZN"}`, []domain.Step{approved("AMZN", time.Minute)}, domain.DecisionDeny},
		{"sell approved for another stock", "sell", `{"stock":"AMZN"}`, []domain.Step{approved("MSFT", time.Minute), priced}, domain.DecisionDeny},
		{"sell approval outside window", "sell", `{"stock":"AMZN"}`, []domain.Step{approved("AMZN", 2*time.Hour), priced}, domain.DecisionDeny},
		{"sell approval refused", "sell", `{"stock":"AMZN"}`, []domain.Step{priorStep("approve_sale", `{"stock":"AMZN"}`, `{"approved":false}`, time.Minute, now), priced}, domain.DecisionDeny},
		{"email before fetch", "send_email", `{}`, nil, domain.DecisionAllow},
		{"email after fetch", "send_email", `{}`, []domain.Step{priorStep("web_fetch", `{}`, `{}`, time.Minute, now)}, domain.DecisionDeny},
		{"transfer within cap counting this call", "transfer", `{"amount":40}`, []domain.Step{priorStep("transfer", `{"amount":60}`, `{}`, time.Minute, now)}, domain.DecisionAllow},
		{"transfer over cap counting this call", "transfer", `{"amount":41}`, []domain.Step{priorStep("transfer", `{"amount":60}`, `{}`, time.Minute, now)}, domain.DecisionDeny},
		{"transfer with non-numeric prior amount", "transfer", `{"amount":1}`, []domain.Step{priorStep("transfer", `{"amount":"lots"}`, `{}`, time.Minute, now)}, domain.DecisionDeny},
		{"repeat recipient", "notify", `{"user":"a"}`, []domain.Step{priorStep("notify", `{"user":"a"}`, `{}`, time.Minute, now), priorStep("notify", `{"user":"b"}`, `{}`, time.Minute, now)}, domain.DecisionAllow},
		{"new recipient over limit", "notify", `{"user":"c"}`, []domain.Step{priorStep("notify", `{"user":"a"}`, `{}`, time.Minute, now), priorStep("notify", `{"user":"b"}`, `{}`, time.Minute, now)}, domain.DecisionDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := engine.Evaluate(context.Background(), domain.PolicyInput{
				Target: tc.target, Args: json.RawMessage(tc.args), StepKind: domain.StepKindTool, Now: now,
				History: func(_ context.Context, _ string, since time.Time) ([]domain.Step, error) {
					var out []domain.Step
					for _, s := range tc.prior {
						if !s.StartedAt.Before(since) {
							out = append(out, s)
						}
					}
					return out, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Decision != tc.want {
				t.Fatalf("decision = %s (rule %s), want %s", res.Decision, res.RuleID, tc.want)
			}
		})
	}
}

func TestHistoryUnavailableDenies(t *testing.T) {
	engine, err := NewRuleEngineFromBundle(`
default_action: allow
rules:
  - id: no-email-after-fetch
    when:
      target: send_email
      history:
        - target: web_fetch
    then: { decision: deny }
`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Evaluate(context.Background(), domain.PolicyInput{
		Target: "send_email", Args: json.RawMessage(`{}`),
		History: func(context.Context, string, time.Time) ([]domain.Step, error) {
			return nil, errors.New("store down")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != domain.DecisionDeny || res.Reason != domain.ReasonHistoryUnavailable || res.RuleID != "no-email-after-fetch" {
		t.Fatalf("result = %+v, want history_unavailable denial from no-email-after-fetch", res)
	}
}

func TestHistoryBundleValidation(t *testing.T) {
	cases := map[string]string{
		"equals_arg outside history": `
rules:
  - id: r
    when: { arguments: { a: { equals_arg: b } } }
    then: { decision: allow }`,
		"nested history": `
rules:
  - id: r
    when: { history: [ { target: x, history: [ { target: y } ] } ] }
    then: { decision: allow }`,
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRuleEngineFromBundle(bundle); err == nil {
				t.Fatal("bundle accepted")
			}
		})
	}
}
