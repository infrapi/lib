package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // the Postgres driver

	"github.com/infrapi/lib/pkg/app/idempotency"
)

// openStore opens the idempotency store and prunes it hourly until ctx ends.
func openStore(ctx context.Context, dsn string) (*idempotency.Postgres, func(), error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("app: database: %w", err)
	}
	pg, err := idempotency.NewPostgres(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("app: idempotency store: %w", err)
	}
	go prune(ctx, pg)
	return pg, func() { _ = db.Close() }, nil
}

func prune(ctx context.Context, pg *idempotency.Postgres) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := pg.Prune(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("idempotency prune failed", "err", err)
		} else if n > 0 {
			slog.Info("idempotency keys pruned", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
