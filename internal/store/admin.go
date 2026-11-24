package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DefaultInstanceName = "TypeType"

// AdminSettings is AdminSettingsItem, field for field and in the same order.
type AdminSettings struct {
	Name                               string   `json:"name"`
	Tagline                            *string  `json:"tagline"`
	LogoURL                            *string  `json:"logoUrl"`
	BannerURL                          *string  `json:"bannerUrl"`
	MinAndroidClientVersion            *string  `json:"minAndroidClientVersion"`
	AllowRegistration                  bool     `json:"allowRegistration"`
	AllowGuest                         bool     `json:"allowGuest"`
	ForceEmailVerification             bool     `json:"forceEmailVerification"`
	ActiveSessionsEnabled              bool     `json:"activeSessionsEnabled"`
	LocalLoginEnabled                  bool     `json:"localLoginEnabled"`
	OidcAutoRedirect                   bool     `json:"oidcAutoRedirect"`
	YoutubeRemoteLoginEnabled          bool     `json:"youtubeRemoteLoginEnabled"`
	AccessMode                         string   `json:"accessMode"`
	RssEnabled                         bool     `json:"rssEnabled"`
	RssPublicBaseURL                   *string  `json:"rssPublicBaseUrl"`
	RssMaxFeedsPerUser                 int      `json:"rssMaxFeedsPerUser"`
	RssMaxItems                        int      `json:"rssMaxItems"`
	RssMinimumPollMinutes              int      `json:"rssMinimumPollMinutes"`
	RssRateLimitPerMinute              int      `json:"rssRateLimitPerMinute"`
	RecommenderEnabled                 bool     `json:"recommenderEnabled"`
	RecommenderDefaultOn               bool     `json:"recommenderDefaultOn"`
	RecommenderDefaultAlgorithm        *string  `json:"recommenderDefaultAlgorithm"`
	RecommenderAllowedAlgorithms       []string `json:"recommenderAllowedAlgorithms"`
	RecommenderTimeoutMs               int      `json:"recommenderTimeoutMs"`
	RecommenderStreamRequestsPerMinute int      `json:"recommenderStreamRequestsPerMinute"`
	DeviceLoginEnabled                 bool     `json:"deviceLoginEnabled"`
	BilibiliMirrorProbingEnabled       bool     `json:"bilibiliMirrorProbingEnabled"`
}

const (
	AccessUnrestricted = "unrestricted"
	AccessAllowList    = "allow_list"
)

func ToAccessMode(s string) string {
	if s == AccessAllowList {
		return AccessAllowList
	}
	return AccessUnrestricted
}

func DefaultAdminSettings() AdminSettings {
	return AdminSettings{
		Name: DefaultInstanceName, AllowRegistration: true, AllowGuest: true, LocalLoginEnabled: true,
		YoutubeRemoteLoginEnabled: envFlag("YOUTUBE_REMOTE_LOGIN_ENABLED"), AccessMode: AccessUnrestricted,
		RssMaxFeedsPerUser: 10, RssMaxItems: 50, RssMinimumPollMinutes: 5, RssRateLimitPerMinute: 30,
		RecommenderEnabled: envFlag("RECOMMENDER_ENABLED"), RecommenderDefaultOn: envFlag("RECOMMENDER_DEFAULT_ON"),
		RecommenderAllowedAlgorithms: []string{}, DeviceLoginEnabled: true,
	}
}

func envFlag(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true"
}

// Admin keeps the settings row in memory, like AdminSettingsCache: read once, replaced on every write.
type Admin struct {
	db     *pgxpool.Pool
	cached atomic.Pointer[AdminSettings]
}

func NewAdmin(db *pgxpool.Pool) *Admin { return &Admin{db: db} }

const adminColumns = `name, tagline, logo_url, banner_url, min_android_client_version, allow_registration, allow_guest,
force_email_verification, active_sessions_enabled, local_login_enabled, oidc_auto_redirect, youtube_remote_login_enabled,
device_login_enabled, bilibili_mirror_probing_enabled, access_mode, rss_enabled, rss_public_base_url, rss_max_feeds_per_user,
rss_max_items, rss_minimum_poll_minutes, rss_rate_limit_per_minute, recommender_enabled, recommender_default_on,
recommender_default_algorithm, recommender_allowed_algorithms, recommender_timeout_ms, recommender_stream_requests_per_minute`

func (a *Admin) Get(ctx context.Context) (AdminSettings, error) {
	if s := a.cached.Load(); s != nil {
		return *s, nil
	}
	var s AdminSettings
	var algorithms string
	err := a.db.QueryRow(ctx, "SELECT "+adminColumns+" FROM admin_settings LIMIT 1").Scan(
		&s.Name, &s.Tagline, &s.LogoURL, &s.BannerURL, &s.MinAndroidClientVersion, &s.AllowRegistration, &s.AllowGuest,
		&s.ForceEmailVerification, &s.ActiveSessionsEnabled, &s.LocalLoginEnabled, &s.OidcAutoRedirect, &s.YoutubeRemoteLoginEnabled,
		&s.DeviceLoginEnabled, &s.BilibiliMirrorProbingEnabled, &s.AccessMode, &s.RssEnabled, &s.RssPublicBaseURL, &s.RssMaxFeedsPerUser,
		&s.RssMaxItems, &s.RssMinimumPollMinutes, &s.RssRateLimitPerMinute, &s.RecommenderEnabled, &s.RecommenderDefaultOn,
		&s.RecommenderDefaultAlgorithm, &algorithms, &s.RecommenderTimeoutMs, &s.RecommenderStreamRequestsPerMinute)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		s = DefaultAdminSettings()
	case err != nil:
		return s, err
	default:
		s.RecommenderAllowedAlgorithms = splitIDs(algorithms)
	}
	if s, err = normalizeAdmin(s); err != nil {
		return s, err
	}
	a.cached.Store(&s)
	return s, nil
}

