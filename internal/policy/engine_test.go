package policy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

func TestRuleEngineStepCountCondition(t *testing.T) {
	stepLimit := 50
	engine, err := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-after-50-steps",
				Priority: 1,
				When: domain.PolicyCondition{
					Action:       "tool.invoke",
					MinStepCount: &stepLimit,
				},
				Then: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "too many steps"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyAllow},
	})
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}

	// Under limit: should allow (rule doesn't match, falls to default allow)
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", StepCount: 10,
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for step_count=10, got %s", result.Decision)
	}

	// At limit: should deny (rule matches)
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", StepCount: 50,
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for step_count=50, got %s", result.Decision)
	}

	// Over limit: should deny
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", StepCount: 100,
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for step_count=100, got %s", result.Decision)
	}
}

func TestRuleEngineDurationCondition(t *testing.T) {
	minDuration := int64(60000) // 60 seconds
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "deny-after-60s",
				Priority: 1,
				When: domain.PolicyCondition{
					Action:        "tool.invoke",
					MinDurationMs: &minDuration,
				},
				Then: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "execution too long"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyAllow},
	})

	// Over duration: should deny (rule matches)
	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", DurationMs: 90000,
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for duration=90s, got %s", result.Decision)
	}

	// Under duration: should allow (rule doesn't match, falls to default)
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", DurationMs: 30000,
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for duration=30s, got %s", result.Decision)
	}

	// At boundary: should deny (rule matches, inclusive threshold like MinStepCount)
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", DurationMs: 60000,
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for duration=60000 (at boundary, inclusive), got %s", result.Decision)
	}

	// One below boundary: should allow (rule doesn't match)
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", DurationMs: 59999,
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for duration=59999 (below boundary), got %s", result.Decision)
	}

	// One above boundary: should deny
	result, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", DurationMs: 60001,
	})
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for duration=60001 (above boundary), got %s", result.Decision)
	}
}

func TestRuleEngineScheduleCondition(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "business-hours-only",
				Priority: 1,
				When: domain.PolicyCondition{
					Action:   "tool.invoke",
					Schedule: "Mon-Fri 09:00-17:00 UTC",
				},
				Then: domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "within business hours"},
			},
		},
		Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "outside business hours"},
	})

	// Wednesday 12:00 UTC - within business hours
	withinHours := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	result, _ := engine.EvaluateAt(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search",
	}, withinHours)
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow during business hours, got %s", result.Decision)
	}

	// Saturday 12:00 UTC - outside business hours
	weekend := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	result, _ = engine.EvaluateAt(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search",
	}, weekend)
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny on weekend, got %s", result.Decision)
	}

	// Wednesday 22:00 UTC - outside business hours
	lateNight := time.Date(2026, 3, 25, 22, 0, 0, 0, time.UTC)
	result, _ = engine.EvaluateAt(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search",
	}, lateNight)
	if result.Decision != domain.PolicyDeny {
		t.Errorf("expected deny outside hours, got %s", result.Decision)
	}
}

func TestRuleEngineRateLimitPropagated(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:        "rate-limited-shell",
				Priority:  1,
				When:      domain.PolicyCondition{Action: "tool.invoke", ToolID: "shell.exec"},
				Then:      domain.PolicyAction{Decision: domain.PolicyAllow},
				RateLimit: &domain.RateLimitConfig{Max: 10, Window: "1m"},
			},
		},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "shell.exec",
	})
	if result.Decision != domain.PolicyAllow {
		t.Errorf("expected allow, got %s", result.Decision)
	}
	if result.RateLimit == nil {
		t.Fatal("expected rate limit config, got nil")
	}
	if result.RateLimit.Max != 10 || result.RateLimit.Window != "1m" {
		t.Errorf("expected max=10 window=1m, got max=%d window=%s", result.RateLimit.Max, result.RateLimit.Window)
	}
}

func TestRuleEngineNoRateLimitWhenNotConfigured(t *testing.T) {
	engine, _ := NewRuleEngine(PolicyConfig{
		Rules: []domain.PolicyRule{
			{
				ID:       "allow-search",
				Priority: 1,
				When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.search"},
				Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
			},
		},
	})

	result, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search",
	})
	if result.RateLimit != nil {
		t.Errorf("expected nil rate limit, got %+v", result.RateLimit)
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
