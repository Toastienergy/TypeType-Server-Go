package api

import (
	"context"
	"hash/crc32"
	"net/url"
	"regexp"
	"strings"
	"time"

	"typetype.dev/server/internal/youtube"
)

const (
	dayMs          = int64(86_400_000)
	focusExitDelay = dayMs
	studyAddDelay  = dayMs
)

type focusRow struct {
	enabled                     bool
	entertainment, study        *string
	transitionDays, archiveDays int
	strict, grayscale           bool
	schedule                    string
	timezone                    *string
	startedAt                   int64
	disableAt                   *int64
}

func defaultFocusRow() focusRow {
	return focusRow{transitionDays: 60, archiveDays: 7, schedule: "always"}
}

// readFocus is readSettledFocusRow: a Focus whose exit delay has passed is switched off on read.
func (s *Server) readFocus(ctx context.Context, user string, now int64) (focusRow, error) {
	f := defaultFocusRow()
	err := s.DB.QueryRow(ctx, `SELECT enabled, entertainment_group_id, study_group_id, transition_days, archive_days, strict_blocking,
grayscale_thumbnails, schedule, timezone, started_at, disable_at FROM focus_settings WHERE user_id = $1`, user).Scan(&f.enabled,
		&f.entertainment, &f.study, &f.transitionDays, &f.archiveDays, &f.strict, &f.grayscale, &f.schedule, &f.timezone, &f.startedAt, &f.disableAt)
	if isNoRows(err) {
		return defaultFocusRow(), nil
	}
	if err != nil {
		return f, err
	}
	if f.enabled && f.disableAt != nil && now >= *f.disableAt {
		f.enabled, f.disableAt = false, nil
		err = s.writeFocus(ctx, user, f, now)
	}
	return f, err
}

func (s *Server) writeFocus(ctx context.Context, user string, f focusRow, now int64) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO focus_settings (user_id, enabled, entertainment_group_id, study_group_id, transition_days, archive_days,
strict_blocking, grayscale_thumbnails, schedule, timezone, started_at, disable_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, entertainment_group_id = EXCLUDED.entertainment_group_id,
study_group_id = EXCLUDED.study_group_id, transition_days = EXCLUDED.transition_days, archive_days = EXCLUDED.archive_days,
strict_blocking = EXCLUDED.strict_blocking, grayscale_thumbnails = EXCLUDED.grayscale_thumbnails, schedule = EXCLUDED.schedule,
timezone = EXCLUDED.timezone, started_at = EXCLUDED.started_at, disable_at = EXCLUDED.disable_at, updated_at = EXCLUDED.updated_at`,
		user, f.enabled, f.entertainment, f.study, f.transitionDays, f.archiveDays, f.strict, f.grayscale, f.schedule, f.timezone, f.startedAt,
		f.disableAt, now)
	return err
}

func validSchedule(schedule string, tz *string) bool {
	switch schedule {
	case "always":
		return true
	case "weekdays":
		if tz == nil {
			return false
		}
		_, err := time.LoadLocation(*tz)
		return err == nil
	}
	return false
}

func scheduleActive(schedule string, tz *string, now int64) bool {
	if schedule != "weekdays" || tz == nil {
		return true
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return true
	}
	d := time.UnixMilli(now).In(loc).Weekday()
	return d != time.Saturday && d != time.Sunday
}

func (f focusRow) active(now int64) bool {
	return f.enabled && f.entertainment != nil && scheduleActive(f.schedule, f.timezone, now)
}

var (
	youtubeHostRe = regexp.MustCompile(`(?i)^https://(?:www\.|m\.|music\.)youtube\.com`)
	channelTabs   = map[string]bool{"featured": true, "videos": true, "shorts": true, "streams": true, "playlists": true, "community": true, "about": true}
	focusVideoRe  = regexp.MustCompile(`(?i)(?:[?&]v=|/(?:shorts|embed|live)/|youtu\.be/)([A-Za-z0-9_-]{6,})`)
)

// channelKey is normalizeChannelKey: one spelling for every way a channel URL is written.
func channelKey(v string) string {
	v = strings.TrimSpace(v)
	v, _, _ = strings.Cut(v, "#")
	v, _, _ = strings.Cut(v, "?")
	v = strings.TrimSuffix(v, "/")
	v = strings.ReplaceAll(v, "http://", "https://")
	v = youtubeHostRe.ReplaceAllString(v, "https://youtube.com")
	u, err := url.Parse(v)
	if err != nil || !strings.EqualFold(u.Hostname(), "youtube.com") {
		return v
	}
	var segs []string
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			segs = append(segs, p)
		}
	}
	if len(segs) < 2 || !channelTabs[strings.ToLower(segs[len(segs)-1])] {
		return v
	}
	return u.Scheme + "://" + u.Host + "/" + strings.Join(segs[:len(segs)-1], "/")
}

func videoKey(v string) string {
	if m := focusVideoRe.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
		return "youtube:video:" + m[1]
	}
	return channelKey(v)
}

