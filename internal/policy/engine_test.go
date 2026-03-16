package policy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestRuleEngineMatchesByToolID(t *testing.T) {
	engine, err := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-search",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web_search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "search allowed"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "denied by default"},
	})
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}

	result, err := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "web_search",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow, got %s", result.Decision)
	}
	if result.RuleID != "allow-search" {
		t.Errorf("expected rule ID allow-search, got %s", result.RuleID)
	}
}

func TestRuleEngineFallsToDefault(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-search",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web_search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "no matching rule"},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "dangerous_tool",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for unmatched tool, got %s", result.Decision)
	}
	if result.RuleID != "default" {
		t.Errorf("expected rule ID default, got %s", result.RuleID)
	}
}

func TestRuleEnginePriorityOrdering(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-all-tools",
				Priority: 10,
				When:     domain.PolicyCondition{Action: "tool.invoke"},
				Then:     domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "catch-all deny"},
			},
			{
				ID:       "allow-search",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web_search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "search exempted"},
			},
		},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "web_search",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow (priority 1 match), got %s: %s", result.Decision, result.Reason)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "delete_files",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny (priority 10 catch-all), got %s", result.Decision)
	}
}

func TestRuleEngineMatchesByToolIDs(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-set",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolIDs: []string{"tool_a", "tool_b"}},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{Action: "tool.invoke", ToolID: "tool_a"})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for tool_a, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{Action: "tool.invoke", ToolID: "tool_c"})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for tool_c, got %s", result.Decision)
	}
}

func TestRuleEngineMatchesByLabels(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "env-prod",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", Labels: map[string]string{"env": "prod"}},
				Then:     domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "no tools in prod"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyAllow},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		Labels: map[string]string{"env": "prod"},
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for env=prod, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		Labels: map[string]string{"env": "dev"},
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for env=dev, got %s", result.Decision)
	}
}

func TestRuleEngineMatchesByAgentID(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-researcher",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", AgentID: "researcher"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "researcher allowed"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "denied"},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "web.search",
		AgentID: "researcher",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for researcher, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "web.search",
		AgentID: "deploy-bot",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for deploy-bot, got %s", result.Decision)
	}
}

func TestRuleEngineMatchesByAgentIDs(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-research-agents",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", AgentIDs: []string{"researcher", "analyst"}},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "analyst",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for analyst, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "deploy-bot",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for deploy-bot, got %s", result.Decision)
	}
}

func TestRuleEngineWildcardToolID(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-web-tools",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.*"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for web.search, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.fetch",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for web.fetch, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "shell.exec",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for shell.exec, got %s", result.Decision)
	}
}

func TestRuleEngineWildcardToolIDs(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-safe-tools",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolIDs: []string{"web.*", "doc.*"}},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "doc.fetch",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for doc.fetch, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "shell.exec",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for shell.exec, got %s", result.Decision)
	}
}

func TestRuleEngineAgentAndToolCombined(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "researcher-web-only",
				Priority: 1,
				When: domain.PolicyCondition{
					Action:  "tool.invoke",
					AgentID: "researcher",
					ToolID:  "web.*",
				},
				Then: domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "researcher can use web tools"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "denied"},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "researcher",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for researcher+web.search, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "shell.exec", AgentID: "researcher",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for researcher+shell.exec, got %s", result.Decision)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "deploy-bot",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for deploy-bot+web.search, got %s", result.Decision)
	}
}

func TestDuplicatePriorityRejected(t *testing.T) {
	_, err := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{ID: "rule-a", Priority: 1, When: domain.PolicyCondition{Action: "tool.invoke"}, Then: domain.PolicyAction{Decision: domain.PolicyAllow}},
			{ID: "rule-b", Priority: 1, When: domain.PolicyCondition{Action: "tool.invoke"}, Then: domain.PolicyAction{Decision: domain.PolicyDeny}},
		},
	})
	if err == nil {
		t.Fatal("expected error for duplicate priority")
	}
}

func TestSecureDefaultEngineAllowsLifecycle(t *testing.T) {
	engine := NewSecureDefaultEngine(nil)

	lifecycleActions := []string{"execution.complete", "execution.fail", "execution.wait"}
	for _, action := range lifecycleActions {
		result, err := engine.Evaluate(context.Background(), domain.PolicyInput{Action: action})
		if err != nil {
			t.Fatalf("Evaluate(%s): %v", action, err)
		}
		if result.Decision != domain.PolicyAllow {
			t.Errorf("expected allow for %s, got %s", action, result.Decision)
		}
	}
}

func TestSecureDefaultEngineDeniesToolsWithoutInner(t *testing.T) {
	engine := NewSecureDefaultEngine(nil)

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "web_search",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for tool.invoke without inner engine, got %s", result.Decision)
	}
}

