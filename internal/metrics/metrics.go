// Package metrics exposes the Prometheus collectors for Agent Lens
// self-observability (SPEC §16.1, issue #4) plus the /metrics HTTP handler.
//
// Collectors register on client_golang's default registry at init, so the
// handler also serves the Go-runtime and process collectors that
// client_golang installs there. Label cardinality is kept low on purpose:
// `kind` (the 12 EventKind values) and `reason` (a small fixed set) are
// bounded; high-cardinality values like session_id are never used as labels.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	eventsIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_lens_events_ingested_total",
		Help: "Events successfully appended to the store, by kind.",
	}, []string{"kind"})

	ingestFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_lens_ingest_failures_total",
		Help: "Ingest attempts that failed, by reason (decode, validation, store).",
	}, []string{"reason"})

	eventsDeduped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_lens_events_deduped_total",
		Help: "Events skipped as idempotent duplicates (ADR 0014), by kind. Expected during replay; not a failure.",
	}, []string{"kind"})

	sessionHeadCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "agent_lens_session_head_cache_size",
		Help: "Sessions held in the ingest handler's in-memory head-hash cache.",
	})

	graphqlRequestDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_lens_graphql_request_duration_seconds",
		Help:    "GraphQL HTTP request handling duration in seconds.",
		Buckets: prometheus.DefBuckets,
	})
)

// EventIngested increments the per-kind ingested counter.
func EventIngested(kind string) { eventsIngested.WithLabelValues(kind).Inc() }

// IngestFailure increments the per-reason failure counter. reason must come
// from a small fixed set (see IngestFailures' Help) so cardinality stays bounded.
func IngestFailure(reason string) { ingestFailures.WithLabelValues(reason).Inc() }

// IngestDeduped increments the per-kind idempotent-skip counter. A dedup skip
// is the expected replay / retried-POST outcome (ADR 0014), not a failure, so
// it is counted on its own series to keep ingest-failure alerting clean.
func IngestDeduped(kind string) { eventsDeduped.WithLabelValues(kind).Inc() }

// SetSessionHeadCacheSize records the current head-hash cache entry count.
func SetSessionHeadCacheSize(n int) { sessionHeadCacheSize.Set(float64(n)) }

// TimeGraphQL wraps next, observing each request's wall-clock duration into
// the GraphQL request-duration histogram.
func TimeGraphQL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		graphqlRequestDuration.Observe(time.Since(start).Seconds())
	})
}

// Handler serves the Prometheus exposition format. Mount at /metrics on the
// root router (no auth) so a scraper on the deploy network can reach it.
func Handler() http.Handler { return promhttp.Handler() }
