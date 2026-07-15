package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

// agentConfigFile is the declarative provisioning manifest passed via --config.
// It lists the agents the kernel should register (upsert) on boot, each with
// its policy bundle. Loaded by both `dev` and `server`.
type agentConfigFile struct {
	Agents []agentConfigEntry `yaml:"agents"`
}

type agentConfigEntry struct {
	ID         string `yaml:"id"`
	WebhookURL string `yaml:"webhook_url"`
	Secret     string `yaml:"secret"`
	Policy     string `yaml:"policy"`      // inline bundle (literal block)
	PolicyFile string `yaml:"policy_file"` // path, relative to the config file
}

func expandEnv(s string) string {
	return os.Expand(s, func(k string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return "$" + k
	})
}

// loadAgentConfig parses a provisioning manifest into agents ready to register.
// policy_file paths are resolved relative to the manifest, and every bundle is
// validated up front so a malformed policy fails the boot instead of silently
// falling back to the permissive engine at evaluation time.
func loadAgentConfig(path string) ([]domain.Agent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	raw = []byte(expandEnv(string(raw)))
	var f agentConfigFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	baseDir := filepath.Dir(path)
	agents := make([]domain.Agent, 0, len(f.Agents))
	for i, a := range f.Agents {
		if a.ID == "" {
			return nil, fmt.Errorf("agents[%d]: id is required", i)
		}
		if a.Policy != "" && a.PolicyFile != "" {
			return nil, fmt.Errorf("agent %q: set policy OR policy_file, not both", a.ID)
		}

		bundle := a.Policy
		if a.PolicyFile != "" {
			pf := a.PolicyFile
			if !filepath.IsAbs(pf) {
				pf = filepath.Join(baseDir, pf)
			}
			b, err := os.ReadFile(pf)
			if err != nil {
				return nil, fmt.Errorf("agent %q: read policy_file: %w", a.ID, err)
			}
			bundle = string(b)
		}

		if bundle != "" {
			if _, err := policy.NewRuleEngineFromBundle(bundle); err != nil {
				return nil, fmt.Errorf("agent %q: invalid policy bundle: %w", a.ID, err)
			}
		}

		agents = append(agents, domain.Agent{
			ID:           a.ID,
			WebhookURL:   a.WebhookURL,
			Secret:       a.Secret,
			PolicyBundle: bundle,
		})
	}
	return agents, nil
}

// registerAgents upserts each agent from the manifest. RegisterAgent is an
// upsert, so the policy bundle is persisted alongside the agent in one call and
// re-applying the manifest on every boot is idempotent. It is additive: agents
// not in the manifest (e.g. registered at runtime via the admin API) are left
// untouched.
func registerAgents(ctx context.Context, r interface {
	RegisterAgent(context.Context, domain.Agent) error
}, agents []domain.Agent) error {
	for _, a := range agents {
		if err := r.RegisterAgent(ctx, a); err != nil {
			return fmt.Errorf("provision agent %q: %w", a.ID, err)
		}
	}
	return nil
}
