package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/youtube"
)

type pushCandidate struct {
	serviceID   int
	channel     string
	videoID     string
	publishedAt int64
	video       youtube.VideoItem
}

type pushTarget struct {
	id, deviceID, endpoint, p256dh, auth string
}

type pushWork struct {
	eventID string
	device  pushTarget
	payload []byte
}

// pushPayload is UnifiedPushNotificationPayload; CacheJson leaves out a thumbnailUrl equal to its "" default.
type pushPayload struct {
	Version          int    `json:"version"`
	EventType        string `json:"eventType"`
	ServiceID        int    `json:"serviceId"`
	ServiceName      string `json:"serviceName"`
	EventID          string `json:"eventId"`
	VideoID          string `json:"videoId"`
	VideoURL         string `json:"videoUrl"`
	ChannelID        string `json:"channelId"`
	ChannelName      string `json:"channelName"`
	ChannelAvatarURL string `json:"channelAvatarUrl"`
	InstanceID       string `json:"instanceId"`
	AccountID        string `json:"accountId"`
	PublishedAt      int64  `json:"publishedAt"`
	Title            string `json:"title"`
	ThumbnailURL     string `json:"thumbnailUrl,omitempty"`
}

// startPushPoller is PushNotificationScheduler: ten seconds after start, then every interval (300 s by default).
func (s *Server) startPushPoller() {
	if !s.features.pushEnabled {
		return
	}
	interval := time.Duration(max(30, min(config.IntEnv("TYPE_TYPE_PUSH_NOTIFICATIONS_INTERVAL_SECONDS", 300), 86400))) * time.Second
	go func() {
		time.Sleep(10 * time.Second)
		for {
			s.pollPushAccounts(context.Background())
			time.Sleep(interval)
		}
	}()
}

func (s *Server) pollPushAccounts(ctx context.Context) {
	rows, err := s.DB.Query(ctx, "SELECT DISTINCT user_id FROM push_devices")
	if err != nil {
		slog.Warn("push_notifications", "event", "registry_failed", "error", err.Error())
		return
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		slog.Warn("push_notifications", "event", "registry_failed", "error", err.Error())
		return
	}
	for _, u := range users {
		if err := s.pollPushUser(ctx, u); err != nil {
			slog.Warn("push_notifications", "event", "poll_failed", "user", u[:min(8, len(u))], "error", err.Error())
		}
	}
}

// pushCandidates is PushNotificationSupport.candidates: a regular video, published after the user followed one of the
// channels that brought it into the feed.
func pushCandidates(v youtube.VideoItem, sources map[string][]string, followed map[string]int64) []pushCandidate {
	if v.IsShortFormContent || v.IsLiveContent || v.IsPostLive || v.IsLive {
		return nil
	}
	channels := sources[feedKey(v)]
	if len(channels) == 0 {
		channels = []string{v.UploaderURL}
	}
	published := publishedMillis(v)
	if published <= 0 {
		return nil
	}
	id := strings.TrimSpace(v.ID)
	if id == "" {
		id = strings.TrimSpace(v.URL)
	}
	if id == "" {
		return nil
	}
	var out []pushCandidate
	seen := map[string]bool{}
	for _, raw := range channels {
		c := db.CanonicalChannelURL(raw)
		since, ok := followed[c]
		if !ok || since > 0 && published < since || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, pushCandidate{videoServiceID(v), c, id, published, v})
	}
	return out
}

func pushEvent(instance string, c pushCandidate) string {
	sum := sha256.Sum256([]byte(instance + "|subscription_new_video|" + strconv.Itoa(c.serviceID) + "|" + c.channel + "|" + c.videoID))
	return hex.EncodeToString(sum[:])
}

// pushFeed is SubscriptionFeedService.getAllWithSources: the current feed, or a cold build waited on for 400 ms.
func (s *Server) pushFeed(user string) (*feedSnapshot, bool) {
	s.feeds.mu.Lock()
	s.feeds.touched[user] = time.Now()
	snap := s.feeds.current[user]
	s.feeds.mu.Unlock()
	if snap == nil {
		select {
		case <-s.feeds.refresh(user):
		case <-time.After(400 * time.Millisecond):
		}
		s.feeds.mu.Lock()
		snap = s.feeds.current[user]
		s.feeds.mu.Unlock()
	}
	return snap, snap != nil && snap.generatedAt != 0
}

