// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// Prometheus instrumentation for the HTTP surface.
//
// The registry is private to this service rather than the global default, so
// metrics cannot collide when several services share a process in tests, and the
// "path" label carries the mux route template rather than the literal URL — or
// every distinct id would become its own time series.

package handlers

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus metric registry. We keep a private registry per service
// instead of using the global default — this avoids collisions when
// multiple services share a process in tests, and keeps the exposed
// metrics surface explicit and reviewable.
//
// Label cardinality is the perpetual Prometheus footgun. The "path"
// label uses gorilla/mux's path TEMPLATE (e.g. "/api/ais/{id}") not
// the literal URL — otherwise every distinct numeric id would create
// a separate time series.
var (
	metricsReg = prometheus.NewRegistry()

	httpRequestsTotal = promauto.With(metricsReg).NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "tachyonikcsm",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "HTTP requests handled, labelled by method, route template, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.With(metricsReg).NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "tachyonikcsm",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds, labelled by method and route template.",
			// Buckets tuned for an admin/REST workload: most calls
			// finish in <100ms, slow ones (AI fan-out) under a few
			// seconds, hard timeout near 30s.
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		},
		[]string{"method", "path"},
	)

	httpInflight = promauto.With(metricsReg).NewGauge(
		prometheus.GaugeOpts{
			Namespace: "tachyonikcsm",
			Subsystem: "http",
			Name:      "inflight_requests",
			Help:      "Currently in-flight HTTP requests.",
		},
	)
)

func init() {
	// Standard process + Go runtime collectors — RSS, goroutine count,
	// GC pause distribution, etc. Cheap and uniformly useful.
	metricsReg.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
}

// MetricsMiddleware wraps each HTTP request with timing + counter
// instrumentation. Place it at the router top, ABOVE the auth
// middleware — we want to see request volume regardless of whether
// requests authenticated successfully.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpInflight.Inc()
		defer httpInflight.Dec()

		start := time.Now()
		// Wrap the writer so we can observe the response status code.
		// http.ResponseWriter doesn't expose it post-hoc; the standard
		// library leaves it to consumers to record manually.
		ww := &statusCapturingResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		// Route template — populated by gorilla/mux during routing.
		// Falls back to literal URL only for requests that didn't
		// match any route (e.g. 404s); those are bounded in count so
		// no cardinality explosion.
		route := mux.CurrentRoute(r)
		path := r.URL.Path
		if route != nil {
			if tpl, err := route.GetPathTemplate(); err == nil {
				path = tpl
			}
		}

		method := r.Method
		status := strconv.Itoa(ww.status)
		duration := time.Since(start).Seconds()

		httpRequestsTotal.WithLabelValues(method, path, status).Inc()
		httpRequestDuration.WithLabelValues(method, path).Observe(duration)
	})
}

// statusCapturingResponseWriter intercepts WriteHeader so the
// middleware can label observations with the response status. If a
// handler never calls WriteHeader, status stays at 200 (Go's default
// behaviour when Write is called first).
type statusCapturingResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusCapturingResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Hijack forwards to the underlying ResponseWriter so WebSocket
// upgrades on routes wrapped by MetricsMiddleware still work. Without
// this, embedding http.ResponseWriter hides the Hijacker that the
// concrete writer provides, and gorilla/websocket's Upgrade fails with
// "response does not implement http.Hijacker".
func (w *statusCapturingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

// Flush forwards to the underlying ResponseWriter, preserving streaming
// behaviour (SSE, chunked responses) through the metrics wrapper.
func (w *statusCapturingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// MetricsHandler returns the http.Handler that exposes the registered
// metrics in Prometheus text format. Mounted at /metrics by the
// router; no auth because Prometheus scrapes anonymously (lock the
// endpoint down at the network layer instead — see the observability
// README).
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
