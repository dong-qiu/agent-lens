package query

import (
	"encoding/json"
	"strings"

	"github.com/dongqiu/agent-lens/internal/store"
)

// toGQLEvent converts a storage-layer event into the gqlgen-generated
// model used by the GraphQL API. Wire-format strings ("prompt", "human")
// are normalized to the GraphQL enum casing ("PROMPT", "HUMAN").
func toGQLEvent(se *store.Event) *Event {
	if se == nil {
		return nil
	}
	actor := &Actor{
		Type: ActorType(strings.ToUpper(se.ActorType)),
		ID:   se.ActorID,
	}
	if se.ActorModel != "" {
		m := se.ActorModel
		actor.Model = &m
	}

	ev := &Event{
		ID:        se.ID,
		Ts:        se.TS,
		SessionID: se.SessionID,
		Actor:     actor,
		Kind:      EventKind(strings.ToUpper(se.Kind)),
		Parents:   nonNilStrings(se.Parents),
		Refs:      nonNilStrings(se.Refs),
		Hash:      se.Hash,
	}
	if se.TurnID != "" {
		t := se.TurnID
		ev.TurnID = &t
	}
	if se.PrevHash != "" {
		p := se.PrevHash
		ev.PrevHash = &p
	}
	if len(se.Payload) > 0 {
		var p map[string]any
		if err := json.Unmarshal(se.Payload, &p); err == nil {
			ev.Payload = p
		}
	}
	return ev
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func toGQLSession(s *store.SessionSummary) *Session {
	if s == nil {
		return nil
	}
	return &Session{
		ID:           s.ID,
		FirstEventAt: s.FirstEventAt,
		LastEventAt:  s.LastEventAt,
		EventCount:   s.EventCount,
	}
}

func toGQLLink(l *store.Link) *Link {
	if l == nil {
		return nil
	}
	return &Link{
		FromEvent:  l.FromEvent,
		ToEvent:    l.ToEvent,
		Relation:   l.Relation,
		Confidence: float64(l.Confidence),
		InferredBy: l.InferredBy,
	}
}

// wireUsage mirrors the JSON shape produced by the transcript package's
// TokenUsage when serialized into payload.usage. Snake_case tags match
// what the hook writes; this struct is kept here (not imported) so the
// query package doesn't take a dependency on cmd/agent-lens-hook just
// to decode wire format.
//
// Adding a new TokenUsage field requires touching FOUR places, kept
// in lock-step intentionally so a stale producer / consumer / shape
// fails loudly rather than dropping data:
//   1. internal/transcript/reader.go           — TokenUsage struct + extractUsage mapping (producer)
//   2. internal/query/mapper.go (this file)    — wireUsage struct + decodePayloadUsage assignment + aggregateSessionUsage accumulator
//   3. internal/query/schema.graphql           — TokenUsage type (regen via `make gqlgen`)
//   4. web/src/types.ts + UI chips             — TokenUsage type + chip / tooltip rendering
//
// TestTokenUsageGraphQLShape catches drift on (3); the others rely on
// build-time field alignment between wireUsage / TokenUsage. Keep
// snake_case wire and camelCase GraphQL — that asymmetry is forced by
// the wire format already shipped in #63.
type wireUsage struct {
	Vendor             string `json:"vendor"`
	Model              string `json:"model"`
	ServiceTier        string `json:"service_tier"`
	InputTokens        int    `json:"input_tokens"`
	OutputTokens       int    `json:"output_tokens"`
	CacheReadTokens    int    `json:"cache_read_tokens"`
	CacheWrite5mTokens int    `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int    `json:"cache_write_1h_tokens"`
	WebSearchCalls     int    `json:"web_search_calls"`
	WebFetchCalls      int    `json:"web_fetch_calls"`
}

// decodePayloadUsage extracts payload.usage and returns a *TokenUsage
// suitable for the GraphQL response, or nil when usage isn't present
// or can't be decoded. Optional fields collapse to nil when their
// underlying value is 0 — `omitempty` on the wire makes 0 and absent
// indistinguishable, so we treat them the same on the way out.
func decodePayloadUsage(payload map[string]any) *TokenUsage {
	if payload == nil {
		return nil
	}
	raw, ok := payload["usage"]
	if !ok || raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var w wireUsage
	if err := json.Unmarshal(b, &w); err != nil {
		return nil
	}
	out := &TokenUsage{
		Vendor:       w.Vendor,
		Model:        w.Model,
		InputTokens:  w.InputTokens,
		OutputTokens: w.OutputTokens,
	}
	if w.ServiceTier != "" {
		v := w.ServiceTier
		out.ServiceTier = &v
	}
	if w.CacheReadTokens != 0 {
		v := w.CacheReadTokens
		out.CacheReadTokens = &v
	}
	if w.CacheWrite5mTokens != 0 {
		v := w.CacheWrite5mTokens
		out.CacheWrite5mTokens = &v
	}
	if w.CacheWrite1hTokens != 0 {
		v := w.CacheWrite1hTokens
		out.CacheWrite1hTokens = &v
	}
	if w.WebSearchCalls != 0 {
		v := w.WebSearchCalls
		out.WebSearchCalls = &v
	}
	if w.WebFetchCalls != 0 {
		v := w.WebFetchCalls
		out.WebFetchCalls = &v
	}
	return out
}

// decodePayloadStopReason pulls payload.stop_reason out of the JSON
// payload. Returns nil when missing or empty so GraphQL renders null
// rather than an empty string.
func decodePayloadStopReason(payload map[string]any) *string {
	if payload == nil {
		return nil
	}
	v, ok := payload["stop_reason"].(string)
	if !ok || v == "" {
		return nil
	}
	return &v
}

// aggregateSessionUsage walks every event in a session and sums the
// numeric counters of any `payload.usage` present. Non-numeric fields
// (vendor / model / service_tier) collapse to a single value when the
// session is homogeneous, else stay empty — multi-model sessions
// shouldn't pretend to have a single model. Returns nil when no event
// in the session carries usage.
func aggregateSessionUsage(events []*store.Event) *TokenUsage {
	var (
		anyUsage                                                       bool
		inputT, outputT, cacheR, cache5m, cache1h, webSearch, webFetch int
		vendors                                                        = map[string]struct{}{}
		models                                                         = map[string]struct{}{}
		tiers                                                          = map[string]struct{}{}
	)
	for _, se := range events {
		if len(se.Payload) == 0 {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(se.Payload, &p); err != nil {
			continue
		}
		u := decodePayloadUsage(p)
		if u == nil {
			continue
		}
		anyUsage = true
		inputT += u.InputTokens
		outputT += u.OutputTokens
		if u.CacheReadTokens != nil {
			cacheR += *u.CacheReadTokens
		}
		if u.CacheWrite5mTokens != nil {
			cache5m += *u.CacheWrite5mTokens
		}
		if u.CacheWrite1hTokens != nil {
			cache1h += *u.CacheWrite1hTokens
		}
		if u.WebSearchCalls != nil {
			webSearch += *u.WebSearchCalls
		}
		if u.WebFetchCalls != nil {
			webFetch += *u.WebFetchCalls
		}
		if u.Vendor != "" {
			vendors[u.Vendor] = struct{}{}
		}
		if u.Model != "" {
			models[u.Model] = struct{}{}
		}
		if u.ServiceTier != nil && *u.ServiceTier != "" {
			tiers[*u.ServiceTier] = struct{}{}
		}
	}
	if !anyUsage {
		return nil
	}
	out := &TokenUsage{InputTokens: inputT, OutputTokens: outputT}
	if len(vendors) == 1 {
		for v := range vendors {
			out.Vendor = v
		}
	}
	if len(models) == 1 {
		for m := range models {
			out.Model = m
		}
	}
	if len(tiers) == 1 {
		for t := range tiers {
			s := t
			out.ServiceTier = &s
		}
	}
	if cacheR > 0 {
		out.CacheReadTokens = &cacheR
	}
	if cache5m > 0 {
		out.CacheWrite5mTokens = &cache5m
	}
	if cache1h > 0 {
		out.CacheWrite1hTokens = &cache1h
	}
	if webSearch > 0 {
		out.WebSearchCalls = &webSearch
	}
	if webFetch > 0 {
		out.WebFetchCalls = &webFetch
	}
	return out
}
