package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CaptionStyles struct {
	FontFamily       string `json:"fontFamily"`
	FontSize         string `json:"fontSize"`
	TextColor        string `json:"textColor"`
	TextOpacity      string `json:"textOpacity"`
	TextShadow       string `json:"textShadow"`
	TextBg           string `json:"textBg"`
	TextBgOpacity    string `json:"textBgOpacity"`
	DisplayBg        string `json:"displayBg"`
	DisplayBgOpacity string `json:"displayBgOpacity"`
}

var sponsorModes = map[string]bool{"auto_skip": true, "mark_only": true, "disabled": true}

func DefaultCategoryActions() map[string]string {
	return map[string]string{"sponsor": "auto_skip", "selfpromo": "auto_skip", "exclusive_access": "mark_only",
		"interaction": "auto_skip", "poi_highlight": "mark_only", "intro": "auto_skip", "outro": "auto_skip",
		"preview": "auto_skip", "filler": "auto_skip", "chapter": "mark_only", "music_offtopic": "auto_skip"}
}

func withDefaultActions(m map[string]string) map[string]string {
	out := DefaultCategoryActions()
	for k, v := range m {
		out[k] = v
	}
	return out
}

func GetSettings(ctx context.Context, db *pgxpool.Pool, userID string) (Settings, error) {
	s := DefaultSettings()
	var captions, actions string
	err := db.QueryRow(ctx, "SELECT "+settingsColumns+" FROM settings WHERE user_id = $1", userID).
		Scan(settingsScanTargets(&s, &captions, &actions)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return s, err
	}
	s.CaptionStyles = CaptionStyles{}
	json.Unmarshal([]byte(captions), &s.CaptionStyles)
	var a map[string]string
	if json.Unmarshal([]byte(actions), &a) != nil {
		a = nil
	}
	valid := map[string]string{}
	for k, v := range a {
		if !sponsorModes[v] {
			valid = nil
			break
		}
		valid[k] = v
	}
	s.SponsorBlockCategoryActions = withDefaultActions(valid)
	if !sponsorModes[s.SponsorBlockMode] {
		s.SponsorBlockMode = "auto_skip"
	}
	s.AccessMode = ToAccessMode(s.AccessMode)
	return s, nil
}

func inSet[T comparable](v T, set ...T) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// Normalize is SettingsItem.normalized.
func (s *Settings) Normalize() {
	if s.DefaultLandingPage == "" || isBlank(s.DefaultLandingPage) {
		s.DefaultLandingPage = "home"
	}
	if !inSet(s.VideoGridColumns, 0, 4, 5, 6) {
		s.VideoGridColumns = 0
	}
	if !inSet(s.RelatedVideoSize, "default", "large") {
		s.RelatedVideoSize = "default"
	}
	if math.IsInf(s.DefaultPlaybackSpeed, 0) || math.IsNaN(s.DefaultPlaybackSpeed) {
		s.DefaultPlaybackSpeed = 1
	} else {
		s.DefaultPlaybackSpeed = math.Max(0.25, math.Min(4, s.DefaultPlaybackSpeed))
	}
	if !inSet(s.HoldPlaybackSpeed, 1.5, 2.0, 2.5, 3.0, 4.0) {
		s.HoldPlaybackSpeed = 2
	}
	s.AccessMode = ToAccessMode(s.AccessMode)
	s.AutoplayCountdownSeconds = clamp(s.AutoplayCountdownSeconds, 0, 60)
	s.AmbientLightSpread = clamp(s.AmbientLightSpread, 0, 200)
	s.AmbientLightBlur = clamp(s.AmbientLightBlur, 0, 100)
	s.AmbientLightBrightness = clamp(s.AmbientLightBrightness, 50, 150)
	s.SponsorBlockCategoryActions = withDefaultActions(s.SponsorBlockCategoryActions)
	s.SponsorBlockMinimumDuration = max(0, s.SponsorBlockMinimumDuration)
	if !inSet(s.DeArrowTitleMode, "original", "dearrow") {
		s.DeArrowTitleMode = "dearrow"
	}
	if !inSet(s.DeArrowThumbnailMode, "original", "dearrow", "random", "dearrow_or_random") {
		s.DeArrowThumbnailMode = "dearrow_or_random"
	}
	if !inSet(s.DeArrowTrustMode, "accepted", "locked") {
		s.DeArrowTrustMode = "accepted"
	}
	if !inSet(s.SeekbarIcon, "typetype", "custom", "none") {
		s.SeekbarIcon = "typetype"
	}
	if !inSet(s.PlayerEngine, "vidstack", "videojs") {
		s.PlayerEngine = "videojs"
	}
	if s.WatchedThresholdPercent != 0 && (s.WatchedThresholdPercent < 50 || s.WatchedThresholdPercent > 100) {
		s.WatchedThresholdPercent = 0
	}
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// ValidModes reports whether the SponsorBlock modes are ones the enum knows; kotlinx refuses anything else.
func (s *Settings) ValidModes() bool {
	if !sponsorModes[s.SponsorBlockMode] {
		return false
	}
	for _, v := range s.SponsorBlockCategoryActions {
		if !sponsorModes[v] {
			return false
		}
	}
	return true
}

// UpsertSettings writes the normalized settings and returns them with the stored seekbar icon URL, which only the
// icon upload changes.
func UpsertSettings(ctx context.Context, db *pgxpool.Pool, userID string, s Settings) (Settings, error) {
	s.Normalize()
	captions, _ := json.Marshal(s.CaptionStyles)
	actions, _ := json.Marshal(s.SponsorBlockCategoryActions)
	vals := settingsWriteValues(&s, string(captions), string(actions))

	placeholders := ""
	sets := ""
	cols := splitCols(settingsWriteColumns)
	for i := range vals {
		if i > 0 {
			placeholders += ", "
			sets += ", "
		}
		placeholders += "$" + itoa(i+2)
		sets += cols[i] + " = EXCLUDED." + cols[i]
	}
	var url string
	err := db.QueryRow(ctx, "INSERT INTO settings (user_id, "+settingsWriteColumns+") VALUES ($1, "+placeholders+
		") ON CONFLICT (user_id) DO UPDATE SET "+sets+" RETURNING seekbar_icon_url", append([]any{userID}, vals...)...).Scan(&url)
	if err != nil {
		return s, err
	}
	s.SeekbarIconUrl = url
	return s, nil
}
