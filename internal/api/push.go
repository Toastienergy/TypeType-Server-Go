package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
)

var (
	pushDeviceID  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	pushEventID   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	pushReasonID  = regexp.MustCompile(`^[a-z_]{1,64}$`)
	pushOutcomes  = map[string]string{"posted": "client_posted", "suppressed": "client_suppressed", "failed": "failed"}
	pushRetryable = []string{"pending", "failed", "awaiting_ack"}
)

const (
	pushMaxDevices   = 8
	pushMaxExpiryMs  = 366 * 24 * 3600 * 1000
	pushMaxAttempts  = 3
	pushRetryDelayMs = 30_000
)

type pushDevice struct {
	ID        string `json:"id"`
	DeviceID  string `json:"deviceId"`
	Platform  string `json:"platform"`
	ExpiresAt *int64 `json:"expiresAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (s *Server) pushInstanceID() string {
	if v := strings.TrimSpace(os.Getenv("TYPE_TYPE_INSTANCE_ID")); v != "" {
		return v
	}
	return "typetype"
}

func removeExpiredDevices(ctx context.Context, tx pgx.Tx, user string, now int64) error {
	_, err := tx.Exec(ctx, "DELETE FROM push_devices WHERE user_id = $1 AND expires_at IS NOT NULL AND expires_at <= $2", user, now)
	return err
}

// clearPendingPush drops the deliveries still waiting for a channel, as PushNotificationDeliveryStore.clearPending.
func (s *Server) clearPendingPush(ctx context.Context, user, channel string) {
	s.DB.Exec(ctx, `DELETE FROM push_notification_deliveries WHERE user_id = $1 AND status = ANY($3)
AND event_id IN (SELECT event_id FROM push_notification_events WHERE channel_id = $2)`, user, channel, pushRetryable)
}

// pushSubscriptionRemoved is PushNotificationService.onSubscriptionRemoved.
func (s *Server) pushSubscriptionRemoved(ctx context.Context, user, channel string) {
	c := db.CanonicalChannelURL(channel)
	s.DB.Exec(ctx, "DELETE FROM channel_notification_preferences WHERE user_id = $1 AND channel_url = $2", user, c)
	s.clearPendingPush(ctx, user, c)
}

func (s *Server) pushRoutes(rt *Router) {
	enabled := func(w http.ResponseWriter, r *http.Request) bool {
		if s.features.pushEnabled {
			return true
		}
		httpx.Error(w, r, http.StatusNotFound, "Push notifications are unavailable", "push_notifications_unavailable")
		return false
	}
	rt.Handle("GET", "/notifications/push/devices", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !enabled(w, r) {
			return
		}
		out := []pushDevice{}
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			if err := removeExpiredDevices(r.Context(), tx, user, time.Now().UnixMilli()); err != nil {
				return err
			}
			rows, err := tx.Query(r.Context(), "SELECT id, device_id, platform, expires_at, updated_at FROM push_devices WHERE user_id = $1 ORDER BY created_at, id", user)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var d pushDevice
				if err := rows.Scan(&d.ID, &d.DeviceID, &d.Platform, &d.ExpiresAt, &d.UpdatedAt); err != nil {
					return err
				}
				out = append(out, d)
			}
			return rows.Err()
		})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
	rt.Handle("POST", "/notifications/push/devices", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !enabled(w, r) {
			return
		}
		var req struct {
			DeviceID  *string `json:"deviceId"`
			Platform  *string `json:"platform"`
			Endpoint  *string `json:"endpoint"`
			P256dh    *string `json:"p256dh"`
			Auth      *string `json:"auth"`
			ExpiresAt *int64  `json:"expiresAt"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || req.DeviceID == nil || req.Endpoint == nil || req.P256dh == nil || req.Auth == nil {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid push device registration", "push_request_invalid")
			return
		}
		invalid := func(reason, code string) {
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Invalid push device registration: "+reason, code)
		}
		deviceID, endpoint, platform := strings.TrimSpace(*req.DeviceID), strings.TrimSpace(*req.Endpoint), "android"
		if req.Platform != nil {
			platform = strings.ToLower(strings.TrimSpace(*req.Platform))
		}
		now := time.Now().UnixMilli()
		switch {
		case !pushDeviceID.MatchString(deviceID):
			invalid("device_id", "push_device_id_invalid")
			return
		case platform != "android":
			httpx.Error(w, r, http.StatusUnprocessableEntity, "Push platform is not supported", "push_platform_unsupported")
			return
		}
		if _, _, err := pushKeys(*req.P256dh, *req.Auth); err != nil {
			invalid("encryption_keys", "push_encryption_keys_invalid")
			return
		}
		if e := req.ExpiresAt; e != nil && (*e <= now || *e > now+pushMaxExpiryMs) {
			invalid("expires_at", "push_expiry_invalid")
			return
		}
		if _, _, reason := validPushEndpoint(r.Context(), endpoint); reason != "" {
			invalid(reason, "push_endpoint_invalid")
			return
		}
		sum := sha256.Sum256([]byte(endpoint))
		hash := hex.EncodeToString(sum[:])
		var out pushDevice
		conflict := ""
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			if err := removeExpiredDevices(r.Context(), tx, user, now); err != nil {
				return err
			}
			var existing, holder string
			if err := tx.QueryRow(r.Context(), "SELECT id FROM push_devices WHERE user_id = $1 AND device_id = $2", user, deviceID).Scan(&existing); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := tx.QueryRow(r.Context(), "SELECT id FROM push_devices WHERE endpoint_hash = $1", hash).Scan(&holder); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if holder != "" && holder != existing {
				conflict = "endpoint"
				return nil
			}
			if existing == "" {
				var count int
				if err := tx.QueryRow(r.Context(), "SELECT count(*) FROM push_devices WHERE user_id = $1", user).Scan(&count); err != nil {
					return err
				}
				if count >= pushMaxDevices {
					conflict = "limit"
					return nil
				}
				existing = uuid.NewString()
				_, err := tx.Exec(r.Context(), `INSERT INTO push_devices (id, user_id, device_id, platform, endpoint, endpoint_hash, p256dh, auth_secret, expires_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`, existing, user, deviceID, platform, endpoint, hash, *req.P256dh, *req.Auth, req.ExpiresAt, now)
				if err != nil {
					return err
				}
			} else if _, err := tx.Exec(r.Context(), `UPDATE push_devices SET platform = $2, endpoint = $3, endpoint_hash = $4, p256dh = $5, auth_secret = $6, expires_at = $7,
updated_at = $8 WHERE id = $1`, existing, platform, endpoint, hash, *req.P256dh, *req.Auth, req.ExpiresAt, now); err != nil {
				return err
			}
			out = pushDevice{ID: existing, DeviceID: deviceID, Platform: platform, ExpiresAt: req.ExpiresAt, UpdatedAt: now}
			return nil
		})
		switch {
		case err != nil:
			s.fail(w, r, err)
		case conflict == "endpoint":
			httpx.Error(w, r, http.StatusConflict, "Push endpoint is already registered", "push_endpoint_conflict")
		case conflict == "limit":
			httpx.Error(w, r, http.StatusConflict, "Push device limit reached", "push_device_limit_reached")
		default:
			httpx.JSON(w, http.StatusCreated, out)
		}
	}))
	rt.Handle("POST", "/notifications/push/deliveries/acknowledgement", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			EventID    *string `json:"eventId"`
			DeviceID   *string `json:"deviceId"`
			Outcome    *string `json:"outcome"`
			ReasonCode *string `json:"reasonCode"`
		}
		bad := func() {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid push delivery acknowledgement", "push_request_invalid")
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req) != nil || req.EventID == nil || req.DeviceID == nil || req.Outcome == nil {
			bad()
			return
		}
		status, ok := pushOutcomes[*req.Outcome]
		if !ok || !pushEventID.MatchString(*req.EventID) || !pushDeviceID.MatchString(*req.DeviceID) || req.ReasonCode != nil && !pushReasonID.MatchString(*req.ReasonCode) {
			bad()
			return
		}
		var lastError *string
		if req.ReasonCode != nil {
			e := "client_" + *req.ReasonCode
			lastError = &e
		}
		result := ""
		err := pgx.BeginFunc(r.Context(), s.DB, func(tx pgx.Tx) error {
			var current string
			err := tx.QueryRow(r.Context(), "SELECT status FROM push_notification_deliveries WHERE event_id = $1 AND user_id = $2 AND device_id = $3",
				*req.EventID, user, *req.DeviceID).Scan(&current)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				result = "missing"
				return nil
			case err != nil:
				return err
			case current == status:
				result = "recorded"
				return nil
			}
			tag, err := tx.Exec(r.Context(), `UPDATE push_notification_deliveries SET status = $4, last_error = $5, updated_at = $6
WHERE event_id = $1 AND user_id = $2 AND device_id = $3 AND status = ANY($7)`, *req.EventID, user, *req.DeviceID, status, lastError, time.Now().UnixMilli(), pushRetryable)
			if err != nil {
				return err
			}
			result = "conflict"
			if tag.RowsAffected() == 1 {
				result = "recorded"
			}
			return nil
		})
		switch {
		case err != nil:
			s.fail(w, r, err)
		case result == "missing":
			slog.Warn("push_notifications", "event", "client_ack_rejected", "trace", (*req.EventID)[:16], "reason", "delivery_not_found")
			httpx.Error(w, r, http.StatusNotFound, "Push delivery not found", "push_delivery_not_found")
		case result == "conflict":
			slog.Warn("push_notifications", "event", "client_ack_rejected", "trace", (*req.EventID)[:16], "reason", "status_conflict")
			httpx.Error(w, r, http.StatusConflict, "Push delivery acknowledgement is invalid", "push_acknowledgement_conflict")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	rt.Handle("DELETE", "/notifications/push/devices/{deviceId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if !enabled(w, r) {
			return
		}
		id := r.PathValue("deviceId")
		if strings.TrimSpace(id) == "" {
			httpx.Error(w, r, http.StatusBadRequest, "Missing deviceId", "push_request_invalid")
			return
		}
		tag, err := s.DB.Exec(r.Context(), "DELETE FROM push_devices WHERE user_id = $1 AND device_id = $2", user, id)
		switch {
		case err != nil:
			s.fail(w, r, err)
		case tag.RowsAffected() == 0:
			httpx.Error(w, r, http.StatusNotFound, "Push device not found", "push_device_not_found")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
}
