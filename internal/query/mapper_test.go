package query

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dongqiu/agent-lens/internal/store"
)

func TestDecodePayloadUsage_Present(t *testing.T) {
	// Wire shape mirrors what the hook writes (transcript.TokenUsage
	// snake_case JSON tags). Decode + map to GraphQL TokenUsage.
	payload := map[string]any{
		"usage": map[string]any{
			"vendor":                "anthropic",
			"model":                 "claude-opus-4-7",
			"service_tier":          "priority",
			"input_tokens":          float64(11),
			"output_tokens":         float64(22),
			"cache_read_tokens":     float64(33),
			"cache_write_5m_tokens": float64(44),
			"cache_write_1h_tokens": float64(55),
			"web_search_calls":      float64(6),
			"web_fetch_calls":       float64(7),
		},
	}
	got := decodePayloadUsage(payload)
	if got == nil {
		t.Fatalf("expected non-nil")
	}
	checks := []struct {
		name     string
		got, exp any
	}{
		{"Vendor", got.Vendor, "anthropic"},
		{"Model", got.Model, "claude-opus-4-7"},
		{"InputTokens", got.InputTokens, 11},
		{"OutputTokens", got.OutputTokens, 22},
	}
	for _, c := range checks {
		if c.got != c.exp {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.exp)
		}
	}
	if got.ServiceTier == nil || *got.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %v, want priority", got.ServiceTier)
	}
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 33 {
		t.Errorf("CacheReadTokens = %v, want 33", got.CacheReadTokens)
	}
	if got.CacheWrite5mTokens == nil || *got.CacheWrite5mTokens != 44 {
		t.Errorf("CacheWrite5mTokens = %v, want 44", got.CacheWrite5mTokens)
	}
	if got.CacheWrite1hTokens == nil || *got.CacheWrite1hTokens != 55 {
		t.Errorf("CacheWrite1hTokens = %v, want 55", got.CacheWrite1hTokens)
	}
	if got.WebSearchCalls == nil || *got.WebSearchCalls != 6 {
		t.Errorf("WebSearchCalls = %v, want 6", got.WebSearchCalls)
	}
	if got.WebFetchCalls == nil || *got.WebFetchCalls != 7 {
		t.Errorf("WebFetchCalls = %v, want 7", got.WebFetchCalls)
	}
}

func TestDecodePayloadUsage_AbsentReturnsNil(t *testing.T) {
	cases := []map[string]any{
		nil,
		{},
		{"usage": nil},
		{"some_other_key": "x"},
	}
	for i, p := range cases {
		if got := decodePayloadUsage(p); got != nil {
			t.Errorf("case %d: expected nil, got %+v", i, got)
		}
	}
}

func TestDecodePayloadUsage_ZeroOptionalsCollapseToNil(t *testing.T) {
	// `omitempty` on the wire makes 0 and absent indistinguishable;
	// the decoder treats them the same.
	payload := map[string]any{
		"usage": map[string]any{
			"vendor":            "anthropic",
			"model":             "claude-opus-4-7",
			"input_tokens":      float64(1),
			"output_tokens":     float64(2),
			"cache_read_tokens": float64(0),
		},
	}
	got := decodePayloadUsage(payload)
	if got == nil {
		t.Fatalf("expected non-nil")
	}
	if got.CacheReadTokens != nil {
		t.Errorf("zero cache_read_tokens should collapse to nil; got %v", *got.CacheReadTokens)
	}
	if got.WebSearchCalls != nil || got.WebFetchCalls != nil {
		t.Errorf("absent counters should be nil")
	}
}

func TestDecodePayloadStopReason(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		wantNil bool
		wantVal string
	}{
		{"present", map[string]any{"stop_reason": "end_turn"}, false, "end_turn"},
		{"empty string", map[string]any{"stop_reason": ""}, true, ""},
		{"absent", map[string]any{}, true, ""},
		{"nil payload", nil, true, ""},
		{"non-string", map[string]any{"stop_reason": 42}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodePayloadStopReason(c.payload)
			if c.wantNil && got != nil {
				t.Errorf("expected nil, got %q", *got)
			}
			if !c.wantNil {
				if got == nil {
					t.Errorf("expected %q, got nil", c.wantVal)
				} else if *got != c.wantVal {
					t.Errorf("got %q, want %q", *got, c.wantVal)
				}
			}
		})
	}
}

