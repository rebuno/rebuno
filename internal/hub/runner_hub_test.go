package hub

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/rebuno/rebuno/internal/store"
)

func runnerMsg(t string) store.RunnerMessage {
	return store.RunnerMessage{Type: t, Payload: json.RawMessage(`{}`)}
}

func TestRunnerRegisterUnregister(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn := h.Register("runner-1", "c1", []string{"web.search"})
	if conn == nil {
		t.Fatal("expected non-nil conn")
	}
	if !h.HasCapability("web.search") {
		t.Fatal("expected capability web.search")
	}

	h.Unregister("runner-1", "c1", conn.Generation())
	if h.HasCapability("web.search") {
		t.Fatal("expected no capability after unregister")
	}
}

func TestRunnerCapabilityIndex(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"web.search", "doc.fetch"})
	h.Register("runner-2", "c1", []string{"calculator"})

	if !h.HasCapability("web.search") {
		t.Fatal("expected web.search capability")
	}
	if !h.HasCapability("doc.fetch") {
		t.Fatal("expected doc.fetch capability")
	}
	if !h.HasCapability("calculator") {
		t.Fatal("expected calculator capability")
	}
	if h.HasCapability("nonexistent") {
		t.Fatal("expected no nonexistent capability")
	}
}

func TestRunnerDispatchToIdleRunner(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn := h.Register("runner-1", "c1", []string{"web.search"})

	info, ok := h.Dispatch("web.search", runnerMsg("job.assigned"))
	if !ok {
		t.Fatal("expected dispatch to succeed")
	}
	if info.RunnerID != "runner-1" || info.ConsumerID != "c1" {
		t.Fatalf("unexpected info: %+v", info)
	}

	select {
	case msg := <-conn.EventCh:
		if msg.Type != "job.assigned" {
			t.Fatalf("expected type job.assigned, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestRunnerDispatchSkipsBusyRunner(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"web.search"})
	conn2 := h.Register("runner-2", "c1", []string{"web.search"})

	h.MarkBusy("runner-1", "c1")

	info, ok := h.Dispatch("web.search", runnerMsg("job.assigned"))
	if !ok {
		t.Fatal("expected dispatch to succeed")
	}
	if info.RunnerID != "runner-2" {
		t.Fatalf("expected runner-2, got %s", info.RunnerID)
	}

	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "job.assigned" {
			t.Fatalf("expected job.assigned, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on runner-2 channel")
	}
}

func TestRunnerDispatchNoCapability(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"calculator"})

	_, ok := h.Dispatch("web.search", runnerMsg("job.assigned"))
	if ok {
		t.Fatal("expected dispatch to fail for missing capability")
	}
}

func TestRunnerRoundRobin(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"web.search"})
	h.Register("runner-2", "c1", []string{"web.search"})

	seen := make(map[string]int)
	for i := 0; i < 10; i++ {
		info, ok := h.Dispatch("web.search", runnerMsg("job.assigned"))
		if !ok {
			t.Fatal("expected dispatch to succeed")
		}
		seen[info.RunnerID]++
		consumers := h.runners[info.RunnerID]
		<-consumers[info.ConsumerID].EventCh
	}

	if len(seen) < 2 {
		t.Fatal("expected round-robin to distribute across runners")
	}
	for runnerID, count := range seen {
		if count < 4 {
			t.Fatalf("expected runner %s to get at least 4 of 10 dispatches, got %d", runnerID, count)
		}
	}
}

func TestRunnerMarkBusyIdle(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn := h.Register("runner-1", "c1", []string{"web.search"})

	h.MarkBusy("runner-1", "c1")
	if !conn.Busy {
		t.Fatal("expected conn to be busy")
	}

	_, ok := h.Dispatch("web.search", runnerMsg("test"))
	if ok {
		t.Fatal("expected dispatch to fail when busy")
	}

	h.MarkIdle("runner-1", "c1")
	if conn.Busy {
		t.Fatal("expected conn to be idle")
	}

	_, ok = h.Dispatch("web.search", runnerMsg("test"))
	if !ok {
		t.Fatal("expected dispatch to succeed after marking idle")
	}
}

