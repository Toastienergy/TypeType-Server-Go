package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	accessTTL = time.Hour
	guestTTL  = 7 * 24 * time.Hour
	// sessionCacheTTL bounds how long a revocation made by another process could go unseen; revocations made
	// here drop the entry at once.
	sessionCacheTTL = 30 * time.Second
)

type Tokens struct {
	Access  string
	Refresh string
}

type Service struct {
	db       *pgxpool.Pool
	secret   []byte
	Session  SessionConfig
	Profiles *Profiles

	mu       sync.Mutex
	sessions map[string]sessionEntry
	// signed keeps tokens whose signature already checked out: a client sends the same token on every request,
	// and checking it again cost 6 us and 14 allocations. Their times are still compared on every use.
	signed map[string]signedClaims
}

const signedTokens = 4096

type sessionEntry struct {
	userID    string
	expiresAt int64
	revoked   bool
	seen      time.Time
}

func NewService(db *pgxpool.Pool, secret string, cfg SessionConfig) *Service {
	return &Service{db: db, secret: []byte(secret), Session: cfg, Profiles: &Profiles{db: db}, sessions: map[string]sessionEntry{}}
}

func UUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nowMs() int64 { return time.Now().UnixMilli() }

func refreshHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) accessToken(userID, sessionID string) string {
	return Sign(s.secret, Claims{ID: UUID(), Subject: userID, SessionID: sessionID, Expires: time.Now().Add(accessTTL).Unix()})
}

// Issue creates or rotates a session: a new access token and a new refresh token, the refresh token stored hashed.
// It refuses a session id that belongs to another user.
func (s *Service) Issue(ctx context.Context, userID, sessionID string) (Tokens, error) {
	if sessionID == "" {
		sessionID = UUID()
	}
	now := nowMs()
	refresh := UUID() + UUID()
	access := s.accessToken(userID, sessionID)
	expires := now + s.Session.RefreshTTL.Milliseconds()
	tag, err := s.db.Exec(ctx, `INSERT INTO sessions (id, user_id, token, refresh_token_hash, created_at, expires_at, revoked_at)
VALUES ($1, $2, $3, $4, $5, $6, NULL)
ON CONFLICT (id) DO UPDATE SET token = EXCLUDED.token, refresh_token_hash = EXCLUDED.refresh_token_hash,
  expires_at = EXCLUDED.expires_at, revoked_at = NULL WHERE sessions.user_id = EXCLUDED.user_id`,
		sessionID, userID, access, refreshHash(refresh), now, expires)
	if err != nil {
		return Tokens{}, err
	}
	if tag.RowsAffected() == 0 {
		return Tokens{}, errors.New("session belongs to another user")
	}
	s.forget(sessionID)
	return Tokens{Access: access, Refresh: refresh}, nil
}

func (s *Service) Refresh(ctx context.Context, refresh string) (Tokens, bool, error) {
	var id, user string
	var expires int64
	var revoked *int64
	err := s.db.QueryRow(ctx, "SELECT id, user_id, expires_at, revoked_at FROM sessions WHERE refresh_token_hash = $1",
		refreshHash(refresh)).Scan(&id, &user, &expires, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, false, nil
	}
	if err != nil {
		return Tokens{}, false, err
	}
	if revoked != nil || expires <= nowMs() {
		return Tokens{}, false, nil
	}
	t, err := s.Issue(ctx, user, id)
	return t, err == nil, err
}

func (s *Service) Logout(ctx context.Context, refresh string) error {
	if strings.TrimSpace(refresh) == "" {
		return nil
	}
	var ids []string
	rows, err := s.db.Query(ctx, "UPDATE sessions SET revoked_at = $2 WHERE refresh_token_hash = $1 RETURNING id", refreshHash(refresh), nowMs())
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.forget(id)
	}
	return rows.Err()
}

func (s *Service) forget(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

// Forget drops cached session state after another component revoked sessions directly in the table.
func (s *Service) Forget(sessionIDs ...string) {
	for _, id := range sessionIDs {
		s.forget(id)
	}
}

// Verify returns the user id of a valid access token: guests by signature alone, users only while their session
// row exists, belongs to them, is not revoked and has not expired. err is set only when the session could not be
// read, so a database outage never answers 401 and signs clients out.
func (s *Service) Verify(ctx context.Context, token string) (string, bool, error) {
	c, ok := s.claims(token, time.Now())
	if !ok || c.Subject == "" {
		return "", false, nil
	}
	if strings.HasPrefix(c.Subject, "guest:") {
		return c.Subject, true, nil
	}
	if c.SessionID == "" {
		return "", false, nil
	}
	e, ok, err := s.session(ctx, c.SessionID)
	if err != nil {
		return "", false, err
	}
	if !ok || e.userID != c.Subject || e.revoked || e.expiresAt <= nowMs() {
		return "", false, nil
	}
	return c.Subject, true, nil
}

// session reads the session row, cached for sessionCacheTTL; when the database fails, a cached entry of any age
// still answers.
func (s *Service) session(ctx context.Context, id string) (sessionEntry, bool, error) {
	s.mu.Lock()
	cached, ok := s.sessions[id]
	s.mu.Unlock()
	if ok && time.Since(cached.seen) < sessionCacheTTL {
		return cached, true, nil
	}
	var e sessionEntry
	var revoked *int64
	err := s.db.QueryRow(ctx, "SELECT user_id, expires_at, revoked_at FROM sessions WHERE id = $1", id).Scan(&e.userID, &e.expiresAt, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		if ok {
			return cached, true, nil
		}
		return e, false, err
	}
	e.revoked, e.seen = revoked != nil, time.Now()
	s.mu.Lock()
	if len(s.sessions) > 100000 {
		s.sessions = map[string]sessionEntry{}
	}
	s.sessions[id] = e
	s.mu.Unlock()
	return e, true, nil
}

func (s *Service) Guest() string {
	return Sign(s.secret, Claims{ID: UUID(), Subject: "guest:" + UUID(), Expires: time.Now().Add(guestTTL).Unix()})
}

func (s *Service) claims(token string, now time.Time) (Claims, bool) {
	s.mu.Lock()
	v, ok := s.signed[token]
	s.mu.Unlock()
	if !ok {
		var err error
		if v, err = parseSigned(s.secret, token); err != nil {
			return Claims{}, false
		}
		s.mu.Lock()
		if s.signed == nil || len(s.signed) >= signedTokens {
			s.signed = map[string]signedClaims{}
		}
		s.signed[token] = v
		s.mu.Unlock()
	}
	return v.Claims, v.validAt(now)
}
