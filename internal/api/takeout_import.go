package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/portability"
)

// The /imports/youtube-takeout job API (YoutubeTakeoutImportJobService): jobs live in youtube_takeout_import_jobs
// for a day; the preview is parsed on first request and kept as JSON; the commit runs in the background.

const takeoutUploadLimit = 2 << 30

type takeoutStatus struct {
	JobID     string  `json:"jobId"`
	Status    string  `json:"status"`
	Phase     string  `json:"phase"`
	Progress  int     `json:"progress"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	ExpiresAt int64   `json:"expiresAt"`
	Error     *string `json:"error"`
}

type takeoutCounts struct {
	Subscriptions int `json:"subscriptions"`
	Playlists     int `json:"playlists"`
	PlaylistItems int `json:"playlistItems"`
	Favorites     int `json:"favorites"`
	WatchLater    int `json:"watchLater"`
	History       int `json:"history"`
}

type takeoutIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Stage    string `json:"stage"`
	Message  string `json:"message"`
	Count    int    `json:"count"`
}

type takeoutIssueSummary struct {
	Total    int            `json:"total"`
	Warnings int            `json:"warnings"`
	Errors   int            `json:"errors"`
	ByCode   map[string]int `json:"byCode"`
}

type takeoutStats struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
}

// takeoutIssues is YoutubeTakeoutIssueService.build.
func takeoutIssues(warnings, errs []string, stage string) ([]takeoutIssue, takeoutIssueSummary) {
	type key struct{ sev, msg string }
	var order []key
	counts := map[key]int{}
	add := func(sev string, list []string) {
		for _, m := range list {
			k := key{sev, strings.TrimSpace(m)}
			if counts[k] == 0 {
				order = append(order, k)
			}
			counts[k]++
		}
	}
	add("warn", warnings)
	add("error", errs)
	items := []takeoutIssue{}
	sum := takeoutIssueSummary{ByCode: map[string]int{}}
	for _, k := range order {
		code := "error_generic"
		if k.sev == "warn" {
			code = "warning_generic"
		}
		switch {
		case strings.HasPrefix(k.msg, "Unsupported CSV schema"):
			code = "unsupported_csv_schema"
		case k.msg == "Invalid subscription row":
			code = "invalid_subscription_row"
		case k.msg == "Invalid playlist row":
			code = "invalid_playlist_row"
		case k.msg == "Invalid playlist item row":
			code = "invalid_playlist_item_row"
		case k.msg == "No subscription rows detected":
			code = "no_subscription_rows"
		case k.msg == "No watch history rows detected":
			code = "no_watch_history_rows"
		}
		sev := "error"
		if k.sev == "warn" {
			sev = "warning"
			sum.Warnings += counts[k]
		} else {
			sum.Errors += counts[k]
		}
		items = append(items, takeoutIssue{code, sev, stage, k.msg, counts[k]})
		sum.ByCode[code] += counts[k]
	}
	sum.Total = sum.Warnings + sum.Errors
	return items, sum
}

var takeoutParsed sync.Map

func (s *Server) takeoutStatus(ctx context.Context, user, id string) (*takeoutStatus, error) {
	var st takeoutStatus
	err := s.DB.QueryRow(ctx, `SELECT id, status, phase, progress, created_at, updated_at, expires_at, error FROM youtube_takeout_import_jobs
WHERE id = $1 AND user_id = $2`, id, user).Scan(&st.JobID, &st.Status, &st.Phase, &st.Progress, &st.CreatedAt, &st.UpdatedAt, &st.ExpiresAt, &st.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &st, err
}

func (s *Server) setTakeoutStatus(ctx context.Context, id, status, phase string, progress int) {
	s.DB.Exec(ctx, "UPDATE youtube_takeout_import_jobs SET status = $2, phase = $3, progress = $4, error = NULL, updated_at = $5 WHERE id = $1",
		id, status, phase, min(max(progress, 0), 100), time.Now().UnixMilli())
}

func (s *Server) failTakeout(ctx context.Context, id, phase, msg string) {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	s.DB.Exec(ctx, "UPDATE youtube_takeout_import_jobs SET status = 'failed', phase = $2, progress = 100, error = $3, updated_at = $4 WHERE id = $1",
		id, phase, msg, time.Now().UnixMilli())
}

func (s *Server) purgeTakeoutJobs(ctx context.Context) {
	now := time.Now().UnixMilli()
	rows, err := s.DB.Query(ctx, "SELECT archive_path FROM youtube_takeout_import_jobs WHERE expires_at <= $1", now)
	if err != nil {
		return
	}
	for rows.Next() {
		var p *string
		if rows.Scan(&p) == nil && p != nil && *p != "" {
			os.Remove(*p)
		}
	}
	rows.Close()
	s.DB.Exec(ctx, "DELETE FROM youtube_takeout_import_jobs WHERE expires_at <= $1", now)
}

func (s *Server) takeoutRoutes(rt *Router) {
	rt.Handle("POST", "/imports/youtube-takeout", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		s.purgeTakeoutJobs(r.Context())
		mr, err := r.MultipartReader()
		if err != nil {
			plain(w, r, http.StatusBadRequest, "Missing archive part")
			return
		}
		path := ""
		fail := func(status int, msg string) {
			if path != "" {
				os.Remove(path)
			}
			plain(w, r, status, msg)
		}
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				fail(http.StatusBadRequest, "Invalid takeout archive")
				return
			}
			if p.FormName() == "archive" && p.FileName() != "" {
				if !strings.HasSuffix(strings.ToLower(p.FileName()), ".zip") {
					p.Close()
					fail(http.StatusBadRequest, "Invalid archive file type")
					return
				}
				f, err := os.CreateTemp("", "youtube-takeout-*.zip")
				if err != nil {
					fail(http.StatusInternalServerError, "Internal server error")
					return
				}
				if path != "" {
					os.Remove(path)
				}
				path = f.Name()
				n, err := io.Copy(f, io.LimitReader(p, takeoutUploadLimit+1))
				f.Close()
				if err != nil {
					fail(http.StatusBadRequest, "Invalid takeout archive")
					return
				}
				if n > takeoutUploadLimit {
					fail(http.StatusRequestEntityTooLarge, "Archive too large")
					return
				}
			}
			p.Close()
		}
		if path == "" {
			plain(w, r, http.StatusBadRequest, "Missing archive part")
			return
		}
		id, now := auth.UUID(), time.Now().UnixMilli()
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO youtube_takeout_import_jobs (id, user_id, status, phase, progress, parse_completed,
import_started, import_completed, archive_path, created_at, updated_at, expires_at) VALUES ($1, $2, 'pending', 'pending', 0, FALSE, FALSE, FALSE, $3, $4, $4, $5)`,
			id, user, path, now, now+24*3600*1000); err != nil {
			os.Remove(path)
			s.fail(w, r, err)
			return
		}
		st, err := s.takeoutStatus(r.Context(), user, id)
		if err != nil || st == nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, st)
	}))
	rt.Handle("GET", "/imports/youtube-takeout/{jobId}", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		st, err := s.takeoutStatus(r.Context(), user, r.PathValue("jobId"))
		switch {
		case err != nil:
			s.fail(w, r, err)
		case st == nil:
			plain(w, r, http.StatusNotFound, "Import job not found")
		default:
			httpx.JSON(w, http.StatusOK, st)
		}
	}))
	rt.Handle("GET", "/imports/youtube-takeout/{jobId}/preview", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		preview, err := s.takeoutPreview(r.Context(), user, r.PathValue("jobId"))
		if err != nil {
			plain(w, r, http.StatusBadRequest, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(preview)
	}))
	rt.Handle("POST", "/imports/youtube-takeout/{jobId}/commit", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		plan := takeoutPlan{true, true, true, true, true, true}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		json.Unmarshal(body, &plan)
		st, err := s.takeoutCommit(r.Context(), user, r.PathValue("jobId"), plan)
		if err != nil {
			plain(w, r, http.StatusBadRequest, err.Error())
			return
		}
		httpx.JSON(w, http.StatusAccepted, st)
	}))
	rt.Handle("GET", "/imports/youtube-takeout/{jobId}/report", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		var report *string
		err := s.DB.QueryRow(r.Context(), "SELECT report_json FROM youtube_takeout_import_jobs WHERE id = $1 AND user_id = $2",
			r.PathValue("jobId"), user).Scan(&report)
		if err != nil || report == nil {
			plain(w, r, http.StatusNotFound, "Report not available")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(*report))
	}))
}

