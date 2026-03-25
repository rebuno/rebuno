package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rebuno/rebuno/internal/domain"
	"gopkg.in/yaml.v3"
)

type PolicyConfig struct {
	Rules   []domain.PolicyRule `yaml:"rules"`
	Default domain.PolicyAction `yaml:"default"`
}

func Load(path string) (*PolicyConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading policy file %s: %w", path, err)
	}
	return Parse(data)
}

// LoadDirResult holds the output of LoadDir: per-agent configs and an optional
// global config loaded from _global.yaml/_global.yml.
type LoadDirResult struct {
	Agents map[string]*PolicyConfig
	Global *PolicyConfig // nil if no _global file exists
}

func LoadDir(dir string) (*LoadDirResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading policy directory %s: %w", dir, err)
	}

	result := &LoadDirResult{Agents: make(map[string]*PolicyConfig)}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading policy file %s: %w", path, err)
		}

		cfg, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}

		if name == "_global" {
			result.Global = cfg
		} else {
			result.Agents[name] = cfg
		}
	}

	if len(result.Agents) == 0 && result.Global == nil {
		return nil, fmt.Errorf("no .yaml or .yml files found in policy directory %s", dir)
	}

	return result, nil
}

func Parse(data []byte) (*PolicyConfig, error) {
	var cfg PolicyConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing policy YAML: %w", err)
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validate(cfg *PolicyConfig) error {
	seen := make(map[string]bool)
	for _, r := range cfg.Rules {
		if r.ID == "" {
			return fmt.Errorf("%w: rule missing ID", domain.ErrInvalidConfiguration)
		}
		if seen[r.ID] {
			return fmt.Errorf("%w: duplicate rule ID %q", domain.ErrInvalidConfiguration, r.ID)
		}
		seen[r.ID] = true
		if r.Then.Decision != domain.PolicyAllow && r.Then.Decision != domain.PolicyDeny && r.Then.Decision != domain.PolicyRequireApproval {
			return fmt.Errorf("%w: rule %q has invalid decision %q", domain.ErrInvalidConfiguration, r.ID, r.Then.Decision)
		}
		for i, pred := range r.When.Arguments {
			if pred.Field == "" {
				return fmt.Errorf("%w: rule %q argument predicate %d has empty field",
					domain.ErrInvalidConfiguration, r.ID, i)
			}
			if pred.Pattern == "" && len(pred.OneOf) == 0 && pred.Min == nil && pred.Max == nil && pred.MaxLength == nil && !pred.Required {
				return fmt.Errorf("%w: rule %q argument predicate %d (field %q) has no constraints",
					domain.ErrInvalidConfiguration, r.ID, i, pred.Field)
			}
		}
		if r.When.MinStepCount != nil && *r.When.MinStepCount < 0 {
			return fmt.Errorf("%w: rule %q min_step_count must be non-negative",
				domain.ErrInvalidConfiguration, r.ID)
		}
		if r.When.MaxDurationMs != nil && *r.When.MaxDurationMs <= 0 {
			return fmt.Errorf("%w: rule %q max_duration_ms must be positive",
				domain.ErrInvalidConfiguration, r.ID)
		}
		if r.When.Schedule != "" {
			if _, err := parseSchedule(r.When.Schedule); err != nil {
				return fmt.Errorf("%w: rule %q has invalid schedule %q: %v",
					domain.ErrInvalidConfiguration, r.ID, r.When.Schedule, err)
			}
		}
	}
	if cfg.Default.Decision != "" && cfg.Default.Decision != domain.PolicyAllow && cfg.Default.Decision != domain.PolicyDeny && cfg.Default.Decision != domain.PolicyRequireApproval {
		return fmt.Errorf("%w: default has invalid decision %q", domain.ErrInvalidConfiguration, cfg.Default.Decision)
	}
	return nil
}
