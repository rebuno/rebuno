package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/domain"
)

func conversation(turns int) []byte {
	rng := rand.New(rand.NewPCG(1, 2))
	messages := make([]map[string]string, turns)
	for i := range messages {
		text := make([]byte, 800+rng.IntN(3000))
		for j := range text {
			text[j] = byte('a' + rng.IntN(26))
		}
		messages[i] = map[string]string{"role": "user", "content": string(text)}
	}
	body, _ := json.Marshal(map[string]any{"model": "m", "messages": messages, "tools": []string{"shell", "read_file"}})
	return body
}

func TestChunksBeforeAnInsertionAreUnchanged(t *testing.T) {
	before := splitChunks(conversation(40))
	after := splitChunks(conversation(41))
	if !bytes.Equal(bytes.Join(after, nil), conversation(41)) {
		t.Fatal("chunks do not reassemble the payload")
	}
	shared := 0
	for shared < len(before) && bytes.Equal(before[shared], after[shared]) {
		shared++
	}
	if shared < len(before)-2 {
		t.Fatalf("an appended message changed %d of %d earlier chunks", len(before)-shared, len(before))
	}
	for _, c := range after[:len(after)-1] {
		if len(c) < minChunk || len(c) > maxChunk {
			t.Fatalf("chunk of %d bytes is outside [%d, %d]", len(c), minChunk, maxChunk)
		}
	}
}

func TestLLMArgsShareChunksAcrossSteps(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()
	s := NewStore(pool)
	agentID := "chunk-agent-" + uuid.NewString()
	if err := s.RegisterAgent(ctx, domain.Agent{ID: agentID, WebhookURL: "http://localhost/wh", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	exec := domain.Execution{ID: uuid.Must(uuid.NewV7()), AgentID: agentID, Input: json.RawMessage(`{}`), Status: domain.ExecutionPending, CreatedAt: time.Now().Add(-48 * time.Hour)}
	if err := s.CreateExecution(ctx, exec); err != nil {
		t.Fatal(err)
	}
	var hashes [][]byte
	for _, c := range append(splitChunks(conversation(40)), splitChunks(conversation(41))...) {
		sum := sha256.Sum256(c)
		hashes = append(hashes, sum[:])
	}
	countChunks := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM payload_chunks WHERE hash = ANY($1)`, hashes).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for turn, args := range [][]byte{conversation(40), conversation(41)} {
		step := domain.Step{
			StepID: fmt.Sprintf("%s-%d", exec.ID, turn), ExecutionID: exec.ID, Kind: domain.StepKindLLM,
			Target: "m", ArgsHash: fmt.Sprint(turn), Status: domain.StepExecuting, Idempotency: "safe_to_retry", Args: args,
		}
		if err := s.Upsert(ctx, step); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetStep(ctx, step.StepID)
		if err != nil || !bytes.Equal(got.Args, args) {
			t.Fatalf("turn %d args did not round-trip: %v", turn, err)
		}
	}
	first := len(splitChunks(conversation(40)))
	if added := countChunks(); added >= 2*first || added <= first {
		t.Fatalf("two overlapping payloads of %d chunks each stored %d chunks", first, added)
	}
	steps, err := s.ListByExecution(ctx, exec.ID)
	if err != nil || len(steps) != 2 || !bytes.Equal(steps[1].Args, conversation(41)) {
		t.Fatalf("listed args did not round-trip: %d steps, %v", len(steps), err)
	}

	if err := s.UpdateExecutionStatus(ctx, exec.ID, domain.ExecutionCompleted, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExecutionsCreatedBefore(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := deleteUnreferencedChunks(ctx, pool, 0); err != nil {
		t.Fatal(err)
	}
	if n := countChunks(); n != 0 {
		t.Fatalf("%d unreferenced chunks survived cleanup", n)
	}
}