func TestRunnerSendTo(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn := h.Register("runner-1", "c1", []string{"web.search"})

	ok := h.SendTo("runner-1", "c1", runnerMsg("ping"))
	if !ok {
		t.Fatal("expected SendTo to succeed")
	}

	select {
	case msg := <-conn.EventCh:
		if msg.Type != "ping" {
			t.Fatalf("expected type ping, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on channel")
	}

	ok = h.SendTo("nonexistent", "c1", runnerMsg("ping"))
	if ok {
		t.Fatal("expected SendTo to fail for missing runner")
	}
}

func TestRunnerConcurrentAccess(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conns := make([]*RunnerConn, 10)
	for i := 0; i < 10; i++ {
		conns[i] = h.Register(fmt.Sprintf("runner-%d", i), fmt.Sprintf("c%d", i), []string{"tool"})
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Dispatch("tool", runnerMsg("concurrent"))
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(conn *RunnerConn) {
			defer wg.Done()
			for {
				select {
				case _, ok := <-conn.EventCh:
					if !ok {
						return
					}
				default:
					return
				}
			}
		}(conns[i])
	}
	wg.Wait()

	for i := 0; i < 10; i++ {
		h.Unregister(fmt.Sprintf("runner-%d", i), fmt.Sprintf("c%d", i), conns[i].Generation())
	}
}

func TestRunnerReRegisterSameConsumer(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"web.search"})
	conn2 := h.Register("runner-1", "c1", []string{"web.search", "calculator"})

	if conn1 == conn2 {
		t.Fatal("expected new connection object")
	}

	if !h.HasCapability("calculator") {
		t.Fatal("expected calculator capability after re-register")
	}

	ok := h.SendTo("runner-1", "c1", runnerMsg("test"))
	if !ok {
		t.Fatal("expected send to succeed")
	}

	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "test" {
			t.Fatalf("expected type test, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on new connection")
	}
}

func TestRunnerUpdateCapabilities(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"old.tool"})
	if !h.HasCapability("old.tool") {
		t.Fatal("expected old.tool capability")
	}

	h.UpdateCapabilities("runner-1", []string{"new.tool_a", "new.tool_b"})

	if h.HasCapability("old.tool") {
		t.Fatal("expected old.tool removed after update")
	}
	if !h.HasCapability("new.tool_a") {
		t.Fatal("expected new.tool_a capability")
	}
	if !h.HasCapability("new.tool_b") {
		t.Fatal("expected new.tool_b capability")
	}

	// Dispatch should work with new capabilities
	conn := h.runners["runner-1"]["c1"]
	info, ok := h.Dispatch("new.tool_a", runnerMsg("job.assigned"))
	if !ok {
		t.Fatal("expected dispatch to succeed with new capability")
	}
	if info.RunnerID != "runner-1" {
		t.Fatalf("expected runner-1, got %s", info.RunnerID)
	}
	<-conn.EventCh
}

func TestRunnerUpdateCapabilitiesUnknownRunner(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	// Should not panic for unknown runner
	h.UpdateCapabilities("nonexistent", []string{"tool"})
	if h.HasCapability("tool") {
		t.Fatal("expected no capability for unknown runner")
	}
}

func TestRunnerConnOverflow(t *testing.T) {
	c := NewRunnerConn("runner-1", "c1", []string{"web.search"})

	for i := 0; i < runnerEventChannelSize; i++ {
		if !c.Send(runnerMsg("fill")) {
			t.Fatalf("expected send to succeed at index %d", i)
		}
	}

	if c.Send(runnerMsg("overflow")) {
		t.Fatal("expected send to fail when channel is full")
	}
}

func TestRunnerUnregisterStaleGeneration(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"web.search"})
	staleGen := conn1.Generation()

	h.Register("runner-1", "c1", []string{"web.search"})

	// Unregister with stale generation should be a no-op
	h.Unregister("runner-1", "c1", staleGen)

	if !h.HasCapability("web.search") {
		t.Fatal("expected capability to survive stale unregister")
	}
}

