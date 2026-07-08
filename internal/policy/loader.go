package policy

import (
	"gopkg.in/yaml.v3"
)

// LoadBundle parses a raw YAML policy bundle into a policy Config.
func LoadBundle(bundleYAML string) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(bundleYAML), &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
