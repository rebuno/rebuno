package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/policy"
)

const minLeaseTimeout = 10 * time.Second

func (k *Kernel) RegisterAgent(ctx context.Context, agent domain.Agent) error {
	if err := k.cfg.Access.Authorize(ctx, auth.RoleAdmin, agent.ID); err != nil {
		return err
	}
	if err := validatePolicyBundle(agent.PolicyBundle); err != nil {
		return err
	}
	if agent.LeaseTimeoutSeconds < minLeaseTimeout.Seconds() {
		agent.LeaseTimeoutSeconds = 0
	}
	return k.d.Agents.RegisterAgent(ctx, agent)
}

func validatePolicyBundle(bundle string) error {
	if bundle == "" {
		return nil
	}
	if _, err := policy.NewRuleEngineFromBundle(bundle); err != nil {
		return fmt.Errorf("%w: invalid policy bundle: %v", domain.ErrValidation, err)
	}
	return nil
}

func (k *Kernel) SetAgentMetadata(ctx context.Context, agentID string, metadata domain.AgentMetadata) error {
	if err := k.cfg.Access.Authorize(ctx, auth.RoleAdmin, agentID); err != nil {
		return err
	}
	for _, schema := range []*json.RawMessage{&metadata.InputSchema, &metadata.OutputSchema} {
		if bytes.Equal(bytes.TrimSpace(*schema), []byte("null")) {
			*schema = nil
		}
		if _, err := compileSchema(*schema); err != nil {
			return err
		}
	}
	return k.d.Agents.SetAgentMetadata(ctx, agentID, metadata)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if raw == nil {
		return nil, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid schema: %v", domain.ErrValidation, err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource("urn:rebuno:schema", doc); err != nil {
		return nil, fmt.Errorf("%w: invalid schema: %v", domain.ErrValidation, err)
	}
	schema, err := c.Compile("urn:rebuno:schema")
	if err != nil {
		return nil, fmt.Errorf("%w: invalid schema: %v", domain.ErrValidation, err)
	}
	return schema, nil
}

func validateInput(agent domain.Agent, input json.RawMessage) error {
	schema, err := compileSchema(agent.InputSchema)
	if err != nil || schema == nil {
		return err
	}
	if len(input) == 0 {
		input = json.RawMessage("null")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil {
		return fmt.Errorf("%w: invalid input: %v", domain.ErrValidation, err)
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("%w: input does not match the agent's input_schema: %v", domain.ErrValidation, err)
	}
	return nil
}

// LookupAgent reads an agent without authorization, for verifying its request signatures.
func (k *Kernel) LookupAgent(ctx context.Context, id string) (domain.Agent, error) {
	return k.d.Agents.GetAgent(ctx, id)
}

func (k *Kernel) GetAgent(ctx context.Context, id string) (domain.Agent, error) {
	if err := k.cfg.Access.Authorize(ctx, auth.RoleRead, id); err != nil {
		return domain.Agent{}, err
	}
	return k.d.Agents.GetAgent(ctx, id)
}

func (k *Kernel) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	agents, err := k.d.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	readable, all := k.cfg.Access.ReadableAgents(ctx)
	if all {
		return agents, nil
	}
	return slices.DeleteFunc(agents, func(a domain.Agent) bool { return !slices.Contains(readable, a.ID) }), nil
}

func (k *Kernel) DeleteAgent(ctx context.Context, id string) error {
	if err := k.cfg.Access.Authorize(ctx, auth.RoleAdmin, id); err != nil {
		return err
	}
	return k.d.Agents.DeleteAgent(ctx, id)
}

func (k *Kernel) LoadPolicyBundle(ctx context.Context, agentID string, bundle string) error {
	if err := k.cfg.Access.Authorize(ctx, auth.RoleAdmin, agentID); err != nil {
		return err
	}
	if err := validatePolicyBundle(bundle); err != nil {
		return fmt.Errorf("load policy bundle: %w", err)
	}
	if err := k.d.Agents.SetPolicyBundle(ctx, agentID, bundle); err != nil {
		return fmt.Errorf("load policy bundle: %w", err)
	}
	return nil
}
