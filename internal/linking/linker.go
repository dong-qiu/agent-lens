// Package linking computes causal/semantic links between events. v0
// rule: any two events sharing a `git:<sha>` (or other) ref are linked
// with relation "references". Linking happens off the ingest write path
// — Notify is non-blocking; Run consumes a queue and writes to
// store.Links.
//
// Recovery: if the queue overflows or the worker is restarted before
// processing every queued job, links may be missing. A periodic
// backfill scan is a follow-up; for v0 the queue is sized so overflow
// is unusual.
package linking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dongqiu/agent-lens/internal/ingest"
	"github.com/dongqiu/agent-lens/internal/store"
)

// DefaultRelation is the fallback when InferRelation has no specific
// rule for the (kindA, kindB) pair. SPEC §7 vocabulary lives in
// relation.go; the linker calls InferRelation per link.
const DefaultRelation = RelationReferences

// Linker is started once by the main process. Notify is safe to call
// from any goroutine; Run owns the worker loop and exits on context
// cancellation. Construction does not start the worker — call Run.
type Linker struct {
	st    store.Store
	queue chan job
}

type job struct {
	eventID string
	kind    string // event kind, used to refine the link's relation
	refs    []string
}

// New returns a Linker with the requested queue size. queueSize 0 means
// "unbounded behavior is unsafe; use 1024".
func New(st store.Store, queueSize int) *Linker {
	if queueSize <= 0 {
		queueSize = 1024
	}
	return &Linker{
		st:    st,
		queue: make(chan job, queueSize),
	}
}

// Notify enqueues an event for linking. Returns immediately; if the
// queue is full the event is dropped with a log line (the periodic
// backfill, when added, is the recovery path).
func (l *Linker) Notify(ev *ingest.WireEvent) {
	if ev == nil || ev.ID == "" || len(ev.Refs) == 0 {
		return
	}
	j := job{eventID: ev.ID, kind: ev.Kind, refs: append([]string(nil), ev.Refs...)}
	select {
	case l.queue <- j:
	default:
		slog.Warn("linker queue full; dropping event", "id", ev.ID, "queue_cap", cap(l.queue))
	}
}

// Run blocks until ctx is cancelled. Returns when the worker stops.
func (l *Linker) Run(ctx context.Context) {
	slog.Info("linker started", "queue_cap", cap(l.queue))
	for {
		select {
		case <-ctx.Done():
			slog.Info("linker stopped")
			return
		case j := <-l.queue:
			if err := l.process(ctx, j); err != nil {
				slog.Warn("linker process", "id", j.eventID, "err", err)
			}
		}
	}
}

// process is exported for tests via ProcessOnce; production callers
// always go through Run. Errors on individual peers don't abort the
// rest — a transient AppendLink failure on one peer should not strand
// every other link the event would have produced. The first error is
// returned (logged by the caller) so debugging still has a signal.
func (l *Linker) process(ctx context.Context, j job) error {
	var firstErr error
	for _, ref := range j.refs {
		peers, err := l.st.EventsByRef(ctx, ref)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("EventsByRef(%q): %w", ref, err)
			}
			continue
		}
		for _, peer := range peers {
			if peer.ID == j.eventID {
				continue
			}
			link := &store.Link{
				FromEvent:  peer.ID,
				ToEvent:    j.eventID,
				Relation:   InferRelation(peer.Kind, j.kind),
				Confidence: 1.0,
				InferredBy: "shared_ref:" + ref,
			}
			if err := l.st.AppendLink(ctx, link); err != nil && !errors.Is(err, store.ErrDuplicate) {
				if firstErr == nil {
					firstErr = fmt.Errorf("AppendLink(%s→%s): %w", peer.ID, j.eventID, err)
				}
				// keep going; other peers / refs may still succeed.
			}
		}
	}
	return firstErr
}

// ProcessOnce is a test-only synchronous entry point that processes a
// single notification inline. Use it in tests to avoid spinning up the
// worker goroutine.
func (l *Linker) ProcessOnce(ctx context.Context, ev *ingest.WireEvent) error {
	if ev == nil || ev.ID == "" || len(ev.Refs) == 0 {
		return nil
	}
	return l.process(ctx, job{eventID: ev.ID, kind: ev.Kind, refs: ev.Refs})
}
