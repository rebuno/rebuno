package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
)

var _ store.APIKeyStore = (*Store)(nil)

const apiKeyColumns = "id, name, scopes, secret_hash, created_at, revoked_at"

func scanAPIKey(row pgx.Row) (domain.APIKey, error) {
	var k domain.APIKey
	var scopes []byte
	if err := row.Scan(&k.ID, &k.Name, &scopes, &k.SecretHash, &k.CreatedAt, &k.RevokedAt); err != nil {
		return k, mapNotFound(err)
	}
	if err := json.Unmarshal(scopes, &k.Scopes); err != nil {
		return domain.APIKey{}, err
	}
	return k, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, k domain.APIKey) error {
	scopes, err := json.Marshal(k.Scopes)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO api_keys (id, name, scopes, secret_hash, created_at, revoked_at) VALUES ($1,$2,$3,$4,$5,$6)`, k.ID, k.Name, string(scopes), k.SecretHash, k.CreatedAt, k.RevokedAt)
	if isUniqueViolation(err) {
		return domain.ErrConflict
	}
	return err
}

func (s *Store) GetAPIKey(ctx context.Context, id string) (domain.APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, "SELECT "+apiKeyColumns+" FROM api_keys WHERE id=$1", id))
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+apiKeyColumns+" FROM api_keys ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.APIKey, 0)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) RotateAPIKey(ctx context.Context, id string, oldHash, newHash []byte) error {
	res, err := s.pool.Exec(ctx, `UPDATE api_keys SET secret_hash=$3 WHERE id=$1 AND secret_hash=$2 AND revoked_at IS NULL`, id, oldHash, newHash)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		if _, err := s.GetAPIKey(ctx, id); err != nil {
			return err
		}
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string, at time.Time) error {
	res, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,$2) WHERE id=$1`, id, at)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
