package kernel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

type CreateAPIKeyRequest struct {
	Name   string         `json:"name"`
	Scopes []domain.Scope `json:"scopes"`
}

type IssuedAPIKey struct {
	domain.APIKey
	Token string `json:"token"`
}

func issueAPIKey(id string) (string, []byte) {
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	token := "rbk_" + id + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	return token, hash[:]
}

func (k *Kernel) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (IssuedAPIKey, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 200 || len(req.Scopes) == 0 {
		return IssuedAPIKey{}, fmt.Errorf("%w: name and scopes are required", domain.ErrValidation)
	}
	scopes := slices.Clone(req.Scopes)
	for _, scope := range scopes {
		if !scope.Valid() {
			return IssuedAPIKey{}, fmt.Errorf("%w: unknown scope %q", domain.ErrValidation, scope)
		}
	}
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	key := domain.APIKey{ID: uuid.Must(uuid.NewV7()).String(), Name: name, Scopes: scopes, CreatedAt: time.Now().UTC()}
	token, hash := issueAPIKey(key.ID)
	key.SecretHash = hash
	if err := k.d.APIKeys.CreateAPIKey(ctx, key); err != nil {
		return IssuedAPIKey{}, err
	}
	return IssuedAPIKey{APIKey: key, Token: token}, nil
}

func (k *Kernel) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	return k.d.APIKeys.ListAPIKeys(ctx)
}

func (k *Kernel) RotateAPIKey(ctx context.Context, id string) (IssuedAPIKey, error) {
	key, err := k.d.APIKeys.GetAPIKey(ctx, id)
	if err != nil {
		return IssuedAPIKey{}, err
	}
	if key.RevokedAt != nil {
		return IssuedAPIKey{}, domain.ErrConflict
	}
	token, hash := issueAPIKey(id)
	if err := k.d.APIKeys.RotateAPIKey(ctx, id, key.SecretHash, hash); err != nil {
		return IssuedAPIKey{}, err
	}
	key.SecretHash = hash
	return IssuedAPIKey{APIKey: key, Token: token}, nil
}

func (k *Kernel) RevokeAPIKey(ctx context.Context, id string) error {
	return k.d.APIKeys.RevokeAPIKey(ctx, id, time.Now().UTC())
}

func (k *Kernel) AuthenticateAPIKey(ctx context.Context, token string) (domain.APIKey, error) {
	id, secret, ok := strings.Cut(strings.TrimPrefix(token, "rbk_"), ".")
	if !strings.HasPrefix(token, "rbk_") || !ok || len(secret) != 43 {
		return domain.APIKey{}, domain.ErrUnauthorized
	}
	if _, err := uuid.Parse(id); err != nil {
		return domain.APIKey{}, domain.ErrUnauthorized
	}
	key, err := k.d.APIKeys.GetAPIKey(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.APIKey{}, domain.ErrUnauthorized
	}
	if err != nil {
		return domain.APIKey{}, err
	}
	hash := sha256.Sum256([]byte(token))
	if key.RevokedAt != nil || subtle.ConstantTimeCompare(hash[:], key.SecretHash) != 1 {
		return domain.APIKey{}, domain.ErrUnauthorized
	}
	return key, nil
}
