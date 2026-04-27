package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
	const q = `SELECT id, ts, session_id, turn_id, actor_type, actor_id, actor_model,
		kind, payload, parents, refs, hash, prev_hash, sig
		FROM events WHERE session_id = $1 ORDER BY ts ASC LIMIT $2`
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
	const q = `SELECT hash FROM events WHERE session_id = $1 ORDER BY ts DESC LIMIT 1`
	var h string
	err := p.pool.QueryRow(ctx, q, sessionID).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
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
