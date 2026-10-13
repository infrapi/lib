package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Stale is how long a key may stay in progress before it is presumed abandoned
// — the process that took it died — and another call may take it over.
const Stale = 10 * time.Minute

// Retention is how long a completed key is remembered.
const Retention = 24 * time.Hour

// Postgres stores keys in one table.
type Postgres struct{ db *sql.DB }

// NewPostgres creates the table if needed.
func NewPostgres(ctx context.Context, db *sql.DB) (*Postgres, error) {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS idempotency_keys (
    scope         text        NOT NULL,
    key           text        NOT NULL,
    request_hash  text        NOT NULL,
    state         text        NOT NULL,
    response_code integer,
    response_type text,
    response_body bytea,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);
CREATE INDEX IF NOT EXISTS idempotency_keys_created_at ON idempotency_keys (created_at);`)
	if err != nil {
		return nil, err
	}
	return &Postgres{db: db}, nil
}

func unavailable(err error) error {
	return infraerrors.Unavailable("the idempotency store could not be reached: %v", err)
}

// Begin claims the key, or says why it cannot be.
func (p *Postgres) Begin(ctx context.Context, scope, key, hash string) (Outcome, *Record, error) {
	res, err := p.db.ExecContext(ctx, `
INSERT INTO idempotency_keys (scope, key, request_hash, state) VALUES ($1, $2, $3, 'in_progress')
ON CONFLICT (scope, key) DO NOTHING`, scope, key, hash)
	if err != nil {
		return 0, nil, unavailable(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil, unavailable(err)
	}
	if n == 1 {
		return Proceed, nil, nil
	}

	var (
		storedHash, state string
		code              sql.NullInt64
		ctype             sql.NullString
		body              []byte
		created           time.Time
	)
	err = p.db.QueryRowContext(ctx, `
SELECT request_hash, state, response_code, response_type, response_body, created_at
FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&storedHash, &state, &code, &ctype, &body, &created)
	if errors.Is(err, sql.ErrNoRows) {
		// Abandoned and removed between the two statements: claim it afresh.
		return p.Begin(ctx, scope, key, hash)
	}
	if err != nil {
		return 0, nil, unavailable(err)
	}

	if storedHash != hash {
		return Mismatch, nil, nil
	}
	if state == "done" {
		return Replay, &Record{Code: int(code.Int64), ContentType: ctype.String, Body: body}, nil
	}

	// Still in progress. Taken over only if presumed dead, and only by one
	// caller: the conditional update is the lock.
	if time.Since(created) < Stale {
		return Busy, nil, nil
	}
	res, err = p.db.ExecContext(ctx, `
UPDATE idempotency_keys SET created_at = now()
WHERE scope = $1 AND key = $2 AND state = 'in_progress' AND created_at = $3`, scope, key, created)
	if err != nil {
		return 0, nil, unavailable(err)
	}
	taken, err := res.RowsAffected()
	if err != nil {
		return 0, nil, unavailable(err)
	}
	if taken == 1 {
		slog.WarnContext(ctx, "idempotency key taken over after its first attempt went silent", "key", key)
		return Proceed, nil, nil
	}
	return Busy, nil, nil
}

// Finish remembers the response.
func (p *Postgres) Finish(ctx context.Context, scope, key string, rec Record) error {
	_, err := p.db.ExecContext(ctx, `
UPDATE idempotency_keys SET state = 'done', response_code = $3, response_type = $4, response_body = $5
WHERE scope = $1 AND key = $2`, scope, key, rec.Code, rec.ContentType, rec.Body)
	return err
}

// Abandon forgets the key, so the next attempt runs.
func (p *Postgres) Abandon(ctx context.Context, scope, key string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key)
	return err
}

// Prune forgets completed keys older than Retention.
func (p *Postgres) Prune(ctx context.Context) (int64, error) {
	res, err := p.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`,
		time.Now().Add(-Retention))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
