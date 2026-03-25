package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const validAgentPolicy = `
rules:
  - id: "allow-web"
    priority: 10
    when:
      action: "tool.invoke"
      tool_id: "web.search"
    then:
      decision: "allow"
`

const validAgentPolicy2 = `
rules:
  - id: "allow-shell"
    priority: 10
    when:
      action: "tool.invoke"
      tool_id: "shell.exec"
    then:
      decision: "allow"
`

func TestLoadSingleFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "policy.yaml", validAgentPolicy)

	cfg, err := Load(filepath.Join(dir, "policy.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Rules))
	}
	if cfg.Rules[0].ID != "allow-web" {
		t.Errorf("expected rule ID allow-web, got %s", cfg.Rules[0].ID)
	}
}

func TestLoadDirReturnsPerAgentConfigs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "researcher.yaml", validAgentPolicy)
	writeFile(t, dir, "shell.yaml", validAgentPolicy2)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result.Agents))
	}
	if result.Global != nil {
		t.Error("expected no global config")
	}
	if _, ok := result.Agents["researcher"]; !ok {
		t.Error("expected config keyed by 'researcher'")
	}
	if _, ok := result.Agents["shell"]; !ok {
		t.Error("expected config keyed by 'shell'")
	}
	if result.Agents["researcher"].Rules[0].ID != "allow-web" {
		t.Errorf("researcher config: expected rule allow-web, got %s", result.Agents["researcher"].Rules[0].ID)
	}
	if result.Agents["shell"].Rules[0].ID != "allow-shell" {
		t.Errorf("shell config: expected rule allow-shell, got %s", result.Agents["shell"].Rules[0].ID)
	}
}

func TestLoadDirMixedExtensions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "agent-a.yaml", validAgentPolicy)
	writeFile(t, dir, "agent-b.yml", validAgentPolicy2)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(result.Agents) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(result.Agents))
	}
	if _, ok := result.Agents["agent-a"]; !ok {
		t.Error("expected config keyed by 'agent-a'")
	}
	if _, ok := result.Agents["agent-b"]; !ok {
		t.Error("expected config keyed by 'agent-b'")
	}
}

func TestLoadDirIgnoresNonYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "researcher.yaml", validAgentPolicy)
	writeFile(t, dir, "readme.md", "# docs")
	writeFile(t, dir, "notes.txt", "some notes")

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 config (non-YAML ignored), got %d", len(result.Agents))
	}
}

func TestLoadDirNoYAMLFilesError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "readme.txt", "not yaml")

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected error for no YAML files, got nil")
	}
	if !strings.Contains(err.Error(), "no .yaml or .yml files") {
		t.Errorf("expected no YAML files error, got: %v", err)
	}
}

func TestLoadDirInvalidYAMLError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.yaml", "not: [valid: yaml")

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("expected error to mention filename, got: %v", err)
	}
}

func TestLoadNonExistentPathError(t *testing.T) {
	_, err := Load("/nonexistent/path/policy.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
}

func TestAgentEngineRoutesToCorrectAgent(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"researcher": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-web",
						Priority: 10,
						When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.search"},
						Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
					},
				},
			},
			"shell": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-shell",
						Priority: 10,
						When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "shell.exec"},
						Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
					},
				},
			},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	r, err := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "web.search",
		AgentID: "researcher",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if r.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for researcher+web.search, got %s", r.Decision)
	}

	r, err = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "shell.exec",
		AgentID: "researcher",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if r.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for researcher+shell.exec, got %s", r.Decision)
	}

	r, err = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "shell.exec",
		AgentID: "shell",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if r.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for shell+shell.exec, got %s", r.Decision)
	}
}

func TestAgentEngineDeniesUnknownAgent(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"researcher": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-web",
						Priority: 10,
						When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.search"},
						Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
					},
				},
			},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	r, err := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action:  "tool.invoke",
		ToolID:  "web.search",
		AgentID: "unknown-agent",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if r.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for unknown agent, got %s", r.Decision)
	}
	if !strings.Contains(r.Reason, "unknown-agent") {
		t.Errorf("expected reason to mention agent name, got: %s", r.Reason)
	}
}