func TestAggregateSessionUsage_SumsCounters(t *testing.T) {
	events := []*store.Event{
		makeEventWithUsage(t, "anthropic", "claude-opus-4-7", "standard", 10, 20, 30, 0, 5),
		makeEventWithUsage(t, "anthropic", "claude-opus-4-7", "standard", 1, 2, 3, 0, 0),
		// Event with no usage payload — must be skipped, not counted.
		{ID: "e3", Payload: json.RawMessage(`{"text":"plain"}`)},
	}
	got := aggregateSessionUsage(events)
	if got == nil {
		t.Fatalf("expected aggregate, got nil")
	}
	if got.InputTokens != 11 || got.OutputTokens != 22 {
		t.Errorf("input/output = %d/%d, want 11/22", got.InputTokens, got.OutputTokens)
	}
	if got.CacheReadTokens == nil || *got.CacheReadTokens != 33 {
		t.Errorf("cache_read_tokens = %v, want 33", got.CacheReadTokens)
	}
	// 5m bucket was zero in both contributors → should stay nil.
	if got.CacheWrite5mTokens != nil {
		t.Errorf("cache_write_5m_tokens should be nil for all-zero session; got %v", *got.CacheWrite5mTokens)
	}
	if got.CacheWrite1hTokens == nil || *got.CacheWrite1hTokens != 5 {
		t.Errorf("cache_write_1h_tokens = %v, want 5", got.CacheWrite1hTokens)
	}
	if got.Vendor != "anthropic" {
		t.Errorf("Vendor = %q, want anthropic", got.Vendor)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", got.Model)
	}
	if got.ServiceTier == nil || *got.ServiceTier != "standard" {
		t.Errorf("ServiceTier = %v, want standard", got.ServiceTier)
	}
}

func TestAggregateSessionUsage_MultiModelCollapsesIdentity(t *testing.T) {
	// Mixed-model session: vendor/model/service_tier shouldn't claim a
	// single value because that'd misrepresent the audit picture. They
	// stay empty / nil while numeric counters still aggregate.
	events := []*store.Event{
		makeEventWithUsage(t, "anthropic", "claude-opus-4-7", "standard", 10, 20, 0, 0, 0),
		makeEventWithUsage(t, "anthropic", "claude-sonnet-4-6", "priority", 5, 10, 0, 0, 0),
	}
	got := aggregateSessionUsage(events)
	if got == nil {
		t.Fatalf("expected aggregate")
	}
	if got.Model != "" {
		t.Errorf("Model should be empty for multi-model session; got %q", got.Model)
	}
	if got.ServiceTier != nil {
		t.Errorf("ServiceTier should be nil for mixed-tier session; got %v", *got.ServiceTier)
	}
	if got.Vendor != "anthropic" {
		t.Errorf("Vendor stays homogeneous (both anthropic); got %q", got.Vendor)
	}
	if got.InputTokens != 15 || got.OutputTokens != 30 {
		t.Errorf("counters didn't aggregate: %d/%d", got.InputTokens, got.OutputTokens)
	}
}

func TestAggregateSessionUsage_ReturnsNilWhenNoUsage(t *testing.T) {
	events := []*store.Event{
		{ID: "e1", Payload: json.RawMessage(`{"text":"plain"}`)},
		{ID: "e2", Payload: json.RawMessage(`{"name":"Bash"}`)},
	}
	if got := aggregateSessionUsage(events); got != nil {
		t.Errorf("expected nil for usage-free session; got %+v", got)
	}
}

func TestAggregateSessionUsage_EmptyInput(t *testing.T) {
	if got := aggregateSessionUsage(nil); got != nil {
		t.Errorf("expected nil for empty input")
	}
	if got := aggregateSessionUsage([]*store.Event{}); got != nil {
		t.Errorf("expected nil for empty slice")
	}
}

// makeEventWithUsage builds a synthetic store.Event whose payload
// carries a usage block in the same wire format the hook writes.
func makeEventWithUsage(t *testing.T, vendor, model, tier string, in, out, cacheR, cache5m, cache1h int) *store.Event {
	t.Helper()
	payload := map[string]any{
		"usage": map[string]any{
			"vendor":                vendor,
			"model":                 model,
			"service_tier":          tier,
			"input_tokens":          in,
			"output_tokens":         out,
			"cache_read_tokens":     cacheR,
			"cache_write_5m_tokens": cache5m,
			"cache_write_1h_tokens": cache1h,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &store.Event{Payload: b}
}

// Ensure the GraphQL TokenUsage struct shape stays in sync with what
// the resolver maps to. If gqlgen regenerates and changes a field, this
// catches the drift.
func TestTokenUsageGraphQLShape(t *testing.T) {
	want := []string{
		"Vendor", "Model", "ServiceTier",
		"InputTokens", "OutputTokens",
		"CacheReadTokens", "CacheWrite5mTokens", "CacheWrite1hTokens",
		"WebSearchCalls", "WebFetchCalls",
	}
	tt := reflect.TypeOf(TokenUsage{})
	got := make([]string, 0, tt.NumField())
	for i := 0; i < tt.NumField(); i++ {
		got = append(got, tt.Field(i).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TokenUsage fields drifted:\ngot:  %v\nwant: %v", got, want)
	}
}