func (a *Admin) Upsert(ctx context.Context, in AdminSettings) (AdminSettings, error) {
	s, err := normalizeAdmin(in)
	if err != nil {
		return s, err
	}
	_, err = a.db.Exec(ctx, `INSERT INTO admin_settings (id, `+adminColumns+`) VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9,
$10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
ON CONFLICT (id) DO UPDATE SET (`+adminColumns+`) = (EXCLUDED.name, EXCLUDED.tagline, EXCLUDED.logo_url, EXCLUDED.banner_url,
EXCLUDED.min_android_client_version, EXCLUDED.allow_registration, EXCLUDED.allow_guest, EXCLUDED.force_email_verification,
EXCLUDED.active_sessions_enabled, EXCLUDED.local_login_enabled, EXCLUDED.oidc_auto_redirect, EXCLUDED.youtube_remote_login_enabled,
EXCLUDED.device_login_enabled, EXCLUDED.bilibili_mirror_probing_enabled, EXCLUDED.access_mode, EXCLUDED.rss_enabled,
EXCLUDED.rss_public_base_url, EXCLUDED.rss_max_feeds_per_user, EXCLUDED.rss_max_items, EXCLUDED.rss_minimum_poll_minutes,
EXCLUDED.rss_rate_limit_per_minute, EXCLUDED.recommender_enabled, EXCLUDED.recommender_default_on,
EXCLUDED.recommender_default_algorithm, EXCLUDED.recommender_allowed_algorithms, EXCLUDED.recommender_timeout_ms,
EXCLUDED.recommender_stream_requests_per_minute)`,
		s.Name, s.Tagline, s.LogoURL, s.BannerURL, s.MinAndroidClientVersion, s.AllowRegistration, s.AllowGuest,
		s.ForceEmailVerification, s.ActiveSessionsEnabled, s.LocalLoginEnabled, s.OidcAutoRedirect, s.YoutubeRemoteLoginEnabled,
		s.DeviceLoginEnabled, s.BilibiliMirrorProbingEnabled, s.AccessMode, s.RssEnabled, s.RssPublicBaseURL, s.RssMaxFeedsPerUser,
		s.RssMaxItems, s.RssMinimumPollMinutes, s.RssRateLimitPerMinute, s.RecommenderEnabled, s.RecommenderDefaultOn,
		s.RecommenderDefaultAlgorithm, strings.Join(s.RecommenderAllowedAlgorithms, ","), s.RecommenderTimeoutMs,
		s.RecommenderStreamRequestsPerMinute)
	if err != nil {
		return s, err
	}
	a.cached.Store(&s)
	return s, nil
}

// ErrInvalid carries the message the Kotlin require() would have thrown; routes answer it as a 400.
type ErrInvalid struct{ Msg string }

func (e ErrInvalid) Error() string { return e.Msg }

func normalizeAdmin(s AdminSettings) (AdminSettings, error) {
	base, err := normalizePublicBaseURL(s.RssPublicBaseURL)
	if err != nil {
		return s, err
	}
	if s.RssEnabled && base == nil {
		return s, ErrInvalid{"RSS public base URL is required when RSS is enabled"}
	}
	if s.Name = strings.TrimSpace(s.Name); s.Name == "" {
		s.Name = DefaultInstanceName
	}
	s.Tagline, s.LogoURL, s.BannerURL = optText(s.Tagline), optText(s.LogoURL), optText(s.BannerURL)
	s.MinAndroidClientVersion = optText(s.MinAndroidClientVersion)
	s.AccessMode = ToAccessMode(s.AccessMode)
	s.RssPublicBaseURL = base
	s.RssMaxFeedsPerUser = clamp(s.RssMaxFeedsPerUser, 1, 100)
	s.RssMaxItems = clamp(s.RssMaxItems, 1, 200)
	s.RssMinimumPollMinutes = clamp(s.RssMinimumPollMinutes, 1, 1440)
	s.RssRateLimitPerMinute = clamp(s.RssRateLimitPerMinute, 1, 600)
	s.RecommenderDefaultAlgorithm = optText(s.RecommenderDefaultAlgorithm)
	ids := []string{}
	seen := map[string]bool{}
	for _, a := range s.RecommenderAllowedAlgorithms {
		if a = strings.TrimSpace(a); a != "" && !seen[a] {
			seen[a] = true
			ids = append(ids, a)
		}
	}
	s.RecommenderAllowedAlgorithms = ids
	s.RecommenderTimeoutMs = clamp(s.RecommenderTimeoutMs, 0, 60000)
	s.RecommenderStreamRequestsPerMinute = clamp(s.RecommenderStreamRequestsPerMinute, 0, 10000)
	return s, nil
}

func normalizePublicBaseURL(v *string) (*string, error) {
	t := optText(v)
	if t == nil {
		return nil, nil
	}
	u, err := url.Parse(*t)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, ErrInvalid{"RSS public base URL must be an absolute HTTP or HTTPS URL"}
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, ErrInvalid{"RSS public base URL cannot contain a query or fragment"}
	}
	if u.User != nil {
		return nil, ErrInvalid{"RSS public base URL cannot contain credentials"}
	}
	out := strings.TrimRight(*t, "/")
	return &out, nil
}

func optText(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" {
		return nil
	}
	return &t
}

func splitIDs(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