func TestAgentEngineAgentsList(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"charlie": {Rules: []domain.PolicyRule{{
				ID: "r1", Priority: 1,
				When: domain.PolicyCondition{Action: "tool.invoke", ToolID: "t"},
				Then: domain.PolicyAction{Decision: domain.PolicyAllow},
			}}},
			"alpha": {Rules: []domain.PolicyRule{{
				ID: "r1", Priority: 1,
				When: domain.PolicyCondition{Action: "tool.invoke", ToolID: "t"},
				Then: domain.PolicyAction{Decision: domain.PolicyAllow},
			}}},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	agents := engine.Agents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0] != "alpha" || agents[1] != "charlie" {
		t.Errorf("expected [alpha charlie], got %v", agents)
	}
}

const globalPolicy = `
rules:
  - id: "global-deny-dangerous"
    priority: 100
    when:
      action: "tool.invoke"
      tool_id: "dangerous.tool"
    then:
      decision: "deny"
      reason: "globally denied"
default:
  decision: "deny"
  reason: "global default deny"
`

const globalPolicyWithAgentIDs = `
rules:
  - id: "research-agents-web"
    priority: 50
    when:
      action: "tool.invoke"
      agent_ids: ["researcher", "analyst"]
      tool_id: "web.*"
    then:
      decision: "allow"
      reason: "research agents can use web tools"
default:
  decision: "deny"
  reason: "global default deny"
`

func TestLoadDirGlobalFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "researcher.yaml", validAgentPolicy)
	writeFile(t, dir, "_global.yaml", globalPolicy)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if result.Global == nil {
		t.Fatal("expected global config to be loaded")
	}
	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 agent config, got %d", len(result.Agents))
	}
	if _, ok := result.Agents["_global"]; ok {
		t.Error("_global should not appear in agent configs")
	}
}

func TestLoadDirGlobalOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "_global.yaml", globalPolicy)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if result.Global == nil {
		t.Fatal("expected global config")
	}
	if len(result.Agents) != 0 {
		t.Fatalf("expected 0 agent configs, got %d", len(result.Agents))
	}
}

func TestAgentEngineGlobalRulesMergedIntoAgentEngine(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"researcher": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-web",
						Priority: 10,
						When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.search"},
						Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
					},
				},
			},
		},
		Global: &PolicyConfig{
			Rules: []domain.PolicyRule{
				{
					ID:       "global-deny-dangerous",
					Priority: 100,
					When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "dangerous.tool"},
					Then:     domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "globally denied"},
				},
			},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	// Agent-specific rule still works
	r, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "researcher",
	})
	if r.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for researcher+web.search, got %s", r.Decision)
	}

	// Global rule is enforced within agent engine
	r, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "dangerous.tool", AgentID: "researcher",
	})
	if r.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for researcher+dangerous.tool via global rule, got %s", r.Decision)
	}
	if r.RuleID != "global-deny-dangerous" {
		t.Errorf("expected global rule ID, got %s", r.RuleID)
	}
}

func TestAgentEngineGlobalFallbackForUnknownAgent(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"researcher": {
				Rules: []domain.PolicyRule{
					{
						ID:       "allow-web",
						Priority: 10,
						When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "web.search"},
						Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
					},
				},
			},
		},
		Global: &PolicyConfig{
			Rules: []domain.PolicyRule{
				{
					ID:       "global-allow-calculator",
					Priority: 50,
					When:     domain.PolicyCondition{Action: "tool.invoke", ToolID: "calculator"},
					Then:     domain.PolicyAction{Decision: domain.PolicyAllow},
				},
			},
			Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "global default deny"},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	// Unknown agent matches global rule
	r, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "calculator", AgentID: "new-agent",
	})
	if r.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for new-agent+calculator via global, got %s", r.Decision)
	}

	// Unknown agent hits global default
	r, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "something-else", AgentID: "new-agent",
	})
	if r.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for new-agent+something-else via global default, got %s", r.Decision)
	}
}

