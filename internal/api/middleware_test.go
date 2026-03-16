package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestBearerAuthMiddleware(t *testing.T) {
	validToken := "test-secret-token"
	mw := bearerAuthMiddleware(validToken)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(inner)

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"valid token", "Bearer test-secret-token", http.StatusOK},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"missing header", "", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
		{"wrong scheme", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"token without scheme", "test-secret-token", http.StatusUnauthorized},
		{"bearer lowercase", "bearer test-secret-token", http.StatusUnauthorized},
		{"extra spaces", "Bearer  test-secret-token", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v0/executions", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status: got %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantStatus == http.StatusUnauthorized {
				var body domain.APIError
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("failed to parse response body: %v", err)
				}
				if body.Code != domain.CodeUnauthorized {
					t.Fatalf("code: got %s, want %s", body.Code, domain.CodeUnauthorized)
				}
			}
		})
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := getRequestID(r.Context())
		w.Header().Set("X-Got-ID", id)
		w.WriteHeader(http.StatusOK)
	})
	handler := requestIDMiddleware(inner)

	t.Run("generates id when header missing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		respID := w.Header().Get("X-Request-ID")
		if respID == "" {
			t.Fatal("expected a generated request ID")
		}
		if gotID := w.Header().Get("X-Got-ID"); gotID != respID {
			t.Fatalf("context ID %q != response header ID %q", gotID, respID)
		}
	})

	t.Run("uses client-provided id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", "my-custom-id")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("X-Request-ID"); got != "my-custom-id" {
			t.Fatalf("expected my-custom-id, got %s", got)
		}
	})

	t.Run("rejects id exceeding 128 chars", func(t *testing.T) {
		longID := strings.Repeat("a", 129)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", longID)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("X-Request-ID"); got == longID {
			t.Fatal("expected generated ID for oversized input, got the original")
		}
	})

	t.Run("rejects id with newline", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", "bad\nid")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("X-Request-ID"); got == "bad\nid" {
			t.Fatal("expected generated ID for id containing newline")
		}
	})

	t.Run("rejects id with carriage return", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", "bad\rid")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("X-Request-ID"); got == "bad\rid" {
			t.Fatal("expected generated ID for id containing carriage return")
		}
	})
}

func TestRecoveryMiddleware(t *testing.T) {
	logger := noopLogger()
	mw := recoveryMiddleware(logger)
	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	handler := mw(panicking)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after panic, got %d", w.Code)
	}
	var body domain.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse error body: %v", err)
	}
	if body.Code != domain.CodeInternalError {
		t.Fatalf("expected INTERNAL_ERROR, got %s", body.Code)
	}
}

func TestBodySizeLimitMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, domain.CodeValidationError, "body too large")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := bodySizeLimitMiddleware(inner)

	t.Run("allows body under limit", func(t *testing.T) {
		body := strings.NewReader(`{"small": true}`)
		req := httptest.NewRequest(http.MethodPost, "/", body)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for small body, got %d", w.Code)
		}
	})

	t.Run("rejects body over 1MB", func(t *testing.T) {
		oversized := strings.NewReader(strings.Repeat("x", maxRequestBodySize+1))
		req := httptest.NewRequest(http.MethodPost, "/", oversized)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code == http.StatusOK {
			t.Fatal("expected non-200 for oversized body")
		}
	})

	t.Run("handles nil body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for nil body, got %d", w.Code)
		}
	})
}

func TestValidateStringLength(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		if err := validateStringLength("field", "abc", 10); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("at limit", func(t *testing.T) {
		if err := validateStringLength("field", "abcde", 5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("over limit", func(t *testing.T) {
		err := validateStringLength("field", "abcdef", 5)
		if err == nil {
			t.Fatal("expected error for string exceeding limit")
		}
	})

	t.Run("empty string", func(t *testing.T) {
		if err := validateStringLength("field", "", 5); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCORSMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("allows matching origin", func(t *testing.T) {
		handler := corsMiddleware("http://localhost:3000")(inner)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
			t.Fatalf("expected matching origin, got %q", got)
		}
	})

	t.Run("rejects non-matching origin", func(t *testing.T) {
		handler := corsMiddleware("http://localhost:3000")(inner)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "http://evil.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected no CORS header for non-matching origin, got %q", got)
		}
	})

	t.Run("wildcard origin", func(t *testing.T) {
		handler := corsMiddleware("*")(inner)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "http://anything.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("expected *, got %q", got)
		}
	})

	t.Run("OPTIONS preflight returns 204", func(t *testing.T) {
		handler := corsMiddleware("http://localhost:3000")(inner)
		req := httptest.NewRequest(http.MethodOptions, "/", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected 204 for OPTIONS preflight, got %d", w.Code)
		}
	})

	t.Run("multiple origins comma-separated", func(t *testing.T) {
		handler := corsMiddleware("http://a.com, http://b.com")(inner)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Origin", "http://b.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://b.com" {
			t.Fatalf("expected http://b.com, got %q", got)
		}
	})

	t.Run("no origin header", func(t *testing.T) {
		handler := corsMiddleware("http://localhost:3000")(inner)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("expected no CORS header when no Origin sent, got %q", got)
		}
	})
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
