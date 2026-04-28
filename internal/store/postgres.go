package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const pgUniqueViolation = "23505"

type Postgres struct {
	pool *pgxpool.Pool
}

func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

func (p *Postgres) AppendEvent(ctx context.Context, e *Event) error {
	const q = `
		INSERT INTO events
		  (id, ts, session_id, turn_id, actor_type, actor_id, actor_model,
		   kind, payload, parents, refs, hash, prev_hash, sig)
		VALUES
		  ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`
	_, err := p.pool.Exec(ctx, q,
		e.ID, e.TS, e.SessionID, nullable(e.TurnID),
		e.ActorType, e.ActorID, nullable(e.ActorModel),
		e.Kind, e.Payload, emptyIfNil(e.Parents), emptyIfNil(e.Refs),
		e.Hash, nullable(e.PrevHash), e.Sig,
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrDuplicate
	}
	return err
}

func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (p *Postgres) GetEvent(ctx context.Context, id string) (*Event, error) {
	const q = `SELECT id, ts, session_id, turn_id, actor_type, actor_id, actor_model,
		kind, payload, parents, refs, hash, prev_hash, sig
		FROM events WHERE id = $1`
	row := p.pool.QueryRow(ctx, q, id)
	e, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (p *Postgres) ListBySession(ctx context.Context, sessionID string, limit int) ([]*Event, error) {
	// id ASC, not ts ASC: ULIDs are monotonic at insert time, so id-order
	// equals append-order. ts is set by the hook on its own wall clock, so
	// concurrent hooks can produce events whose ts is earlier than an
	// already-appended event's ts — sorting by ts then walks the hash chain
	// in the wrong order. See issue #38.
	const q = `SELECT id, ts, session_id, turn_id, actor_type, actor_id, actor_model,
		kind, payload, parents, refs, hash, prev_hash, sig
		FROM events WHERE session_id = $1 ORDER BY id ASC LIMIT $2`
	// limit <= 0 means "no limit"; PG would otherwise treat 0 literally.
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	rows, err := p.pool.Query(ctx, q, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) HeadHash(ctx context.Context, sessionID string) (string, error) {
	// "Latest" = last-inserted, identified by max id (ULIDs are monotonic
	// at insert). ts is hook wall-clock and can be skewed across concurrent
	// hooks, so ordering on ts would pick the wrong head on collector
	// restart. See issue #38.
	const q = `SELECT hash FROM events WHERE session_id = $1 ORDER BY id DESC LIMIT 1`
	var h string
	err := p.pool.QueryRow(ctx, q, sessionID).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func (p *Postgres) EventsByRef(ctx context.Context, ref string) ([]*Event, error) {
	const q = `SELECT id, ts, session_id, turn_id, actor_type, actor_id, actor_model,
		kind, payload, parents, refs, hash, prev_hash, sig
		FROM events WHERE $1 = ANY(refs) ORDER BY ts ASC, id ASC`
	rows, err := p.pool.Query(ctx, q, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *Postgres) ListSessions(ctx context.Context, limit int, since time.Time) ([]*SessionSummary, error) {
	// limit <= 0 means "no limit"; PG would otherwise treat 0 literally.
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	// `since` is filtered on aggregated MAX(ts), so it goes in HAVING,
	// not WHERE — a session with old events but a recent event still
	// qualifies. We pass NULL via the zero check so the predicate is a
	// no-op when the caller didn't supply `since`.
	const q = `SELECT session_id, MIN(ts), MAX(ts), COUNT(*)
		FROM events
		GROUP BY session_id
		HAVING $1::timestamptz IS NULL OR MAX(ts) >= $1::timestamptz
		ORDER BY MAX(ts) DESC, session_id ASC
		LIMIT $2`
	var sinceArg any
	if !since.IsZero() {
		sinceArg = since
	}
	rows, err := p.pool.Query(ctx, q, sinceArg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SessionSummary
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.ID, &s.FirstEventAt, &s.LastEventAt, &s.EventCount); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (p *Postgres) AppendLink(ctx context.Context, l *Link) error {
	const q = `INSERT INTO links (from_event, to_event, relation, confidence, inferred_by)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := p.pool.Exec(ctx, q, l.FromEvent, l.ToEvent, l.Relation, l.Confidence, l.InferredBy)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrDuplicate
	}
	return err
}

func (p *Postgres) LinksForEvent(ctx context.Context, eventID string) ([]*Link, error) {
	const q = `SELECT from_event, to_event, relation, confidence, inferred_by
		FROM links WHERE from_event = $1 OR to_event = $1
		ORDER BY relation, from_event, to_event`
	rows, err := p.pool.Query(ctx, q, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.FromEvent, &l.ToEvent, &l.Relation, &l.Confidence, &l.InferredBy); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

func (p *Postgres) LinksForSession(ctx context.Context, sessionID string) ([]*Link, error) {
	const q = `SELECT from_event, to_event, relation, confidence, inferred_by
		FROM links
		WHERE from_event IN (SELECT id FROM events WHERE session_id = $1)
		   OR to_event   IN (SELECT id FROM events WHERE session_id = $1)
		ORDER BY relation, from_event, to_event`
	rows, err := p.pool.Query(ctx, q, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.FromEvent, &l.ToEvent, &l.Relation, &l.Confidence, &l.InferredBy); err != nil {
			return nil, err
		}
		out = append(out, &l)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanEvent(s scanner) (*Event, error) {
	var e Event
	var turn, model, prev *string
	if err := s.Scan(&e.ID, &e.TS, &e.SessionID, &turn,
		&e.ActorType, &e.ActorID, &model,
		&e.Kind, &e.Payload, &e.Parents, &e.Refs,
		&e.Hash, &prev, &e.Sig); err != nil {
		return nil, err
	}
	if turn != nil {
		e.TurnID = *turn
	}
	if model != nil {
		e.ActorModel = *model
	}
	if prev != nil {
		e.PrevHash = *prev
	}
	return &e, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
