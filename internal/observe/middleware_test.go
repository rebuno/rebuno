package observe

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsMiddlewarePreservesFlusher(t *testing.T) {
	var sawFlusher bool
	h := Default().MetricsMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !sawFlusher {
		t.Fatal("handler did not receive an http.Flusher through the metrics middleware")
	}
}