type focusProfile struct {
	seed          string
	entertainment map[string]bool
	study         map[string]bool
	pending       map[string]int64
	share         float64
	archiveDays   int
	archiveCutoff int64
	strict        bool
}

func (s *Server) channelsOf(ctx context.Context, user, group string) map[string]bool {
	out := map[string]bool{}
	rows, err := s.DB.Query(ctx, "SELECT channel_url FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2", group, user)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			out[channelKey(u)] = true
		}
	}
	return out
}

func (s *Server) partitionStudy(ctx context.Context, user string, f focusRow, now int64, ent map[string]bool) (map[string]bool, map[string]int64) {
	active, pending := map[string]bool{}, map[string]int64{}
	if f.study == nil {
		return active, pending
	}
	rows, err := s.DB.Query(ctx, "SELECT channel_url, added_at FROM subscription_group_memberships WHERE group_id = $1 AND user_id = $2", *f.study, user)
	if err != nil {
		return active, pending
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var added int64
		if rows.Scan(&u, &added) != nil {
			continue
		}
		k := channelKey(u)
		if ent[k] {
			continue
		}
		if f.enabled && added > f.startedAt && now-added < studyAddDelay {
			if prev, ok := pending[k]; !ok || added+studyAddDelay < prev {
				pending[k] = added + studyAddDelay
			}
		} else {
			active[k] = true
		}
	}
	for k := range active {
		delete(pending, k)
	}
	return active, pending
}

// focusFor is FocusService.profileFor: nil when Focus does not change what the user sees.
func (s *Server) focusFor(ctx context.Context, user string) *focusProfile {
	now := time.Now().UnixMilli()
	f, err := s.readFocus(ctx, user, now)
	if err != nil || !f.active(now) {
		return nil
	}
	ent := s.channelsOf(ctx, user, *f.entertainment)
	study, pending := s.partitionStudy(ctx, user, f, now, ent)
	if !f.strict && len(ent) == 0 && len(study) == 0 && len(pending) == 0 {
		return nil
	}
	return &focusProfile{seed: user, entertainment: ent, study: study, pending: pending, share: focusShare(f.startedAt, f.transitionDays, now),
		archiveDays: f.archiveDays, archiveCutoff: now - int64(f.archiveDays)*dayMs, strict: f.strict}
}

func (p *focusProfile) isEntertainment(u string) bool {
	return u != "" && p.entertainment[channelKey(u)]
}
func (p *focusProfile) isStudy(u string) bool {
	return u != "" && !p.isEntertainment(u) && p.study[channelKey(u)]
}

// keepsVideo hides a stable, growing share of entertainment videos: the same ones on every page and reload.
func (p *focusProfile) keepsVideo(videoURL, uploader string) bool {
	if !p.isEntertainment(uploader) || p.share >= 1 {
		return true
	}
	if p.share <= 0 {
		return false
	}
	return float64(crc32.ChecksumIEEE([]byte(p.seed+"|"+videoKey(videoURL))))/4294967296.0 < p.share
}

func published(v youtube.VideoItem) *int64 {
	if v.PublishedAt != nil {
		return v.PublishedAt
	}
	if v.Uploaded > 0 {
		u := v.Uploaded
		return &u
	}
	return nil
}

func (p *focusProfile) withinArchive(at *int64) bool { return at == nil || *at >= p.archiveCutoff }

func (p *focusProfile) keepsFeedVideo(v youtube.VideoItem) bool {
	switch {
	case p.isEntertainment(v.UploaderURL):
		return p.withinArchive(published(v)) && p.keepsVideo(v.URL, v.UploaderURL)
	case p.isStudy(v.UploaderURL):
		return true
	}
	return !p.strict
}

func (p *focusProfile) filter(vs []youtube.VideoItem) []youtube.VideoItem {
	if p == nil {
		return vs
	}
	out := make([]youtube.VideoItem, 0, len(vs))
	for _, v := range vs {
		if p.keepsFeedVideo(v) {
			out = append(out, v)
		}
	}
	return out
}

// playbackBlock is FocusProfile.blockReasonForPlayback with focusBlockedBody: nil when the video may play.
func (p *focusProfile) playbackBlock(uploader string, at *int64) map[string]any {
	if p == nil || !p.strict {
		return nil
	}
	body := func(msg, code string) map[string]any {
		return map[string]any{"error": msg, "code": code, "unlockAt": nil}
	}
	switch {
	case uploader == "":
		return body("Focus mode restricts videos outside your study group", "focus_channel_blocked")
	case p.isEntertainment(uploader):
		if p.withinArchive(at) {
			return nil
		}
		return body("Hidden by Focus mode", "focus_entertainment_blocked")
	case p.isStudy(uploader):
		return nil
	}
	if unlock, ok := p.pending[channelKey(uploader)]; ok {
		b := body("This channel is cooling off before joining your study group", "focus_study_pending")
		b["unlockAt"] = unlock
		return b
	}
	return body("Focus mode restricts videos outside your study group", "focus_channel_blocked")
}
