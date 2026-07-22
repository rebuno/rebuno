package stream

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const notifyChannel = "rebuno_stream"

type envelope struct {
	ExecID uuid.UUID `json:"exec_id"`
	Delta  Delta     `json:"delta"`
}

// PostgresBus fans deltas across replicas via pg_notify. Deltas are ephemeral:
// pg_notify does not buffer, so a delta published while no replica is listening
// is simply dropped — the event ledger stays the source of truth.
type PostgresBus struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewPostgresBus(pool *pgxpool.Pool, log *slog.Logger) *PostgresBus {
	if log == nil {
		log = slog.Default()
	}
	return &PostgresBus{pool: pool, log: log}
}

func (b *PostgresBus) Publish(ctx context.Context, execID uuid.UUID, d Delta) error {
	payload, err := json.Marshal(envelope{ExecID: execID, Delta: d})
	if err != nil {
		return err
	}
	_, err = b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", notifyChannel, string(payload))
	return err
}

// Start holds one dedicated connection on LISTEN rebuno_stream and delivers
// every notification. It reconnects on error until ctx is cancelled.
func (b *PostgresBus) Start(ctx context.Context, deliver func(execID uuid.UUID, d Delta)) error {
	for ctx.Err() == nil {
		if err := b.listen(ctx, deliver); err != nil && ctx.Err() == nil {
			b.log.Warn("stream listen dropped, reconnecting", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}
	return ctx.Err()
}

func (b *PostgresBus) listen(ctx context.Context, deliver func(execID uuid.UUID, d Delta)) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var e envelope
		if err := json.Unmarshal([]byte(n.Payload), &e); err != nil {
			b.log.Warn("bad stream payload", "error", err)
			continue
		}
		deliver(e.ExecID, e.Delta)
	}
}
