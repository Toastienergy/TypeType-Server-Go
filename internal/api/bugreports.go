package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

type bugApiError struct {
	RequestID *string `json:"requestId"`
	Endpoint  string  `json:"endpoint"`
	Status    int     `json:"status"`
	Code      *string `json:"code"`
	Message   *string `json:"message"`
	Timestamp int64   `json:"timestamp"`
}

type bugCrash struct {
	Message   string  `json:"message"`
	Stack     *string `json:"stack"`
	Timestamp int64   `json:"timestamp"`
}

type bugContext struct {
	VideoURL         *string         `json:"videoUrl"`
	Route            string          `json:"route"`
	Timestamp        int64           `json:"timestamp"`
	UserAgent        string          `json:"userAgent"`
	BrowserLanguage  string          `json:"browserLanguage"`
	ViewportWidth    *int            `json:"viewportWidth"`
	ViewportHeight   *int            `json:"viewportHeight"`
	ScreenWidth      *int            `json:"screenWidth"`
	ScreenHeight     *int            `json:"screenHeight"`
	DevicePixelRatio *float64        `json:"devicePixelRatio"`
	Online           *bool           `json:"online"`
	Timezone         *string         `json:"timezone"`
	PlayerState      json.RawMessage `json:"playerState"`
	CrashLogs        []bugCrash      `json:"crashLogs"`
	APIErrors        []bugApiError   `json:"apiErrors"`
}

var (
	bugCategories = map[string]bool{"player": true, "audio_language": true, "subtitles": true, "ui": true, "functionality": true}
	bugStatuses   = map[string]bool{"new": true, "triaged": true, "in_progress": true, "fixed": true, "closed": true}
	bugNext       = map[string]string{"new": "triaged", "triaged": "in_progress", "in_progress": "fixed", "fixed": "closed"}
	repoRe        = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	urlTokenRe    = regexp.MustCompile(`https?://[^\s)]+`)
	hostTokenRe   = regexp.MustCompile(`\b(?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63}\b`)
)

func validateBugContext(c bugContext) string {
	switch {
	case strings.TrimSpace(c.Route) == "":
		return "Context route is required"
	case strings.TrimSpace(c.UserAgent) == "":
		return "Context userAgent is required"
	case strings.TrimSpace(c.BrowserLanguage) == "":
		return "Context browserLanguage is required"
	case c.Timestamp <= 0:
		return "Context timestamp is invalid"
	case len(c.CrashLogs) > 200:
		return "Too many crash logs"
	case len(c.APIErrors) > 100:
		return "Too many api errors"
	}
	for _, e := range c.APIErrors {
		if strings.TrimSpace(e.Endpoint) == "" || e.Timestamp <= 0 || e.Status <= 0 {
			return "Invalid api error entry"
		}
	}
	for _, d := range []*int{c.ViewportWidth, c.ViewportHeight, c.ScreenWidth, c.ScreenHeight} {
		if d != nil && (*d < 1 || *d > 100000) {
			return "Invalid display dimensions"
		}
	}
	if c.DevicePixelRatio != nil && (*c.DevicePixelRatio < 0.1 || *c.DevicePixelRatio > 100) {
		return "Invalid device pixel ratio"
	}
	if c.Timezone != nil && len([]rune(*c.Timezone)) > 128 {
		return "Invalid timezone"
	}
	return ""
}

// redactDomains keeps an issue free of instance hosts: URLs lose their host and bare host names become a placeholder.
func redactDomains(v string) string {
	t := strings.TrimSpace(v)
	if t == "" {
		return "n/a"
	}
	strip := func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return raw
		}
		p := u.EscapedPath()
		if p == "" {
			p = "/"
		}
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			p += "#" + u.EscapedFragment()
		}
		return p
	}
	if u, err := url.Parse(t); err == nil && u.Host != "" {
		t = strip(t)
	} else {
		t = urlTokenRe.ReplaceAllStringFunc(t, strip)
	}
	return hostTokenRe.ReplaceAllStringFunc(t, func(m string) string { return "redacted-host" })
}

func (s *Server) modOnly(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		role, _ := s.Auth.Role(r.Context(), user)
		if role == nil || *role != "admin" && *role != "moderator" {
			plain(w, r, http.StatusForbidden, "Admin or moderator access required")
			return
		}
		next(w, r, user)
	})
}

type bugRow struct {
	ID, Category, Description, Status, UserID, UserEmail string
	Context                                              string
	GithubIssueURL                                       *string
	CreatedAt, UpdatedAt                                 int64
}

func (b bugRow) item(withContext bool) map[string]any {
	m := map[string]any{"id": b.ID, "category": b.Category, "description": b.Description, "status": b.Status, "userId": b.UserID,
		"userEmail": b.UserEmail, "githubIssueUrl": b.GithubIssueURL, "createdAt": b.CreatedAt, "updatedAt": b.UpdatedAt}
	if withContext {
		m["context"] = json.RawMessage(b.Context)
	}
	return m
}

