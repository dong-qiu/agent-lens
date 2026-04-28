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
