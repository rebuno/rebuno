package observe

import (
	"context"
	"log/slog"
	"testing"
)

func TestNewLoggerRespectsLevel(t *testing.T) {
	l := NewLogger("warn", "text")
	if l == nil {
		t.Fatal("nil logger")
	}
	h := l.Handler()
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be disabled at warn level")
	}
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("warn should be enabled at warn level")
	}
}

func TestNewLoggerJSONFormat(t *testing.T) {
	l := NewLogger("info", "json")
	if l == nil {
		t.Fatal("nil logger")
	}
}

func TestInitTracerNoopWhenNoEndpoint(t *testing.T) {
	shutdown, err := InitTracer(context.Background(), "", 1.0, false, NewLogger("info", "text"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown func must not be nil")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown returned error: %v", err)
	}
}