func TestSecureDefaultEngineDelegatesToInner(t *testing.T) {
	inner, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-all",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
	})
	engine := NewSecureDefaultEngine(inner)

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "anything",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow via inner engine, got %s", result.Decision)
	}
}

func TestRuleEngineEmptyRulesUsesDefault(t *testing.T) {
	engine, err := NewRuleEngine(PolicyConfig{
		Rules:   []domain.PolicyRule{},
		Default: domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "empty rules allow"},
	})
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "anything",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow from default, got %s", result.Decision)
	}
	if result.RuleID != "default" {
		t.Errorf("expected rule ID default, got %s", result.RuleID)
	}
}

func TestRuleEngineNoDefaultUsesImplicitDeny(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-search",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		// No default specified
	})
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "unmatched",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected implicit deny, got %s", result.Decision)
	}
}

func TestRuleEngineRequireApprovalDecision(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "approve-deploy",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "deploy"},
				Then:     domain.PolicyAction{Decision: domain.PolicyRequireApproval, Reason: "needs human approval"},
			},
		},
	})
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "deploy",
	})
	if result.Decision != domain.PolicyRequireApproval {
		t.Errorf("expected require_approval, got %s", result.Decision)
	}
}

func TestRuleEngineTimeoutMsPropagated(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-with-timeout",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "slow_tool"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow, TimeoutMs: 30000},
			},
		},
	})
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "slow_tool",
	})
	if result.TimeoutMs != 30000 {
		t.Errorf("expected timeout_ms=30000, got %d", result.TimeoutMs)
	}
}

func TestRuleEngineEmptyActionMatchesAll(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "match-all",
				Priority: 1,
				When:     domain.PolicyCondition{ToolID: "web.search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "web.search",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow (empty action matches any), got %s", result.Decision)
	}
}

func TestRuleEngineActionMismatch(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "lifecycle-only",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "execution.complete"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "anything",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny (action mismatch), got %s", result.Decision)
	}
}

func TestRuleEngineLabelsPartialMatch(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "env-tier",
				Priority: 1,
				When: domain.PolicyCondition{
					Action: "tool.invoke",
					Labels: map[string]string{"env": "prod", "tier": "critical"},
				},
				Then: domain.PolicyAction{Decision: domain.PolicyDeny},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyAllow},
	})
	// Only env=prod but missing tier=critical
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		Labels: map[string]string{"env": "prod"},
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow (partial label match should not trigger rule), got %s", result.Decision)
	}
	// Both labels match
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		Labels: map[string]string{"env": "prod", "tier": "critical"},
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny (all labels match), got %s", result.Decision)
	}
}

func TestRuleEngineGlobPatternInvalid(t *testing.T) {
	// path.Match returns error for malformed patterns like "[invalid"
	// The globMatch func falls back to exact string comparison
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "bad-glob",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "[invalid"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny},
	})
	// Falls back to exact match
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "[invalid",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow (exact match fallback for invalid glob), got %s", result.Decision)
	}
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke",
		ToolID: "other",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny (no match for invalid glob), got %s", result.Decision)
	}
}

func TestSecureDefaultEngineUnknownActionWithoutInner(t *testing.T) {
	engine := NewSecureDefaultEngine(nil)
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "unknown.action",
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for unknown action without inner, got %s", result.Decision)
	}
}

func TestRuleEngineArgumentPredicateWithToolID(t *testing.T) {
	engine, err := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-safe-shell",
				Priority: 1,
				When: domain.PolicyCondition{
					Action: "tool.invoke",
					ToolID: "shell.exec",
					Arguments: []domain.ArgumentPredicate{
						{Field: "command", Pattern: "^ls"},
					},
				},
				Then: domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "safe shell command"},
			},
			{
				ID:       "deny-shell",
				Priority: 10,
				When: domain.PolicyCondition{
					Action: "tool.invoke",
					ToolID: "shell.exec",
				},
				Then: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "shell denied by default"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "default deny"},
	})
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:    "tool.invoke",
		ToolID:    "shell.exec",
		Arguments: json.RawMessage(`{"command":"ls -la"}`),
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for ls command, got %s (%s)", result.Decision, result.Reason)
	}
	if result.RuleID != "allow-safe-shell" {
		t.Errorf("expected rule allow-safe-shell, got %s", result.RuleID)
	}

	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:    "tool.invoke",
		ToolID:    "shell.exec",
		Arguments: json.RawMessage(`{"command":"rm -rf /"}`),
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for rm command, got %s", result.Decision)
	}
	if result.RuleID != "deny-shell" {
		t.Errorf("expected rule deny-shell, got %s", result.RuleID)
	}
}
