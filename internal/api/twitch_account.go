package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"typetype.dev/server/internal/auth"
	"typetype.dev/server/internal/httpx"
)

func (s *Server) twitchSignedIn(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.twitchOn(w, r) {
			return
		}
		user, ok := s.userID(w, r)
		if !ok {
			return
		}
		if strings.HasPrefix(user, "guest:") {
			httpx.Error(w, r, http.StatusForbidden, "Sign in to link a Twitch account", "account_required")
			return
		}
		next(w, r, user)
	}
}

func (s *Server) linked(w http.ResponseWriter, r *http.Request, res liveResponse, ok bool) {
	if !ok {
		httpx.Error(w, r, http.StatusConflict, "No Twitch account linked", "twitch_not_linked")
		return
	}
	writeLive(w, res)
}

func (s *Server) twitchAccountRoutes(rt *Router) {
	rt.Handle("GET", "/twitch/account", s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
		a, err := s.twitchAccount(r.Context(), user)
		if err != nil {
			httpx.JSON(w, http.StatusOK, map[string]any{"linked": false, "login": nil, "displayName": nil, "avatarUrl": nil})
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"linked": true, "login": a.Login, "displayName": a.DisplayName, "avatarUrl": a.AvatarURL})
	}))
	rt.Handle("POST", "/twitch/account/device", s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, _ string) {
		writeLive(w, s.twitch.do(r.Context(), "POST", "/v1/auth/device", nil, []byte("{}"), ""))
	}))
	rt.Handle("POST", "/twitch/account/poll", s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			DeviceCode string `json:"deviceCode"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
		if json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.DeviceCode) == "" || len(req.DeviceCode) > 200 {
			plain(w, r, http.StatusBadRequest, "Invalid request body")
			return
		}
		b, _ := json.Marshal(map[string]string{"deviceCode": req.DeviceCode})
		res := s.twitch.do(r.Context(), "POST", "/v1/auth/poll", nil, b, "")
		failed := func(status int) { httpx.Error(w, r, status, "Twitch login failed", "twitch_login_failed") }
		if res.status != http.StatusOK {
			failed(res.status)
			return
		}
		var j map[string]any
		if json.Unmarshal(res.body, &j) != nil {
			failed(http.StatusBadGateway)
			return
		}
		status, _ := j["status"].(string)
		if status == "" {
			failed(http.StatusBadGateway)
			return
		}
		if status != "ok" {
			httpx.JSON(w, http.StatusOK, map[string]any{"status": status, "account": nil})
			return
		}
		tok, _ := j["token"].(map[string]any)
		u, _ := j["user"].(map[string]any)
		access, _ := tok["accessToken"].(string)
		if tok == nil || u == nil || access == "" {
			failed(http.StatusBadGateway)
			return
		}
		a := &twitchAccount{AccessToken: access, ExpiresAt: expiryFrom(tok)}
		if rt, ok := tok["refreshToken"].(string); ok {
			a.RefreshToken = &rt
		}
		a.Login, _ = u["login"].(string)
		a.DisplayName, _ = u["displayName"].(string)
		a.AvatarURL, _ = u["avatarUrl"].(string)
		if err := s.saveTwitchAccount(r.Context(), user, a); err != nil {
			s.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"status": "ok", "account": map[string]any{"linked": true, "login": a.Login,
			"displayName": a.DisplayName, "avatarUrl": a.AvatarURL}})
	}))
	rt.Handle("DELETE", "/twitch/account", s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
		if a, err := s.twitchAccount(r.Context(), user); err == nil {
			b, _ := json.Marshal(map[string]string{"token": a.AccessToken})
			s.twitch.do(r.Context(), "POST", "/v1/auth/revoke", nil, b, "")
			s.DB.Exec(r.Context(), "DELETE FROM twitch_accounts WHERE user_id = $1", user)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, p := range []string{"live", "videos", "follows"} {
		path := "/v1/me/" + p
		rt.Handle("GET", "/twitch/me/"+p, s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
			q := pickQuery(r, "first", "after")
			res, ok := s.withTwitch(r.Context(), user, func(t string) liveResponse { return s.twitch.do(r.Context(), "GET", path, q, nil, t) })
			s.linked(w, r, res, ok)
		}))
	}
	loginRoute := func(method, route, upstream, upMethod string) {
		rt.Handle(method, route, func(w http.ResponseWriter, r *http.Request) {
			login, ok := pathOK(w, r, "login", twitchLoginRe)
			if !ok {
				return
			}
			s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
				p := strings.ReplaceAll(upstream, "{}", login)
				res, ok := s.withTwitch(r.Context(), user, func(t string) liveResponse { return s.twitch.do(r.Context(), upMethod, p, nil, nil, t) })
				s.linked(w, r, res, ok)
			})(w, r)
		})
	}
	loginRoute("GET", "/twitch/me/follows/{login}", "/v1/me/follows/{}", "GET")
	loginRoute("PUT", "/twitch/me/follows/{login}", "/v1/me/follows/{}", "PUT")
	loginRoute("DELETE", "/twitch/me/follows/{login}", "/v1/me/follows/{}", "DELETE")
	loginRoute("GET", "/twitch/me/emotes/{login}", "/v1/me/emotes/{}", "GET")
	rt.Handle("POST", "/twitch/chat/{login}/messages", func(w http.ResponseWriter, r *http.Request) {
		login, ok := pathOK(w, r, "login", twitchLoginRe)
		if !ok {
			return
		}
		s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
			var req struct {
				Message string `json:"message"`
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			json.Unmarshal(body, &req)
			msg := strings.TrimSpace(req.Message)
			if msg == "" || auth.JavaLength(msg) > 500 {
				plain(w, r, http.StatusBadRequest, "Invalid message")
				return
			}
			b, _ := json.Marshal(map[string]string{"message": msg})
			res, ok := s.withTwitch(r.Context(), user, func(t string) liveResponse {
				return s.twitch.do(r.Context(), "POST", "/v1/chat/"+login+"/messages", nil, b, t)
			})
			s.linked(w, r, res, ok)
		})(w, r)
	})
	rt.Handle("POST", "/twitch/polls/vote", s.twitchSignedIn(func(w http.ResponseWriter, r *http.Request, user string) {
		var req struct {
			PollID   string `json:"pollId"`
			ChoiceID string `json:"choiceId"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<14))
		if json.Unmarshal(body, &req) != nil || !pollIDRe.MatchString(req.PollID) || !pollIDRe.MatchString(req.ChoiceID) {
			plain(w, r, http.StatusBadRequest, "Invalid vote")
			return
		}
		b, _ := json.Marshal(map[string]string{"pollId": req.PollID, "choiceId": req.ChoiceID})
		res, ok := s.withTwitch(r.Context(), user, func(t string) liveResponse { return s.twitch.do(r.Context(), "POST", "/v1/polls/vote", nil, b, t) })
		s.linked(w, r, res, ok)
	}))
	rt.Handle("GET", "/twitch/chat/{login}/events", func(w http.ResponseWriter, r *http.Request) {
		login, ok := pathOK(w, r, "login", twitchLoginRe)
		if !ok || !s.twitchOn(w, r) {
			return
		}
		if _, ok := s.publicAccess(w, r); !ok {
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), "GET", s.twitch.base+"/v1/chat/"+login+"/events", nil)
		req.Header.Set("X-Live-Secret", s.twitch.secret)
		res, err := s.twitch.streams.Do(req)
		if err != nil || res.StatusCode/100 != 2 {
			if res != nil {
				res.Body.Close()
			}
			plain(w, r, http.StatusBadGateway, "Chat unavailable")
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 8<<10)
		for {
			n, err := res.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	})
}