func TestParseValidationMissingRuleID(t *testing.T) {
	yaml := `
rules:
  - priority: 1
    when:
      action: "tool.invoke"
    then:
      decision: "allow"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for missing rule ID")
	}
	if !strings.Contains(err.Error(), "missing ID") {
		t.Errorf("expected missing ID error, got: %v", err)
	}
}

func TestParseValidationDuplicateRuleID(t *testing.T) {
	yaml := `
rules:
  - id: "same-id"
    priority: 1
    when:
      action: "tool.invoke"
    then:
      decision: "allow"
  - id: "same-id"
    priority: 2
    when:
      action: "tool.invoke"
    then:
      decision: "deny"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for duplicate rule ID")
	}
	if !strings.Contains(err.Error(), "duplicate rule ID") {
		t.Errorf("expected duplicate rule ID error, got: %v", err)
	}
}

func TestParseValidationInvalidDecision(t *testing.T) {
	yaml := `
rules:
  - id: "bad-decision"
    priority: 1
    when:
      action: "tool.invoke"
    then:
      decision: "maybe"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid decision")
	}
	if !strings.Contains(err.Error(), "invalid decision") {
		t.Errorf("expected invalid decision error, got: %v", err)
	}
}

func TestParseValidationInvalidDefaultDecision(t *testing.T) {
	yaml := `
rules:
  - id: "ok"
    priority: 1
    when:
      action: "tool.invoke"
    then:
      decision: "allow"
default:
  decision: "invalid"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid default decision")
	}
	if !strings.Contains(err.Error(), "invalid decision") {
		t.Errorf("expected invalid decision error, got: %v", err)
	}
}

func TestParseValidationEmptyArgumentField(t *testing.T) {
	yaml := `
rules:
  - id: "bad-arg"
    priority: 1
    when:
      action: "tool.invoke"
      arguments:
        - field: ""
          pattern: "^ls"
    then:
      decision: "allow"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty argument field")
	}
	if !strings.Contains(err.Error(), "empty field") {
		t.Errorf("expected empty field error, got: %v", err)
	}
}

func TestParseValidationNoConstraintArgument(t *testing.T) {
	yaml := `
rules:
  - id: "no-constraint"
    priority: 1
    when:
      action: "tool.invoke"
      arguments:
        - field: "command"
    then:
      decision: "allow"
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for argument predicate with no constraints")
	}
	if !strings.Contains(err.Error(), "no constraints") {
		t.Errorf("expected no constraints error, got: %v", err)
	}
}

func TestParseValidationRequireApprovalDecision(t *testing.T) {
	yaml := `
rules:
  - id: "needs-approval"
    priority: 1
    when:
      action: "tool.invoke"
      tool_id: "deploy"
    then:
      decision: "require_approval"
      reason: "needs human sign-off"
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("expected require_approval to be valid, got: %v", err)
	}
	if cfg.Rules[0].Then.Decision != domain.PolicyRequireApproval {
		t.Errorf("expected require_approval, got %s", cfg.Rules[0].Then.Decision)
	}
}

func TestParseWithDefaultBlock(t *testing.T) {
	yaml := `
rules:
  - id: "r1"
    priority: 1
    when:
      action: "tool.invoke"
    then:
      decision: "allow"
default:
  decision: "deny"
  reason: "explicit default deny"
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Default.Decision != domain.PolicyDeny {
		t.Errorf("expected default deny, got %s", cfg.Default.Decision)
	}
	if cfg.Default.Reason != "explicit default deny" {
		t.Errorf("expected explicit default deny reason, got %s", cfg.Default.Reason)
	}
}

func TestLoadDirGlobalYmlExtension(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "_global.yml", globalPolicy)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if result.Global == nil {
		t.Fatal("expected _global.yml to be loaded as global config")
	}
}