const bugColumns = `b.id, b.category, b.description, b.status, b.user_id, COALESCE(u.email, ''), b.context, b.github_issue_url, b.created_at,
b.updated_at FROM bug_reports b LEFT JOIN users u ON u.id = b.user_id`

func (s *Server) bugRoutes(rt *Router) {
	rt.Handle("POST", "/bug-reports", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		if strings.HasPrefix(user, "guest:") {
			plain(w, r, http.StatusForbidden, "Guest users cannot create bug reports")
			return
		}
		var req struct {
			Category    *string     `json:"category"`
			Description *string     `json:"description"`
			Context     *bugContext `json:"context"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if json.Unmarshal(body, &req) != nil || req.Category == nil || req.Description == nil || req.Context == nil {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		desc := strings.TrimSpace(*req.Description)
		msg := ""
		switch {
		case !bugCategories[*req.Category]:
			msg = "Invalid category"
		case desc == "":
			msg = "Description is required"
		case len([]rune(desc)) > 10000:
			msg = "Description is too long"
		default:
			msg = validateBugContext(*req.Context)
		}
		if msg != "" {
			plain(w, r, http.StatusBadRequest, msg)
			return
		}
		if req.Context.CrashLogs == nil {
			req.Context.CrashLogs = []bugCrash{}
		}
		if req.Context.APIErrors == nil {
			req.Context.APIErrors = []bugApiError{}
		}
		ctxJSON, _ := json.Marshal(req.Context)
		id, now := auth.UUID(), time.Now().UnixMilli()
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO bug_reports (id, user_id, category, description, context, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'new', $6, $6)`, id, user, *req.Category, desc, string(ctxJSON), now); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": id, "status": "new", "createdAt": now})
	}))
	rt.Handle("GET", "/admin/bug-reports", s.modOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		q := r.URL.Query()
		page, limit := 1, 20
		if n, err := strconv.Atoi(q.Get("page")); err == nil {
			page = n
		}
		if n, err := strconv.Atoi(q.Get("limit")); err == nil {
			limit = n
		}
		if page < 1 {
			plain(w, r, http.StatusBadRequest, "Invalid page")
			return
		}
		if limit < 1 || limit > 100 {
			plain(w, r, http.StatusBadRequest, "Invalid limit")
			return
		}
		where, args := []string{"TRUE"}, []any{}
		if q.Has("status") {
			if !bugStatuses[q.Get("status")] {
				plain(w, r, http.StatusBadRequest, "Invalid status")
				return
			}
			args = append(args, q.Get("status"))
			where = append(where, fmt.Sprintf("b.status = $%d", len(args)))
		}
		if q.Has("category") {
			if !bugCategories[q.Get("category")] {
				plain(w, r, http.StatusBadRequest, "Invalid category")
				return
			}
			args = append(args, q.Get("category"))
			where = append(where, fmt.Sprintf("b.category = $%d", len(args)))
		}
		cond := strings.Join(where, " AND ")
		var total int64
		s.DB.QueryRow(r.Context(), "SELECT count(*) FROM bug_reports b WHERE "+cond, args...).Scan(&total)
		args = append(args, limit, int64(page-1)*int64(limit))
		rows, err := s.DB.Query(r.Context(), fmt.Sprintf("SELECT %s WHERE %s ORDER BY b.created_at DESC LIMIT $%d OFFSET $%d", bugColumns, cond, len(args)-1, len(args)), args...)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items := []map[string]any{}
		for rows.Next() {
			var b bugRow
			if rows.Scan(&b.ID, &b.Category, &b.Description, &b.Status, &b.UserID, &b.UserEmail, &b.Context, &b.GithubIssueURL, &b.CreatedAt, &b.UpdatedAt) == nil {
				items = append(items, b.item(false))
			}
		}
		rows.Close()
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "page": page, "limit": limit, "total": total})
	}))
	load := func(r *http.Request) (*bugRow, error) {
		var b bugRow
		err := s.DB.QueryRow(r.Context(), "SELECT "+bugColumns+" WHERE b.id = $1", r.PathValue("id")).Scan(&b.ID, &b.Category, &b.Description, &b.Status,
			&b.UserID, &b.UserEmail, &b.Context, &b.GithubIssueURL, &b.CreatedAt, &b.UpdatedAt)
		return &b, err
	}
	rt.Handle("GET", "/admin/bug-reports/{id}", s.modOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		b, err := load(r)
		if err != nil {
			plain(w, r, http.StatusNotFound, "Bug report not found")
			return
		}
		httpx.JSON(w, http.StatusOK, b.item(true))
	}))
	rt.Handle("PUT", "/admin/bug-reports/{id}/status", s.modOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		var req struct {
			Status *string `json:"status"`
		}
		if !decodeOr(w, r, &req, "Invalid request body") {
			return
		}
		if req.Status == nil || !bugStatuses[*req.Status] {
			plain(w, r, http.StatusBadRequest, "Invalid status")
			return
		}
		b, err := load(r)
		if err != nil {
			plain(w, r, http.StatusNotFound, "Bug report not found")
			return
		}
		if *req.Status != b.Status && bugNext[b.Status] != *req.Status {
			plain(w, r, http.StatusBadRequest, "Invalid status transition: "+b.Status+" -> "+*req.Status)
			return
		}
		now := time.Now().UnixMilli()
		s.DB.Exec(r.Context(), "UPDATE bug_reports SET status = $2, updated_at = $3 WHERE id = $1", b.ID, *req.Status, now)
		httpx.JSON(w, http.StatusOK, map[string]any{"id": b.ID, "status": *req.Status, "updatedAt": now})
	}))
	rt.Handle("POST", "/admin/bug-reports/{id}/github-issue", s.adminOnly(func(w http.ResponseWriter, r *http.Request, _ string) {
		b, err := load(r)
		if err != nil {
			plain(w, r, http.StatusNotFound, "Bug report not found")
			return
		}
		if b.GithubIssueURL != nil {
			httpx.JSON(w, http.StatusConflict, map[string]any{"error": "GitHub issue already exists", "githubIssueUrl": *b.GithubIssueURL})
			return
		}
		repo := strings.TrimSpace(os.Getenv("GITHUB_REPO"))
		if repo == "" {
			repo = "TypeType-Video/TypeType"
		}
		if !repoRe.MatchString(repo) {
			plain(w, r, http.StatusUnprocessableEntity, "GitHub repository is invalid")
			return
		}
		issue := issueURL(repo, b)
		now := time.Now().UnixMilli()
		tag, _ := s.DB.Exec(r.Context(), "UPDATE bug_reports SET github_issue_url = $2, updated_at = $3 WHERE id = $1 AND github_issue_url IS NULL", b.ID, issue, now)
		if tag.RowsAffected() == 0 {
			plain(w, r, http.StatusNotFound, "Bug report not found")
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"id": b.ID, "githubIssueUrl": issue, "updatedAt": now})
	}))
}

