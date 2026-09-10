package auth

import (
	"context"
	"slices"

	"github.com/rebuno/rebuno/internal/domain"
)

type principalKey struct{}
type principal struct {
	agentID string
	admin   bool
	scopes  []domain.Scope
	client  bool
}

func WithAgent(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, principalKey{}, principal{agentID: agentID})
}

// WithAdmin grants deployment-wide resource access after bearer authentication.
func WithAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, principalKey{}, principal{admin: true})
}

func AuthorizeAgent(ctx context.Context, agentID string) error {
	p, ok := ctx.Value(principalKey{}).(principal)
	if !ok {
		return domain.ErrUnauthorized
	}
	if p.admin || p.client || (p.agentID != "" && p.agentID == agentID) {
		return nil
	}
	return domain.ErrForbidden
}

func WithClient(ctx context.Context, scopes []domain.Scope) context.Context {
	return context.WithValue(ctx, principalKey{}, principal{client: true, scopes: slices.Clone(scopes)})
}

func HasScope(ctx context.Context, scope domain.Scope) bool {
	p, ok := ctx.Value(principalKey{}).(principal)
	return ok && (p.admin || (p.client && slices.Contains(p.scopes, scope)))
}
