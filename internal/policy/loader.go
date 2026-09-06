package policy

import (
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Unknown keys are rejected: a misspelled predicate would be ignored, and a
// rule whose constraint vanished matches every input.
func LoadBundle(bundleYAML string) (Config, error) {
	dec := yaml.NewDecoder(strings.NewReader(bundleYAML))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}
	return cfg, nil
}