func (s *Server) takeoutArchive(ctx context.Context, user, id string) (string, error) {
	var p *string
	err := s.DB.QueryRow(ctx, "SELECT archive_path FROM youtube_takeout_import_jobs WHERE id = $1 AND user_id = $2", id, user).Scan(&p)
	if err != nil || p == nil {
		return "", errors.New("Import job not found")
	}
	return *p, nil
}

func (s *Server) takeoutParse(ctx context.Context, user, id string) (*portability.LegacyParsed, error) {
	if v, ok := takeoutParsed.Load(id); ok {
		return v.(*portability.LegacyParsed), nil
	}
	path, err := s.takeoutArchive(ctx, user, id)
	if err != nil {
		return nil, err
	}
	parsed, err := portability.ParseLegacyTakeout(path)
	if err != nil {
		return nil, err
	}
	takeoutParsed.Store(id, parsed)
	return parsed, nil
}

// takeoutPreview is YoutubeTakeoutImportJobService.preview with YoutubeTakeoutPreviewService.build.
func (s *Server) takeoutPreview(ctx context.Context, user, id string) ([]byte, error) {
	var stored *string
	if err := s.DB.QueryRow(ctx, "SELECT preview_json FROM youtube_takeout_import_jobs WHERE id = $1 AND user_id = $2", id, user).Scan(&stored); err == nil && stored != nil {
		return []byte(*stored), nil
	}
	s.setTakeoutStatus(ctx, id, "running", "parsing", 10)
	parsed, err := s.takeoutParse(ctx, user, id)
	if err != nil {
		s.failTakeout(ctx, id, "parsing_failed", "IllegalArgumentException: "+err.Error())
		return nil, err
	}
	preview, err := s.buildTakeoutPreview(ctx, user, parsed)
	if err != nil {
		s.failTakeout(ctx, id, "parsing_failed", "Exception: "+err.Error())
		return nil, err
	}
	s.DB.Exec(ctx, "UPDATE youtube_takeout_import_jobs SET preview_json = $2, parse_completed = TRUE, updated_at = $3 WHERE id = $1", id, string(preview), time.Now().UnixMilli())
	s.setTakeoutStatus(ctx, id, "completed", "preview_ready", 100)
	return preview, nil
}

