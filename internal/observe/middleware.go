package observe

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// MetricsMiddleware returns chi-compatible middleware that records request
// count/duration and creates an OpenTelemetry span per request.
func (o *Observer) MetricsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			tracer := o.Tracer()
			ctx, span := tracer.Start(r.Context(), httpSpanName(r),
				trace.WithSpanKind(trace.SpanKindServer),
			)
			defer span.End()

			span.SetAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", routePattern(r)),
				attribute.String("http.target", r.URL.Path),
			)

			rw := &recordingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(rw, r.WithContext(ctx))

			if rw.statusCode >= 500 {
				span.SetStatus(codes.Error, "request failed with "+strconv.Itoa(rw.statusCode))
			}
			span.SetAttributes(attribute.Int("http.status_code", rw.statusCode))

			o.RecordHTTP(routePattern(r), rw.statusCode, time.Since(start))
		})
	}
}

// MetricsMiddlewareWithDefault is a convenience wrapper that uses the package
// default Observer.
func MetricsMiddlewareWithDefault() func(http.Handler) http.Handler {
	return Default().MetricsMiddleware()
}

type recordingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *recordingResponseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recordingResponseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return r.URL.Path
	}
	pattern := strings.Join(rctx.RoutePatterns, "")
	if pattern == "" {
		return r.URL.Path
	}
	return pattern
}

func httpSpanName(r *http.Request) string {
	return r.Method + " " + routePattern(r)
}
