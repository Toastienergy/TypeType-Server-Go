package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Profiles is ProfileAccountService: every account owns one or more profiles, each profile being a users row of
// its own; the owner's profile id is the account id.
type Profiles struct {
	db *pgxpool.Pool
}

// conn is what *pgxpool.Pool and pgx.Tx have in common.
type conn interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// OwnerID returns the account owning a profile, registering a lone account as its own default profile the first
// time it is seen. "" means the profile does not exist (or is a guest).
func (p *Profiles) OwnerID(ctx context.Context, profileID string) (string, error) {
	return ownerID(ctx, p.db, profileID)
}

func ownerID(ctx context.Context, db conn, profileID string) (string, error) {
	if strings.HasPrefix(profileID, "guest:") {
		return "", nil
	}
	var owner string
	err := db.QueryRow(ctx, "SELECT owner_user_id FROM profile_accounts WHERE profile_id = $1", profileID).Scan(&owner)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var exists bool
	if err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", profileID).Scan(&exists); err != nil || !exists {
		return "", err
	}
	now := nowMs()
	if _, err := db.Exec(ctx, `INSERT INTO profile_accounts (profile_id, owner_user_id, display_name, is_default, last_used_at, created_at, updated_at)
VALUES ($1, $1, 'Profile', TRUE, 0, $2, $2) ON CONFLICT DO NOTHING`, profileID, now); err != nil {
		return "", err
	}
	return profileID, nil
}

// IsOwner is false for guests and secondary profiles; an unknown id counts as an owner, as in Kotlin.
func (p *Profiles) IsOwner(ctx context.Context, profileID string) (bool, error) {
	if strings.HasPrefix(profileID, "guest:") {
		return false, nil
	}
	var owner string
	err := p.db.QueryRow(ctx, "SELECT owner_user_id FROM profile_accounts WHERE profile_id = $1", profileID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return owner == profileID, err
}

// ResolveSignIn picks the profile a sign-in lands on: the last used one, else the default, and marks it used.
func (p *Profiles) ResolveSignIn(ctx context.Context, ownerUserID string) (string, error) {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	owner, err := ownerID(ctx, tx, ownerUserID)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return ownerUserID, tx.Commit(ctx)
	}
	if err := ensureDefault(ctx, tx, owner); err != nil {
		return "", err
	}
	var selected string
	err = tx.QueryRow(ctx, `SELECT profile_id FROM profile_accounts WHERE owner_user_id = $1
ORDER BY (last_used_at > 0) DESC, CASE WHEN last_used_at > 0 THEN last_used_at ELSE 0 END DESC, is_default DESC LIMIT 1`, owner).Scan(&selected)
	if errors.Is(err, pgx.ErrNoRows) {
		selected, err = owner, nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE profile_accounts SET last_used_at = $2 WHERE profile_id = $1", selected, nowMs()); err != nil {
		return "", err
	}
	return selected, tx.Commit(ctx)
}

// ensureDefault keeps exactly one default profile per account: the most recently used default, else the oldest.
func ensureDefault(ctx context.Context, tx pgx.Tx, owner string) error {
	var selected string
	err := tx.QueryRow(ctx, `SELECT profile_id FROM profile_accounts WHERE owner_user_id = $1
ORDER BY is_default DESC, CASE WHEN is_default THEN last_used_at END DESC NULLS LAST, created_at ASC LIMIT 1`, owner).Scan(&selected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE profile_accounts SET is_default = FALSE WHERE owner_user_id = $1 AND profile_id <> $2 AND is_default", owner, selected); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE profile_accounts SET is_default = TRUE WHERE profile_id = $1 AND NOT is_default", selected)
	return err
}
