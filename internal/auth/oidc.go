package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/rebuno/rebuno/internal/domain"
)

type OIDCConfig struct {
	Issuer      string
	Audience    string
	UserClaim   string
	GroupsClaim string
	GroupScopes map[string][]domain.Scope
}

type OIDC struct {
	verifier *oidc.IDTokenVerifier
	cfg      OIDCConfig
}

func NewOIDC(ctx context.Context, cfg OIDCConfig) (*OIDC, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.Issuer, err)
	}
	return &OIDC{verifier: provider.Verifier(&oidc.Config{ClientID: cfg.Audience}), cfg: cfg}, nil
}

func (o *OIDC) Authenticate(ctx context.Context, raw string) (context.Context, error) {
	tok, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, domain.ErrUnauthorized
	}
	var claims map[string]any
	if err := tok.Claims(&claims); err != nil {
		return nil, domain.ErrUnauthorized
	}
	user, _ := claims[o.cfg.UserClaim].(string)
	if user == "" {
		return nil, domain.ErrUnauthorized
	}
	groups, _ := claims[o.cfg.GroupsClaim].([]any)
	var scopes []domain.Scope
	for _, g := range groups {
		if name, ok := g.(string); ok {
			scopes = append(scopes, o.cfg.GroupScopes[name]...)
		}
	}
	return WithUser(ctx, user, scopes), nil
}

func ParseGroupScopes(raw string) (map[string][]domain.Scope, error) {
	if raw == "" {
		return nil, nil
	}
	var in map[string][]string
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("group scopes: %w", err)
	}
	out := make(map[string][]domain.Scope, len(in))
	for group, names := range in {
		for _, name := range names {
			if name == "*" {
				out[group] = append(out[group], domain.AllScopes...)
				continue
			}
			if s := domain.Scope(name); s.Valid() {
				out[group] = append(out[group], s)
				continue
			}
			return nil, fmt.Errorf("group scopes: group %q has unknown scope %q", group, name)
		}
		slices.Sort(out[group])
		out[group] = slices.Compact(out[group])
	}
	return out, nil
}