func TestRunnerMarkRunnerIdle(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"tool-a"})
	conn2 := h.Register("runner-1", "c2", []string{"tool-b"})

	h.MarkBusy("runner-1", "c1")
	h.MarkBusy("runner-1", "c2")

	if !conn1.Busy || !conn2.Busy {
		t.Fatal("expected both connections to be busy")
	}

	h.MarkRunnerIdle("runner-1")

	if conn1.Busy || conn2.Busy {
		t.Fatal("expected both connections to be idle after MarkRunnerIdle")
	}
}

func TestRunnerSendToMissingConsumer(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	h.Register("runner-1", "c1", []string{"tool"})

	ok := h.SendTo("runner-1", "nonexistent-consumer", runnerMsg("ping"))
	if ok {
		t.Fatal("expected SendTo to fail for missing consumer")
	}
}

func TestRunnerCloseClosesAllChannels(t *testing.T) {
	h := NewRunnerHub(nil)

	conn1 := h.Register("runner-1", "c1", []string{"tool-a"})
	conn2 := h.Register("runner-2", "c1", []string{"tool-b"})

	h.Close()

	if _, open := <-conn1.EventCh; open {
		t.Fatal("expected conn1 channel to be closed")
	}
	if _, open := <-conn2.EventCh; open {
		t.Fatal("expected conn2 channel to be closed")
	}

	if h.HasCapability("tool-a") {
		t.Fatal("expected no capabilities after close")
	}
}

func TestRunnerSharedCapabilityPartialUnregister(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"shared-tool"})
	h.Register("runner-2", "c1", []string{"shared-tool"})

	h.Unregister("runner-1", "c1", conn1.Generation())

	// Capability should still exist from runner-2
	if !h.HasCapability("shared-tool") {
		t.Fatal("expected shared-tool capability to remain from runner-2")
	}

	// Dispatch should go to runner-2
	info, ok := h.Dispatch("shared-tool", runnerMsg("job"))
	if !ok {
		t.Fatal("expected dispatch to succeed")
	}
	if info.RunnerID != "runner-2" {
		t.Fatalf("expected runner-2, got %s", info.RunnerID)
	}
}

func TestRunnerMultipleConsumersPerRunner(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"tool"})
	conn2 := h.Register("runner-1", "c2", []string{"tool"})

	// Both consumers should be reachable via SendTo
	ok := h.SendTo("runner-1", "c1", runnerMsg("ping1"))
	if !ok {
		t.Fatal("expected SendTo c1 to succeed")
	}
	ok = h.SendTo("runner-1", "c2", runnerMsg("ping2"))
	if !ok {
		t.Fatal("expected SendTo c2 to succeed")
	}

	select {
	case msg := <-conn1.EventCh:
		if msg.Type != "ping1" {
			t.Fatalf("expected ping1, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on c1")
	}
	select {
	case msg := <-conn2.EventCh:
		if msg.Type != "ping2" {
			t.Fatalf("expected ping2, got %q", msg.Type)
		}
	default:
		t.Fatal("expected message on c2")
	}
}

func TestRunnerDispatchSkipsFullChannel(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	conn1 := h.Register("runner-1", "c1", []string{"tool"})
	conn2 := h.Register("runner-2", "c1", []string{"tool"})

	// Fill runner-1's channel
	for i := 0; i < runnerEventChannelSize; i++ {
		conn1.EventCh <- runnerMsg("fill")
	}

	// Dispatch should skip full runner-1 and go to runner-2
	info, ok := h.Dispatch("tool", runnerMsg("job"))
	if !ok {
		t.Fatal("expected dispatch to succeed")
	}
	if info.RunnerID != "runner-2" {
		t.Fatalf("expected runner-2 (runner-1 full), got %s", info.RunnerID)
	}
	<-conn2.EventCh
}

func TestRunnerUnregisterNonexistent(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	// Should not panic
	h.Unregister("nonexistent", "c1", 999)
}

func TestRunnerMarkBusyNonexistent(t *testing.T) {
	h := NewRunnerHub(nil)
	defer h.Close()

	// Should not panic
	h.MarkBusy("nonexistent", "c1")
	h.MarkIdle("nonexistent", "c1")
	h.MarkRunnerIdle("nonexistent")
}
