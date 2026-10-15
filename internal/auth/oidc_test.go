package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
)

func TestOIDCAuthenticate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                issuer,
				"jwks_uri":                              issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	groupScopes, err := auth.ParseGroupScopes(`{"support": ["approvals:read", "approvals:write"]}`)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewOIDC(context.Background(), auth.OIDCConfig{
		Issuer: issuer, Audience: "rebuno-prod", UserClaim: "email", GroupsClaim: "groups", GroupScopes: groupScopes,
	})
	if err != nil {
		t.Fatal(err)
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	token := func(claims map[string]any) string {
		t.Helper()
		base := map[string]any{"iss": issuer, "aud": "rebuno-prod", "sub": "00u1", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
		for k, v := range claims {
			base[k] = v
		}
		payload, _ := json.Marshal(base)
		sig, err := signer.Sign(payload)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := sig.CompactSerialize()
		return raw
	}

	t.Run("a valid token names the user and grants its groups' scopes", func(t *testing.T) {
		ctx, err := verifier.Authenticate(context.Background(), token(map[string]any{"email": "alice@corp.com", "groups": []string{"support", "unmapped"}}))
		if err != nil {
			t.Fatal(err)
		}
		if got := auth.Identity(ctx); got != "user:alice@corp.com" {
			t.Errorf("identity: got %q", got)
		}
		if !auth.HasScope(ctx, domain.ScopeApprovalsWrite) || auth.HasScope(ctx, domain.ScopeAPIKeysManage) {
			t.Error("scopes do not match the support group mapping")
		}
	})

	t.Run("a token for another deployment is rejected", func(t *testing.T) {
		_, err := verifier.Authenticate(context.Background(), token(map[string]any{"aud": "rebuno-staging", "email": "alice@corp.com"}))
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("got %v, want ErrUnauthorized", err)
		}
	})

	t.Run("a token without the user claim is rejected", func(t *testing.T) {
		_, err := verifier.Authenticate(context.Background(), token(nil))
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("got %v, want ErrUnauthorized", err)
		}
	})
}