func TestAgentEnginePriorityConflictBetweenAgentAndGlobal(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{
			"agent1": {
				Rules: []domain.PolicyRule{
					{ID: "agent-rule", Priority: 100, When: domain.PolicyCondition{Action: "tool.invoke", ToolID: "t"}, Then: domain.PolicyAction{Decision: domain.PolicyAllow}},
				},
			},
		},
		Global: &PolicyConfig{
			Rules: []domain.PolicyRule{
				{ID: "global-rule", Priority: 100, When: domain.PolicyCondition{Action: "tool.invoke", ToolID: "t"}, Then: domain.PolicyAction{Decision: domain.PolicyDeny}},
			},
		},
	}
	_, err := NewAgentEngine(result)
	if err == nil {
		t.Fatal("expected error for priority conflict between agent and global rules")
	}
	if !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "priority") {
		t.Errorf("expected duplicate priority error, got: %v", err)
	}
}

func TestLoadDirSubdirectoriesIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "agent.yaml", validAgentPolicy)
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, subdir, "nested.yaml", validAgentPolicy2)

	result, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 agent config (subdirs ignored), got %d", len(result.Agents))
	}
}

func TestValidateRateLimitInvalidMax(t *testing.T) {
	_, err := Parse([]byte(`
rules:
  - id: bad-rate
    priority: 1
    when:
      action: tool.invoke
    then:
      decision: allow
    rate_limit:
      max: 0
      window: 1m
`))
	if err == nil {
		t.Error("expected error for rate_limit.max=0")
	}
}

func TestValidateRateLimitInvalidWindow(t *testing.T) {
	_, err := Parse([]byte(`
rules:
  - id: bad-window
    priority: 1
    when:
      action: tool.invoke
    then:
      decision: allow
    rate_limit:
      max: 10
      window: invalid
`))
	if err == nil {
		t.Error("expected error for invalid rate_limit.window")
	}
}

func TestValidateRateLimitValid(t *testing.T) {
	cfg, err := Parse([]byte(`
rules:
  - id: good-rate
    priority: 1
    when:
      action: tool.invoke
    then:
      decision: allow
    rate_limit:
      max: 10
      window: 1m
`))
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
	if cfg.Rules[0].RateLimit == nil {
		t.Fatal("expected rate limit to be parsed")
	}
	if cfg.Rules[0].RateLimit.Max != 10 {
		t.Errorf("expected max=10, got %d", cfg.Rules[0].RateLimit.Max)
	}
}

func TestAgentEngineGlobalAgentIDsScoping(t *testing.T) {
	result := &LoadDirResult{
		Agents: map[string]*PolicyConfig{},
		Global: &PolicyConfig{
			Rules: []domain.PolicyRule{
				{
					ID:       "research-agents-web",
					Priority: 50,
					When:     domain.PolicyCondition{Action: "tool.invoke", AgentIDs: []string{"researcher", "analyst"}, ToolID: "web.*"},
					Then:     domain.PolicyAction{Decision: domain.PolicyAllow, Reason: "research agents can use web tools"},
				},
			},
			Default: domain.PolicyAction{Decision: domain.PolicyDeny, Reason: "global default deny"},
		},
	}

	engine, err := NewAgentEngine(result)
	if err != nil {
		t.Fatalf("NewAgentEngine: %v", err)
	}

	// Matching agent_ids + tool
	r, _ := engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "analyst",
	})
	if r.Decision != domain.PolicyAllow {
		t.Errorf("expected allow for analyst+web.search, got %s", r.Decision)
	}

	// Non-matching agent_ids
	r, _ = engine.Evaluate(context.Background(), domain.PolicyInput{
		Action: "tool.invoke", ToolID: "web.search", AgentID: "deploy-bot",
	})
	if r.Decision != domain.PolicyDeny {
		t.Errorf("expected deny for deploy-bot+web.search, got %s", r.Decision)
	}
}
