package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

//go:embed migrations.sql
var migrationsSQL string

const canonicalizeMarker = "-- go:canonicalize_subscriptions"

// Open connects and brings the schema to the exact state the Kotlin server leaves it in: the tables it creates on
// a fresh base, then its migrations in the same order, so a base from any earlier version keeps working.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7210593)"); err != nil {
		return err
	}
	before, after, _ := strings.Cut(migrationsSQL, canonicalizeMarker+";")
	for i, batch := range []string{schemaSQL + "\n" + before, "", after} {
		if batch == "" {
			if err := canonicalizeSubscriptions(ctx, tx); err != nil {
				return fmt.Errorf("subscriptions: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, strings.ReplaceAll(batch, ";\n--\n", ";\n")); err != nil {
			return fmt.Errorf("schema batch %d: %w", i, err)
		}
	}
	return tx.Commit(ctx)
}

type subscriptionRow struct {
	user, url, name, avatar string
	at                      int64
}

// canonicalizeSubscriptions mirrors DatabaseSubscriptionsCanonicalMigration: canonical channel URLs, one row per
// user and channel (latest subscription wins, then name, then avatar), rewritten only when something changes.
func canonicalizeSubscriptions(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, "SELECT user_id, channel_url, name, avatar_url, subscribed_at FROM subscriptions")
	if err != nil {
		return err
	}
	var all []subscriptionRow
	for rows.Next() {
		var r subscriptionRow
		if err := rows.Scan(&r.user, &r.url, &r.name, &r.avatar, &r.at); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(all) == 0 {
		return err
	}
	changed := false
	type key struct{ user, url string }
	best := map[key]subscriptionRow{}
	var order []key
	for _, r := range all {
		c := CanonicalChannelURL(r.url)
		if c != r.url {
			changed = true
		}
		r.url = c
		k := key{r.user, c}
		cur, ok := best[k]
		if !ok {
			order = append(order, k)
			best[k] = r
			continue
		}
		if later(r, cur) {
			best[k] = r
		}
	}
	if !changed && len(best) == len(all) {
		return nil
	}
	if _, err := tx.Exec(ctx, "DELETE FROM subscriptions"); err != nil {
		return err
	}
	for _, k := range order {
		r := best[k]
		if _, err := tx.Exec(ctx, "INSERT INTO subscriptions (user_id, channel_url, name, avatar_url, subscribed_at) VALUES ($1, $2, $3, $4, $5)",
			r.user, r.url, r.name, r.avatar, r.at); err != nil {
			return err
		}
	}
	return nil
}

func later(a, b subscriptionRow) bool {
	if a.at != b.at {
		return a.at > b.at
	}
	if a.name != b.name {
		return a.name > b.name
	}
	return a.avatar > b.avatar
}