// issueURL is GitHubIssueService: a prefilled "new issue" link, never an API call, with instance hosts redacted.
func issueURL(repo string, b *bugRow) string {
	var c bugContext
	json.Unmarshal([]byte(b.Context), &c)
	title := []rune(redactDomains(b.Description))
	if len(title) > 80 {
		title = title[:80]
	}
	var sb strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&sb, f+"\n", a...) }
	video := "n/a"
	if c.VideoURL != nil {
		video = *c.VideoURL
	}
	line("## Bug report")
	line("%s", redactDomains(b.Description))
	line("")
	line("## Context")
	line("- User id: %s", b.UserID)
	line("- Route: %s", redactDomains(c.Route))
	line("- Video: %s", redactDomains(video))
	line("- Browser language: %s", c.BrowserLanguage)
	line("- User agent: %s", redactDomains(c.UserAgent))
	line("- Timestamp: %d", c.Timestamp)
	line("- Crash logs: %d", len(c.CrashLogs))
	line("- API errors: %d", len(c.APIErrors))
	opt := func(p *string) string {
		if p == nil {
			return "n/a"
		}
		return *p
	}
	if len(c.APIErrors) > 0 {
		e := c.APIErrors[0]
		line("- Latest API endpoint: %s", redactDomains(e.Endpoint))
		line("- Latest API status: %d", e.Status)
		line("- Latest API requestId: %s", redactDomains(opt(e.RequestID)))
		line("- Latest API code: %s", opt(e.Code))
	}
	line("")
	line("## Player state")
	ps := string(c.PlayerState)
	if ps == "" || ps == "null" {
		ps = "{}"
	}
	line("%s", ps)
	if len(c.APIErrors) > 0 {
		line("")
		line("## API errors")
		for i, e := range c.APIErrors {
			if i == 10 {
				break
			}
			line("- endpoint=%s status=%d requestId=%s code=%s timestamp=%d", redactDomains(e.Endpoint), e.Status, redactDomains(opt(e.RequestID)), opt(e.Code), e.Timestamp)
			if e.Message != nil && strings.TrimSpace(*e.Message) != "" {
				line("  message=%s", redactDomains(*e.Message))
			}
		}
	}
	params := []string{"title=" + url.QueryEscape("[Bug]["+b.Category+"] "+string(title)), "body=" + url.QueryEscape(sb.String()), "labels=bug"}
	if t := strings.TrimSpace(os.Getenv("GITHUB_ISSUE_TEMPLATE")); t != "" {
		params = append(params, "template="+url.QueryEscape(t))
	}
	return "https://github.com/" + repo + "/issues/new?" + strings.Join(params, "&")
}
