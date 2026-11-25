package recommender

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Settings is RecommenderSettingsItem. Answers carry every field, as Kotlin's encodeDefaults does; the stored copy
// leaves defaults out (see storedJSON), as Kotlin's CacheJson does, so both servers read what the other wrote.
type Settings struct {
	Enabled         bool                       `json:"enabled"`
	AlgorithmID     *string                    `json:"algorithmId"`
	Parameters      map[string]json.RawMessage `json:"parameters"`
	MutedGroups     []string                   `json:"mutedGroups"`
	IgnoredSignals  []string                   `json:"ignoredSignals"`
	PinnedGroups    []string                   `json:"pinnedGroups"`
	SessionResetAt  *int64                     `json:"sessionResetAt"`
	CustomInterests []string                   `json:"customInterests"`
	UnusedData      []string                   `json:"unusedData"`
	ExcludedItems   []string                   `json:"excludedItems"`
	RetentionDays   map[string]int             `json:"retentionDays"`
}

// Normalized fills the nil lists and maps with empty ones, as the Kotlin defaults are.
func (s Settings) Normalized() Settings {
	for _, l := range []*[]string{&s.MutedGroups, &s.IgnoredSignals, &s.PinnedGroups, &s.CustomInterests, &s.UnusedData, &s.ExcludedItems} {
		if *l == nil {
			*l = []string{}
		}
	}
	if s.Parameters == nil {
		s.Parameters = map[string]json.RawMessage{}
	}
	if s.RetentionDays == nil {
		s.RetentionDays = map[string]int{}
	}
	return s
}

func (s Settings) storedJSON() string {
	m := map[string]any{}
	if s.Enabled {
		m["enabled"] = true
	}
	if s.AlgorithmID != nil {
		m["algorithmId"] = *s.AlgorithmID
	}
	if len(s.Parameters) > 0 {
		m["parameters"] = s.Parameters
	}
	for name, l := range map[string][]string{"mutedGroups": s.MutedGroups, "ignoredSignals": s.IgnoredSignals,
		"pinnedGroups": s.PinnedGroups, "customInterests": s.CustomInterests, "unusedData": s.UnusedData,
		"excludedItems": s.ExcludedItems} {
		if len(l) > 0 {
			m[name] = l
		}
	}
	if s.SessionResetAt != nil {
		m["sessionResetAt"] = *s.SessionResetAt
	}
	if len(s.RetentionDays) > 0 {
		m["retentionDays"] = s.RetentionDays
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// Querier is the part of the pool the recommender uses; *pgxpool.Pool satisfies it.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// SettingsService is RecommenderSettingsService: the stored settings kept in Dragonfly for SettingsTTL, the admin's
// default when a user never saved any.
type SettingsService struct {
	db    Querier
	cache Cache
	cfg   Config
	admin AdminSource
}

func NewSettingsService(db Querier, cache Cache, cfg Config, admin AdminSource) *SettingsService {
	return &SettingsService{db: db, cache: cache, cfg: cfg, admin: admin}
}

func settingsKey(user string) string { return "recommender:settings:v1:" + user }

func (s *SettingsService) Get(ctx context.Context, user string) (Settings, error) {
	if raw, ok, _ := s.cache.Get(ctx, settingsKey(user)); ok {
		var st Settings
		if json.Unmarshal([]byte(raw), &st) == nil {
			return st.Normalized(), nil
		}
	}
	var raw string
	err := s.db.QueryRow(ctx, "SELECT settings_json FROM recommender_user_settings WHERE user_id = $1", user).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		a, aerr := s.admin(ctx)
		if aerr != nil {
			return Settings{}, aerr
		}
		return Settings{Enabled: a.DefaultOn}.Normalized(), nil
	}
	if err != nil {
		return Settings{}, err
	}
	var st Settings
	if json.Unmarshal([]byte(raw), &st) != nil {
		a, _ := s.admin(ctx)
		return Settings{Enabled: a.DefaultOn}.Normalized(), nil
	}
	s.remember(ctx, user, st)
	return st.Normalized(), nil
}

func (s *SettingsService) Upsert(ctx context.Context, user string, given Settings) (Settings, error) {
	st := bounded(given)
	_, err := s.db.Exec(ctx, `INSERT INTO recommender_user_settings (user_id, enabled, algorithm_id, settings_json, updated_at)
VALUES ($1, $2, $3, $4, $5) ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, algorithm_id = EXCLUDED.algorithm_id,
settings_json = EXCLUDED.settings_json, updated_at = EXCLUDED.updated_at`, user, st.Enabled, st.AlgorithmID, st.storedJSON(), time.Now().UnixMilli())
	if err != nil {
		return Settings{}, err
	}
	s.remember(ctx, user, st)
	return st.Normalized(), nil
}

func (s *SettingsService) Forget(ctx context.Context, user string) {
	s.cache.Delete(ctx, settingsKey(user))
}

func (s *SettingsService) remember(ctx context.Context, user string, st Settings) {
	s.cache.Set(ctx, settingsKey(user), st.storedJSON(), s.cfg.SettingsTTL)
}

const (
	maxIDChars       = 64
	maxInterests     = 10
	maxInterestChars = 60
	maxParameters    = 64
	maxRefChars      = 300
	maxCorrections   = 500
	maxRetentionDays = 3_650
)

var spaces = regexp.MustCompile(`\s+`)

// bounded is RecommenderSettingsService.bounded: ids, parameters (never strings), corrections, interests and
// retention cut to their limits.
func bounded(s Settings) Settings {
	if s.AlgorithmID != nil {
		id := truncate(*s.AlgorithmID, maxIDChars)
		s.AlgorithmID = &id
	}
	params := map[string]json.RawMessage{}
	for k, v := range s.Parameters {
		if len(params) >= maxParameters {
			break
		}
		if len(k) <= maxIDChars && !isJSONString(v) {
			params[k] = v
		}
	}
	s.Parameters = params
	s.MutedGroups = boundedRefs(s.MutedGroups)
	s.IgnoredSignals = boundedRefs(s.IgnoredSignals)
	s.PinnedGroups = boundedRefs(s.PinnedGroups)
	s.UnusedData = boundedRefs(s.UnusedData)
	s.ExcludedItems = boundedRefs(s.ExcludedItems)
	var interests []string
	seen := map[string]bool{}
	for _, i := range s.CustomInterests {
		i = truncate(spaces.ReplaceAllString(strings.TrimSpace(i), " "), maxInterestChars)
		if i == "" || seen[strings.ToLower(i)] || len(interests) >= maxInterests {
			continue
		}
		seen[strings.ToLower(i)] = true
		interests = append(interests, i)
	}
	s.CustomInterests = interests
	days := map[string]int{}
	for k, d := range s.RetentionDays {
		if len(days) >= maxParameters {
			break
		}
		if len(k) <= maxIDChars && d >= 1 && d <= maxRetentionDays {
			days[k] = d
		}
	}
	s.RetentionDays = days
	return s
}

func boundedRefs(l []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range l {
		if len(r) <= maxRefChars && !seen[r] && len(out) < maxCorrections {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

func isJSONString(v json.RawMessage) bool {
	t := strings.TrimSpace(string(v))
	return strings.HasPrefix(t, `"`)
}

// truncate cuts to n characters, as Kotlin's take does on UTF-16 text for the ASCII ids it sees.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
