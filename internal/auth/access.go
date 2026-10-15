package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rebuno/rebuno/internal/domain"
)

type Role string

const (
	RoleRead    Role = "read"
	RoleWrite   Role = "write"
	RoleApprove Role = "approve"
	RoleAdmin   Role = "admin"
)

const AllAgents = "*"

// A nil Access grants every authenticated client every role.
type Access map[string]map[Role][]string

func ParseAccess(raw []byte) (Access, error) {
	a := Access{}
	if err := yaml.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse access config: %w", err)
	}
	for agent, roles := range a {
		for role, principals := range roles {
			switch role {
			case RoleRead, RoleWrite, RoleApprove, RoleAdmin:
			default:
				return nil, fmt.Errorf("agent %q: unknown role %q", agent, role)
			}
			for _, p := range principals {
				if !ValidPrincipal(p) {
					return nil, fmt.Errorf("agent %q role %s: principal %q needs a user, service, or group prefix", agent, role, p)
				}
			}
		}
	}
	return a, nil
}

func grants(have, need Role) bool {
	return have == need || have == RoleAdmin || need == RoleRead
}

func ValidPrincipal(entry string) bool {
	kind, name, ok := strings.Cut(entry, ":")
	return ok && name != "" && (kind == "user" || kind == "service" || kind == "group")
}

func MatchPrincipal(entries []string, id string, groups []string) bool {
	for _, e := range entries {
		if e == id {
			return true
		}
		if g, ok := strings.CutPrefix(e, "group:"); ok && slices.Contains(groups, g) {
			return true
		}
	}
	return false
}

func (p principal) matches(entries []string) bool {
	return MatchPrincipal(entries, p.identity(), p.groups)
}

func (a Access) allows(p principal, need Role, agentID string) bool {
	for _, key := range []string{AllAgents, agentID} {
		for role, entries := range a[key] {
			if grants(role, need) && p.matches(entries) {
				return true
			}
		}
	}
	return false
}

func (a Access) Authorize(ctx context.Context, need Role, agentID string) error {
	p, ok := ctx.Value(principalKey{}).(principal)
	switch {
	case !ok:
		return domain.ErrUnauthorized
	case p.admin:
		return nil
	case p.agentID != "":
		if p.agentID == agentID {
			return nil
		}
	case p.client && (a == nil || a.allows(p, need, agentID)):
		return nil
	}
	return domain.ErrForbidden
}

// ReadableAgents lists the agents the caller may read; all reports that no restriction applies.
func (a Access) ReadableAgents(ctx context.Context) (agents []string, all bool) {
	p, ok := ctx.Value(principalKey{}).(principal)
	switch {
	case !ok:
		return nil, false
	case p.admin, p.client && a == nil:
		return nil, true
	case p.agentID != "":
		return []string{p.agentID}, false
	case !p.client:
		return nil, false
	case a.allows(p, RoleRead, AllAgents):
		return nil, true
	}
	agents = []string{}
	for agent := range a {
		if agent != AllAgents && a.allows(p, RoleRead, agent) {
			agents = append(agents, agent)
		}
	}
	slices.Sort(agents)
	return agents, false
}

func Matches(ctx context.Context, entries []string) bool {
	p, ok := ctx.Value(principalKey{}).(principal)
	return ok && p.matches(entries)
}
