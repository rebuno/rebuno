package hub

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/rebuno/rebuno/internal/store"
)

func testMsg(t string) store.AgentMessage {
	return store.AgentMessage{Type: t, Payload: json.RawMessage(`{}`)}
}

func TestRegisterUnregister(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")
	if conn == nil {
		t.Fatal("expected non-nil conn")
	}
	if !h.HasConnections("agent-1") {
		t.Fatal("expected connections for agent-1")
	}

	h.Unregister("agent-1", "c1", conn.Generation())
	if h.HasConnections("agent-1") {
		t.Fatal("expected no connections for agent-1 after unregister")
	}
}

func TestSendRouting(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")

	ok := h.Send("agent-1", testMsg("test"))
	if !ok {
		t.Fatal("expected send to succeed")
	}

	select {
	case msg := <-conn.EventCh:
		if msg.Type != "test" {
			t.Fatalf("expected type 'test', got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestSendNoConnections(t *testing.T) {
	h := New(nil)
	defer h.Close()

	ok := h.Send("nonexistent", testMsg("test"))
	if ok {
		t.Fatal("expected send to return false for missing agent")
	}
}

func TestSendTo(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn1 := h.Register("agent-1", "c1")
	conn2 := h.Register("agent-1", "c2")

	ok := h.SendTo("c2", "agent-1", testMsg("targeted"))
	if !ok {
		t.Fatal("expected SendTo to succeed")
	}

	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "targeted" {
			t.Fatalf("expected type 'targeted', got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on c2 channel")
	}

	select {
	case <-conn1.EventCh:
		t.Fatal("expected no message on c1 channel")
	default:
	}
}

func TestSendToSession(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")
	h.SetSession("agent-1", "c1", "session-abc")

	ok := h.SendToSession("session-abc", testMsg("session-msg"))
	if !ok {
		t.Fatal("expected SendToSession to succeed")
	}

	select {
	case msg := <-conn.EventCh:
		if msg.Type != "session-msg" {
			t.Fatalf("expected type 'session-msg', got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestSendToSessionNotFound(t *testing.T) {
	h := New(nil)
	defer h.Close()

	ok := h.SendToSession("nonexistent", testMsg("test"))
	if ok {
		t.Fatal("expected SendToSession to return false for missing session")
	}
}

func TestPickConnection(t *testing.T) {
	h := New(nil)
	defer h.Close()

	h.Register("agent-1", "c1")

	info, ok := h.PickConnection("agent-1")
	if !ok {
		t.Fatal("expected PickConnection to succeed")
	}
	if info.ConsumerID != "c1" {
		t.Fatalf("expected consumer_id 'c1', got %q", info.ConsumerID)
	}
}

func TestPickConnectionNone(t *testing.T) {
	h := New(nil)
	defer h.Close()

	_, ok := h.PickConnection("nonexistent")
	if ok {
		t.Fatal("expected PickConnection to return false for missing agent")
	}
}

func TestRoundRobin(t *testing.T) {
	h := New(nil)
	defer h.Close()

	h.Register("agent-1", "c1")
	h.Register("agent-1", "c2")

	seen := make(map[string]int)
	for i := 0; i < 10; i++ {
		info, ok := h.PickConnection("agent-1")
		if !ok {
			t.Fatal("expected PickConnection to succeed")
		}
		seen[info.ConsumerID]++
	}

	if len(seen) < 2 {
		t.Fatal("expected round-robin to distribute across consumers")
	}
	for consumerID, count := range seen {
		if count < 4 {
			t.Fatalf("expected consumer %s to get at least 4 of 10 dispatches, got %d", consumerID, count)
		}
	}
}

func TestSessionClearedOnUnregister(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")
	h.SetSession("agent-1", "c1", "session-xyz")

	if ok := h.SendToSession("session-xyz", testMsg("test")); !ok {
		t.Fatal("expected session to be reachable")
	}

	h.Unregister("agent-1", "c1", conn.Generation())

	if ok := h.SendToSession("session-xyz", testMsg("test")); ok {
		t.Fatal("expected session to be unreachable after unregister")
	}
}

func TestReRegisterSameConsumer(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn1 := h.Register("agent-1", "c1")
	h.SetSession("agent-1", "c1", "session-1")

	conn2 := h.Register("agent-1", "c1")

	if conn1 == conn2 {
		t.Fatal("expected new connection object")
	}

	if ok := h.SendToSession("session-1", testMsg("test")); ok {
		t.Fatal("expected old session to be gone after re-register")
	}

	ok := h.Send("agent-1", testMsg("test"))
	if !ok {
		t.Fatal("expected send to succeed")
	}

	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "test" {
			t.Fatalf("expected type 'test', got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on new connection")
	}
}

func TestConcurrentAccess(t *testing.T) {
	h := New(nil)
	defer h.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%d", id)
			consumerID := fmt.Sprintf("c%d", id)
			conn := h.Register(agentID, consumerID)
			h.Send(agentID, testMsg("concurrent"))
			<-conn.EventCh
			h.Unregister(agentID, consumerID, conn.Generation())
		}(i)
	}
	wg.Wait()
}

func TestConnOverflow(t *testing.T) {
	c := NewConn("agent-1", "c1")

	for i := 0; i < eventChannelSize; i++ {
		if !c.Send(testMsg("fill")) {
			t.Fatalf("expected send to succeed at index %d", i)
		}
	}

	if c.Send(testMsg("overflow")) {
		t.Fatal("expected send to fail when channel is full")
	}
}

func TestUnregisterStaleGeneration(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn1 := h.Register("agent-1", "c1")
	staleGen := conn1.Generation()

	// Re-register bumps generation
	conn2 := h.Register("agent-1", "c1")

	// Unregister with stale generation should be a no-op
	h.Unregister("agent-1", "c1", staleGen)

	if !h.HasConnections("agent-1") {
		t.Fatal("expected connection to survive stale unregister")
	}

	// Current connection should still work
	ok := h.Send("agent-1", testMsg("alive"))
	if !ok {
		t.Fatal("expected send to succeed")
	}
	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "alive" {
			t.Fatalf("expected type 'alive', got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestSendFullChannelEvictsConnection(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")

	// Fill the channel
	for i := 0; i < eventChannelSize; i++ {
		conn.EventCh <- testMsg("fill")
	}

	// Send should fail and evict the connection
	ok := h.Send("agent-1", testMsg("overflow"))
	if ok {
		t.Fatal("expected send to return false for full channel")
	}

	if h.HasConnections("agent-1") {
		t.Fatal("expected connection to be evicted after overflow")
	}
}

func TestSendFullChannelEvictsSessionToo(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")
	h.SetSession("agent-1", "c1", "session-1")

	for i := 0; i < eventChannelSize; i++ {
		conn.EventCh <- testMsg("fill")
	}

	h.Send("agent-1", testMsg("overflow"))

	if ok := h.SendToSession("session-1", testMsg("test")); ok {
		t.Fatal("expected session to be cleaned up after overflow eviction")
	}
}

func TestSetSessionReplacePrevious(t *testing.T) {
	h := New(nil)
	defer h.Close()

	h.Register("agent-1", "c1")
	h.SetSession("agent-1", "c1", "session-old")

	if ok := h.SendToSession("session-old", testMsg("test")); !ok {
		t.Fatal("expected old session to be reachable")
	}

	h.SetSession("agent-1", "c1", "session-new")

	if ok := h.SendToSession("session-old", testMsg("test")); ok {
		t.Fatal("expected old session to be unreachable after replacement")
	}
	if ok := h.SendToSession("session-new", testMsg("test")); !ok {
		t.Fatal("expected new session to be reachable")
	}
}

func TestGetSessionID(t *testing.T) {
	h := New(nil)
	defer h.Close()

	conn := h.Register("agent-1", "c1")
	gen := conn.Generation()

	if s := h.GetSessionID("agent-1", "c1", gen); s != "" {
		t.Fatalf("expected empty session before SetSession, got %q", s)
	}

	h.SetSession("agent-1", "c1", "session-1")

	if s := h.GetSessionID("agent-1", "c1", gen); s != "session-1" {
		t.Fatalf("expected 'session-1', got %q", s)
	}

	// Stale generation returns empty
	if s := h.GetSessionID("agent-1", "c1", gen-1); s != "" {
		t.Fatalf("expected empty for stale generation, got %q", s)
	}

	// Missing agent returns empty
	if s := h.GetSessionID("nonexistent", "c1", gen); s != "" {
		t.Fatalf("expected empty for missing agent, got %q", s)
	}
}

func TestSendToWrongConsumer(t *testing.T) {
	h := New(nil)
	defer h.Close()

	h.Register("agent-1", "c1")

	ok := h.SendTo("c-nonexistent", "agent-1", testMsg("test"))
	if ok {
		t.Fatal("expected SendTo to return false for wrong consumer")
	}

	ok = h.SendTo("c1", "agent-nonexistent", testMsg("test"))
	if ok {
		t.Fatal("expected SendTo to return false for wrong agent")
	}
}

func TestCloseClosesAllChannels(t *testing.T) {
	h := New(nil)

	conn1 := h.Register("agent-1", "c1")
	conn2 := h.Register("agent-2", "c1")

	h.Close()

	// Channels should be closed
	if _, open := <-conn1.EventCh; open {
		t.Fatal("expected conn1 channel to be closed")
	}
	if _, open := <-conn2.EventCh; open {
		t.Fatal("expected conn2 channel to be closed")
	}

	if h.HasConnections("agent-1") {
		t.Fatal("expected no connections after close")
	}
}

func TestUnregisterNonexistentAgent(t *testing.T) {
	h := New(nil)
	defer h.Close()

	// Should not panic
	h.Unregister("nonexistent", "c1", 999)
}

func TestSetSessionMissingAgent(t *testing.T) {
	h := New(nil)
	defer h.Close()

	// Should not panic
	h.SetSession("nonexistent", "c1", "session-1")
}

func TestRoundRobinIndependencePerAgent(t *testing.T) {
	h := New(nil)
	defer h.Close()

	h.Register("agent-1", "c1")
	h.Register("agent-1", "c2")
	h.Register("agent-2", "c3")

	// Pick from agent-1 several times
	for i := 0; i < 5; i++ {
		h.PickConnection("agent-1")
	}

	// agent-2 round robin should be independent
	info, ok := h.PickConnection("agent-2")
	if !ok {
		t.Fatal("expected PickConnection to succeed for agent-2")
	}
	if info.ConsumerID != "c3" {
		t.Fatalf("expected consumer_id 'c3', got %q", info.ConsumerID)
	}
}

func TestConcurrentRegisterUnregisterSameAgent(t *testing.T) {
	h := New(nil)
	defer h.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			consumerID := fmt.Sprintf("c%d", id)
			conn := h.Register("agent-shared", consumerID)
			h.Send("agent-shared", testMsg("test"))
			h.Unregister("agent-shared", consumerID, conn.Generation())
		}(i)
	}
	wg.Wait()
}
