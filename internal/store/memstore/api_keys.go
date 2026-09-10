package memstore

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

var _ store.APIKeyStore = (*Store)(nil)

func cloneAPIKey(k domain.APIKey) domain.APIKey {
	k.Scopes = slices.Clone(k.Scopes)
	k.SecretHash = bytes.Clone(k.SecretHash)
	if k.RevokedAt != nil {
		at := *k.RevokedAt
		k.RevokedAt = &at
	}
	return k
}

func (s *Store) CreateAPIKey(ctx context.Context, key domain.APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apiKeys[key.ID]; ok {
		return domain.ErrConflict
	}
	s.apiKeys[key.ID] = cloneAPIKey(key)
	return nil
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (domain.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.apiKeys[id]
	if !ok {
		return domain.APIKey{}, domain.ErrNotFound
	}
	return cloneAPIKey(k), nil
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.APIKey, 0, len(s.apiKeys))
	for _, k := range s.apiKeys {
		out = append(out, cloneAPIKey(k))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) RotateAPIKey(ctx context.Context, id string, oldHash, newHash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.apiKeys[id]
	if !ok {
		return domain.ErrNotFound
	}
	if k.RevokedAt != nil || !bytes.Equal(k.SecretHash, oldHash) {
		return domain.ErrConflict
	}
	k.SecretHash = bytes.Clone(newHash)
	s.apiKeys[id] = k
	return nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.apiKeys[id]
	if !ok {
		return domain.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = &at
		s.apiKeys[id] = k
	}
	return nil
}
