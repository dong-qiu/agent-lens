package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// Counter assertions are delta-based: these collectors live on the global
// default registry, so other tests (or test order) may have already
// incremented them. Absolute values would be flaky; deltas are not.

func TestEventIngestedAndFailureCounters(t *testing.T) {
	evt := eventsIngested.WithLabelValues("prompt")
	before := testutil.ToFloat64(evt)
	EventIngested("prompt")
	if d := testutil.ToFloat64(evt) - before; d != 1 {
		t.Errorf("events_ingested{kind=prompt} delta = %v, want 1", d)
	}

	fail := ingestFailures.WithLabelValues("decode")
	beforeFail := testutil.ToFloat64(fail)
	IngestFailure("decode")
	if d := testutil.ToFloat64(fail) - beforeFail; d != 1 {
		t.Errorf("ingest_failures{reason=decode} delta = %v, want 1", d)
	}
}

func TestSetSessionHeadCacheSize(t *testing.T) {
	SetSessionHeadCacheSize(7)
	if got := testutil.ToFloat64(sessionHeadCacheSize); got != 7 {
		t.Errorf("session_head_cache_size = %v, want 7", got)
	}
}

func TestTimeGraphQLObservesAndPassesThrough(t *testing.T) {
	var m1 dto.Metric
	if err := graphqlRequestDuration.Write(&m1); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	before := m1.GetHistogram().GetSampleCount()

	called := false
	wrapped := TimeGraphQL(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/graphql", nil))

	if !called || rec.Code != http.StatusTeapot {
		t.Fatalf("inner handler not invoked through wrapper (called=%v, code=%d)", called, rec.Code)
	}

	var m2 dto.Metric
	if err := graphqlRequestDuration.Write(&m2); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	if d := m2.GetHistogram().GetSampleCount() - before; d != 1 {
		t.Errorf("graphql histogram sample-count delta = %d, want 1", d)
	}
}

func TestHandlerServesExposition(t *testing.T) {
	// Touch each collector so its series is present in the exposition.
	EventIngested("commit")
	IngestFailure("store")
	SetSessionHeadCacheSize(1)
	graphqlRequestDuration.Observe(0.01)

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{
		"agent_lens_events_ingested_total",
		"agent_lens_ingest_failures_total",
		"agent_lens_session_head_cache_size",
		"agent_lens_graphql_request_duration_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("/metrics body missing %q", name)
		}
	}
}
