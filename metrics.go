package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry        *prometheus.Registry
	requests        *prometheus.CounterVec
	latency         *prometheus.HistogramVec
	outboxEnqueued  *prometheus.CounterVec
	outboxPublished *prometheus.CounterVec
	outboxRetries   *prometheus.CounterVec
	outboxDLQ       *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_http_requests_total",
		Help: "Total HTTP requests by method, route and status.",
	}, []string{"method", "route", "status"})
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ledger_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	outboxEnqueued := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_outbox_enqueued_total",
		Help: "Total events enqueued into the transactional outbox.",
	}, []string{"event_type"})
	outboxPublished := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_outbox_published_total",
		Help: "Total events successfully dispatched from the outbox.",
	}, []string{"event_type"})
	outboxRetries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_outbox_retries_total",
		Help: "Total retry attempts for outbox dispatch failures.",
	}, []string{"event_type"})
	outboxDLQ := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ledger_outbox_dlq_total",
		Help: "Total events moved to dead-letter queue after exhausting retries.",
	}, []string{"event_type"})

	registry.MustRegister(requests, latency, outboxEnqueued, outboxPublished, outboxRetries, outboxDLQ)
	return &Metrics{
		registry:        registry,
		requests:        requests,
		latency:         latency,
		outboxEnqueued:  outboxEnqueued,
		outboxPublished: outboxPublished,
		outboxRetries:   outboxRetries,
		outboxDLQ:       outboxDLQ,
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.requests.WithLabelValues(r.Method, r.URL.Path, strconv.Itoa(rec.status)).Inc()
		m.latency.WithLabelValues(r.Method, r.URL.Path).Observe(time.Since(start).Seconds())
	})
}

func (m *Metrics) RecordOutboxEnqueued(eventType string) {
	if m != nil && m.outboxEnqueued != nil {
		m.outboxEnqueued.WithLabelValues(eventType).Inc()
	}
}

func (m *Metrics) RecordOutboxPublished(eventType string) {
	if m != nil && m.outboxPublished != nil {
		m.outboxPublished.WithLabelValues(eventType).Inc()
	}
}

func (m *Metrics) RecordOutboxRetry(eventType string) {
	if m != nil && m.outboxRetries != nil {
		m.outboxRetries.WithLabelValues(eventType).Inc()
	}
}

func (m *Metrics) RecordOutboxDLQ(eventType string) {
	if m != nil && m.outboxDLQ != nil {
		m.outboxDLQ.WithLabelValues(eventType).Inc()
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