// pollPushUser is PushNotificationService.pollUser. The first poll of an account only records what is already in the
// feed; later polls queue one delivery per device for each new video of a channel with notifications on, and retry
// a delivery that is not acknowledged after 30 s, three attempts at most.
func (s *Server) pollPushUser(ctx context.Context, user string) error {
	followed := map[string]int64{}
	rows, err := s.DB.Query(ctx, "SELECT channel_url, subscribed_at FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u string
		var at int64
		if rows.Scan(&u, &at) == nil {
			followed[db.CanonicalChannelURL(u)] = at
		}
	}
	rows.Close()
	if len(followed) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	var devices []pushTarget
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if err := removeExpiredDevices(ctx, tx, user, now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id, device_id, endpoint, p256dh, auth_secret FROM push_devices WHERE user_id = $1 AND p256dh IS NOT NULL AND auth_secret IS NOT NULL", user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d pushTarget
			if err := rows.Scan(&d.id, &d.deviceID, &d.endpoint, &d.p256dh, &d.auth); err != nil {
				return err
			}
			devices = append(devices, d)
		}
		return rows.Err()
	})
	if err != nil || len(devices) == 0 {
		return err
	}
	snap, ok := s.pushFeed(user)
	if !ok {
		return nil
	}
	enabled := map[string]bool{}
	prefs, err := s.DB.Query(ctx, "SELECT channel_url FROM channel_notification_preferences WHERE user_id = $1 AND enabled", user)
	if err != nil {
		return err
	}
	for prefs.Next() {
		var u string
		if prefs.Scan(&u) == nil {
			enabled[db.CanonicalChannelURL(u)] = true
		}
	}
	prefs.Close()
	var candidates []pushCandidate
	unique := map[string]bool{}
	for _, v := range snap.videos {
		for _, c := range pushCandidates(v, snap.sources, followed) {
			k := strconv.Itoa(c.serviceID) + "\x00" + c.channel + "\x00" + c.videoID
			if !unique[k] {
				unique[k] = true
				candidates = append(candidates, c)
			}
		}
	}
	instance := s.pushInstanceID()
	var work []pushWork
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		work = nil
		tag, err := tx.Exec(ctx, "INSERT INTO push_notification_baselines (user_id, initialized_at) VALUES ($1, $2) ON CONFLICT DO NOTHING", user, now)
		if err != nil {
			return err
		}
		first := tag.RowsAffected() == 1
		for _, c := range candidates {
			seen, err := tx.Exec(ctx, `INSERT INTO push_notification_seen_videos (user_id, service_id, channel_id, video_id, first_seen_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING`, user, c.serviceID, c.channel, c.videoID, now)
			if err != nil {
				return err
			}
			if first || !enabled[c.channel] {
				continue
			}
			event := pushEvent(instance, c)
			if _, err := tx.Exec(ctx, `INSERT INTO push_notification_events (event_id, event_type, instance_id, service_id, channel_id, video_id, video_url, title,
channel_name, channel_avatar_url, published_at, created_at) VALUES ($1, 'subscription_new_video', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
				event, instance, c.serviceID, c.channel, c.videoID, c.video.URL, c.video.Title, c.video.UploaderName, c.video.UploaderAvatarURL, c.publishedAt, now); err != nil {
				return err
			}
			var payload []byte
			for _, d := range devices {
				claimed, err := claimPush(ctx, tx, event, user, d.deviceID, seen.RowsAffected() == 1, now)
				if err != nil {
					return err
				}
				if !claimed {
					continue
				}
				if payload == nil {
					payload = encodePushPayload(pushPayload{Version: 1, EventType: "subscription_new_video", ServiceID: c.serviceID, ServiceName: serviceName(c.serviceID),
						EventID: event, VideoID: c.videoID, VideoURL: c.video.URL, ChannelID: c.channel, ChannelName: c.video.UploaderName,
						ChannelAvatarURL: c.video.UploaderAvatarURL, InstanceID: instance, AccountID: user, PublishedAt: c.publishedAt, Title: c.video.Title,
						ThumbnailURL: c.video.ThumbnailURL})
				}
				work = append(work, pushWork{event, d, payload})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, w := range work {
		s.deliverPush(ctx, user, w)
	}
	return nil
}

func encodePushPayload(p pushPayload) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(p)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// claimPush is PushNotificationDeliveryStore.claim: a new delivery only for a video seen for the first time, a retry
// only for a delivery still open, under three attempts and untouched for 30 s.
func claimPush(ctx context.Context, tx pgx.Tx, event, user, device string, create bool, now int64) (bool, error) {
	var status string
	var attempts int
	var updated int64
	err := tx.QueryRow(ctx, "SELECT status, attempts, updated_at FROM push_notification_deliveries WHERE event_id = $1 AND user_id = $2 AND device_id = $3",
		event, user, device).Scan(&status, &attempts, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		if !create {
			return false, nil
		}
		tag, err := tx.Exec(ctx, `INSERT INTO push_notification_deliveries (event_id, user_id, device_id, status, attempts, last_error, updated_at)
VALUES ($1, $2, $3, 'pending', 1, NULL, $4) ON CONFLICT DO NOTHING`, event, user, device, now)
		return err == nil && tag.RowsAffected() == 1, err
	}
	if err != nil {
		return false, err
	}
	if !inList(status, pushRetryable) || attempts >= pushMaxAttempts || updated > now-pushRetryDelayMs {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE push_notification_deliveries SET status = 'pending', attempts = $4 + 1, last_error = NULL, updated_at = $6
WHERE event_id = $1 AND user_id = $2 AND device_id = $3 AND status = ANY($7) AND attempts = $4 AND updated_at = $5`,
		event, user, device, attempts, updated, now, pushRetryable)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Server) deliverPush(ctx context.Context, user string, w pushWork) {
	result, code := sendPush(ctx, w.device.endpoint, w.eventID, w.payload, w.device.p256dh, w.device.auth)
	mark := func(status string, lastError *string) {
		s.DB.Exec(ctx, `UPDATE push_notification_deliveries SET status = $4, last_error = $5, updated_at = $6
WHERE event_id = $1 AND user_id = $2 AND device_id = $3 AND status = 'pending'`, w.eventID, user, w.device.deviceID, status, lastError, time.Now().UnixMilli())
	}
	reason := func(v string) *string { return &v }
	switch result {
	case pushDelivered:
		mark("awaiting_ack", nil)
		slog.Info("push_notifications", "event", "distributor_accepted", "trace", w.eventID[:16])
	case pushInvalidEndpoint:
		s.DB.Exec(ctx, "DELETE FROM push_devices WHERE id = $1", w.device.id)
		mark("invalid", reason("endpoint_invalid"))
	case pushInvalidSubscription:
		s.DB.Exec(ctx, "DELETE FROM push_devices WHERE id = $1", w.device.id)
		mark("invalid", reason("subscription_keys_invalid"))
	default:
		if code > 0 {
			mark("failed", reason(strconv.Itoa(code)))
		} else {
			mark("failed", reason("network"))
		}
	}
}
