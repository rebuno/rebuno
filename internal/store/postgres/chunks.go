package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

// Content-defined chunking: boundaries depend on the bytes around them, so an
// insertion leaves the chunks before and after it unchanged. Changing these
// constants or the gear table stops new payloads sharing chunks with old ones.
const (
	minChunk  = 512
	maxChunk  = 16 << 10
	chunkMask = 1<<11 - 1
)

// A chunk used within chunkTouchInterval is not written again, and cleanup
// keeps every chunk used within chunkGrace, so a transaction that reuses a
// chunk without writing it never sees it deleted.
const (
	chunkTouchInterval = time.Hour
	chunkGrace         = 24 * time.Hour
)

var gear = func() (t [256]uint64) {
	x := uint64(0x9e3779b97f4a7c15)
	for i := range t {
		x += 0x9e3779b97f4a7c15
		z := (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		t[i] = z ^ (z >> 31)
	}
	return t
}()

func splitChunks(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := chunkLen(data)
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func chunkLen(data []byte) int {
	if len(data) <= minChunk {
		return len(data)
	}
	end := min(len(data), maxChunk)
	var h uint64
	for i := minChunk; i < end; i++ {
		h = h<<1 + gear[data[i]]
		if h&chunkMask == 0 {
			return i + 1
		}
	}
	return end
}

func writeChunks(ctx context.Context, q Querier, data []byte) ([][]byte, error) {
	pieces := splitChunks(data)
	hashes := make([][]byte, len(pieces))
	pending := make(map[string][]byte, len(pieces))
	for i, p := range pieces {
		sum := sha256.Sum256(p)
		hashes[i] = sum[:]
		pending[string(sum[:])] = p
	}

	rows, err := q.Query(ctx, `
		SELECT hash FROM payload_chunks
		WHERE hash = ANY($1) AND used_at > now() - make_interval(secs => $2)
	`, hashes, chunkTouchInterval.Seconds())
	if err != nil {
		return nil, fmt.Errorf("find payload chunks: %w", err)
	}
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		delete(pending, string(h))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find payload chunks: %w", err)
	}
	if len(pending) == 0 {
		return hashes, nil
	}

	// Sorted so concurrent writers lock shared chunks in the same order.
	keys := make([]string, 0, len(pending))
	for h := range pending {
		keys = append(keys, h)
	}
	sort.Strings(keys)
	newHashes := make([][]byte, len(keys))
	newData := make([][]byte, len(keys))
	for i, h := range keys {
		newHashes[i] = []byte(h)
		newData[i] = pending[h]
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO payload_chunks (hash, data, used_at)
		SELECT hash, data, now() FROM unnest($1::bytea[], $2::bytea[]) AS c(hash, data)
		ORDER BY hash
		ON CONFLICT (hash) DO UPDATE SET used_at = now()
	`, newHashes, newData); err != nil {
		return nil, fmt.Errorf("write payload chunks: %w", err)
	}
	return hashes, nil
}

func readChunks(ctx context.Context, q Querier, lists ...[][]byte) ([][]byte, error) {
	var all [][]byte
	for _, l := range lists {
		all = append(all, l...)
	}
	stored := make(map[string][]byte, len(all))
	if len(all) > 0 {
		rows, err := q.Query(ctx, `SELECT hash, data FROM payload_chunks WHERE hash = ANY($1)`, all)
		if err != nil {
			return nil, fmt.Errorf("read payload chunks: %w", err)
		}
		for rows.Next() {
			var h, d []byte
			if err := rows.Scan(&h, &d); err != nil {
				rows.Close()
				return nil, err
			}
			stored[string(h)] = d
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read payload chunks: %w", err)
		}
	}
	out := make([][]byte, len(lists))
	for i, l := range lists {
		if l == nil {
			continue
		}
		var buf bytes.Buffer
		for _, h := range l {
			d, ok := stored[string(h)]
			if !ok {
				return nil, fmt.Errorf("payload chunk %x is missing", h)
			}
			buf.Write(d)
		}
		out[i] = buf.Bytes()
	}
	return out, nil
}

func (s *Store) DeleteUnreferencedChunks(ctx context.Context) error {
	return deleteUnreferencedChunks(ctx, s.q(ctx), chunkGrace)
}

func (q querier) DeleteUnreferencedChunks(ctx context.Context) error {
	return deleteUnreferencedChunks(ctx, q.q, chunkGrace)
}

func deleteUnreferencedChunks(ctx context.Context, q Querier, grace time.Duration) error {
	if _, err := q.Exec(ctx, `
		DELETE FROM payload_chunks c
		WHERE c.used_at < now() - make_interval(secs => $1)
		  AND NOT EXISTS (SELECT 1 FROM steps s WHERE s.args_chunks @> ARRAY[c.hash])
		  AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.state_chunks @> ARRAY[c.hash])
	`, grace.Seconds()); err != nil {
		return fmt.Errorf("delete unreferenced payload chunks: %w", err)
	}
	return nil
}