func (s *Server) stringSet(ctx context.Context, q string, args ...any) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			out[v] = true
		}
	}
	return out, rows.Err()
}

func take[T any](list []T, n int) []T {
	if list == nil {
		return []T{}
	}
	return list[:min(n, len(list))]
}

func (s *Server) buildTakeoutPreview(ctx context.Context, user string, p *portability.LegacyParsed) ([]byte, error) {
	issues, summary := takeoutIssues(p.Warnings, p.Errors, "preview")
	subs, err := s.stringSet(ctx, "SELECT channel_url FROM subscriptions WHERE user_id = $1", user)
	if err != nil {
		return nil, err
	}
	lists, _ := s.stringSet(ctx, "SELECT lower(name) FROM playlists WHERE user_id = $1", user)
	listVideos, _ := s.stringSet(ctx, "SELECT url FROM playlist_videos WHERE user_id = $1", user)
	history, _ := s.stringSet(ctx, "SELECT url || '|' || watched_at FROM history WHERE user_id = $1", user)
	favs, _ := s.stringSet(ctx, "SELECT video_url FROM favorites WHERE user_id = $1", user)
	later, _ := s.stringSet(ctx, "SELECT url FROM watch_later WHERE user_id = $1", user)
	var items []portability.LegacyVideo
	for _, k := range p.ItemKeys {
		items = append(items, p.Items[k]...)
	}
	count := func(n int, f func(int) bool) int {
		c := 0
		for i := 0; i < n; i++ {
			if f(i) {
				c++
			}
		}
		return c
	}
	favURLs := []string{}
	for _, f := range p.Favorites {
		favURLs = append(favURLs, f.VideoURL)
	}
	out := map[string]any{
		"counts": takeoutCounts{len(p.Subscriptions), len(p.Playlists), len(items), len(p.Favorites), len(p.WatchLater), len(p.History)},
		"dedup": takeoutCounts{
			count(len(p.Subscriptions), func(i int) bool { return subs[p.Subscriptions[i].ChannelURL] }),
			count(len(p.Playlists), func(i int) bool { return lists[strings.ToLower(p.Playlists[i].Name)] }),
			count(len(items), func(i int) bool { return listVideos[items[i].URL] }),
			count(len(p.Favorites), func(i int) bool { return favs[p.Favorites[i].VideoURL] }),
			count(len(p.WatchLater), func(i int) bool { return later[p.WatchLater[i].URL] }),
			count(len(p.History), func(i int) bool { return history[p.History[i].URL+"|"+strconv.FormatInt(p.History[i].WatchedAt, 10)] }),
		},
		"samples": map[string]any{"subscriptions": take(p.Subscriptions, 5), "playlists": take(p.Playlists, 5), "playlistItems": take(items, 5),
			"favorites": take(favURLs, 5), "watchLater": take(p.WatchLater, 5), "history": take(p.History, 5)},
		"warnings": p.Warnings, "errors": p.Errors, "issues": issues, "issueSummary": summary,
	}
	return json.Marshal(out)
}
