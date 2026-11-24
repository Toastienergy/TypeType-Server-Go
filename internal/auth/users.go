package auth

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jackc/pgx/v5"
)

type SessionConfig struct {
	RefreshTTL           time.Duration
	AllowInsecureCookies bool
}

func SessionConfigFromEnv() SessionConfig {
	days := int64(30)
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("AUTH_SESSION_TTL_DAYS")), 10, 64); err == nil {
		days = max(1, min(v, 365))
	}
	insecure := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AUTH_ALLOW_INSECURE_COOKIES"))) {
	case "1", "true", "yes", "on":
		insecure = true
	}
	return SessionConfig{RefreshTTL: time.Duration(days) * 24 * time.Hour, AllowInsecureCookies: insecure}
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// JavaLength counts UTF-16 code units, the unit Kotlin's String.length uses in every limit the API enforces.
func JavaLength(s string) int { return len(utf16.Encode([]rune(s))) }

func ValidPublicUsername(v string) bool {
	n := JavaLength(v)
	return n >= 3 && n <= 32 && usernameRe.MatchString(v)
}

var ErrExists = errors.New("user exists")

func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin')").Scan(&ok)
	return ok, err
}

func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&ok)
	return ok, err
}

// Register creates a local account (the first one, or the first after admins disappear, becomes admin) and signs
// in its sign-in profile.
func (s *Service) Register(ctx context.Context, email, password, name string) (Tokens, error) {
	hash, err := HashPassword(ctx, password)
	if err != nil {
		return Tokens{}, err
	}
	hasAdmin, err := s.HasAdmin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	role := "user"
	if !hasAdmin {
		role = "admin"
	}
	var public *string
	if t := strings.TrimSpace(name); ValidPublicUsername(t) {
		public = &t
	}
	id := UUID()
	now := nowMs()
	if _, err := s.db.Exec(ctx, `INSERT INTO users (id, email, password_hash, name, public_username, role, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, id, email, hash, name, public, role, now); err != nil {
		return Tokens{}, err
	}
	profile, err := s.Profiles.ResolveSignIn(ctx, id)
	if err != nil {
		return Tokens{}, err
	}
	return s.Issue(ctx, profile, "")
}

// Login takes an email (contains @) or a public username, case-insensitive. Secondary profiles have placeholder
// credentials and can never sign in directly.
func (s *Service) Login(ctx context.Context, identifier, password string) (Tokens, bool, error) {
	ident := strings.ToLower(strings.TrimSpace(identifier))
	if ident == "" {
		return Tokens{}, false, nil
	}
	q := "SELECT id, password_hash FROM users WHERE lower(public_username) = $1"
	if strings.Contains(ident, "@") {
		q = "SELECT id, password_hash FROM users WHERE lower(email) = $1"
	}
	rows, err := s.db.Query(ctx, q, ident)
	if err != nil {
		return Tokens{}, false, err
	}
	var id, hash string
	n := 0
	for rows.Next() {
		n++
		rows.Scan(&id, &hash)
	}
	rows.Close()
	if n != 1 {
		return Tokens{}, false, rows.Err()
	}
	owner, err := s.Profiles.IsOwner(ctx, id)
	if err != nil || !owner {
		return Tokens{}, false, err
	}
	if !CheckPassword(ctx, password, hash) {
		return Tokens{}, false, nil
	}
	profile, err := s.Profiles.ResolveSignIn(ctx, id)
	if err != nil {
		return Tokens{}, false, err
	}
	t, err := s.Issue(ctx, profile, "")
	return t, err == nil, err
}

func (s *Service) IssueForOwner(ctx context.Context, ownerID string) (Tokens, error) {
	profile, err := s.Profiles.ResolveSignIn(ctx, ownerID)
	if err != nil {
		return Tokens{}, err
	}
	return s.Issue(ctx, profile, "")
}

// Role is the role of the account owning the profile; guests are users.
func (s *Service) Role(ctx context.Context, userID string) (*string, error) {
	if strings.HasPrefix(userID, "guest:") {
		r := "user"
		return &r, nil
	}
	owner, err := s.Profiles.OwnerID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		owner = userID
	}
	var role string
	err = s.db.QueryRow(ctx, "SELECT role FROM users WHERE id = $1", owner).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &role, err
}
