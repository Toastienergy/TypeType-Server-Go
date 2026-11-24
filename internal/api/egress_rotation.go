package api

import (
	"context"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// egressRotation is GluetunEgressRotation: YouTube rates each exit IP, so once it refuses the current one the egress
// moves to another VPN server through the gluetun control server, at most once per ten minutes since every viewer
// shares the exit. A new exit stays only when a token session minted on it is accepted; with named servers the
// rotation skips those refused within the hour and goes back to the last accepted one when none passes.
type egressRotation struct {
	s         *Server
	url, key  string
	servers   []string
	mu        sync.Mutex
	rotatedAt time.Time
	refused   map[string]time.Time
	unknown   map[string]bool
	current   string
	accepted  string
	accept    func(ctx context.Context, videoID string) bool
	forget    func()
	settle    time.Duration
}

func newEgressRotation(s *Server) *egressRotation {
	u, k := secretConfig("YOUTUBE_EGRESS_GLUETUN_URL"), secretConfig("YOUTUBE_EGRESS_GLUETUN_API_KEY")
	if u == "" || k == "" {
		return nil
	}
	var servers []string
	for _, n := range strings.Split(os.Getenv("YOUTUBE_EGRESS_GLUETUN_SERVERS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			servers = append(servers, n)
		}
	}
	e := &egressRotation{s: s, url: strings.TrimRight(u, "/"), key: k, servers: servers, refused: map[string]time.Time{}, unknown: map[string]bool{},
		settle: 3 * time.Second}
	e.accept = func(ctx context.Context, videoID string) bool { return s.acceptance.accepts(ctx, videoID) }
	e.forget = func() { s.Sabr.Token().Forget() }
	return e
}

var gluetunHTTP = &http.Client{Timeout: 10 * time.Second}

func (e *egressRotation) put(ctx context.Context, path, body string) bool {
	req, _ := http.NewRequestWithContext(ctx, "PUT", e.url+path, strings.NewReader(body))
	req.Header.Set("X-API-Key", e.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := gluetunHTTP.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode/100 == 2
}

// move connects to a named server, or reconnects at random with an empty name, then lets the tunnel settle.
func (e *egressRotation) move(ctx context.Context, server string) bool {
	var ok bool
	if server == "" {
		ok = e.put(ctx, "/v1/vpn/status", `{"status":"stopped"}`) && e.put(ctx, "/v1/vpn/status", `{"status":"running"}`)
	} else {
		ok = e.put(ctx, "/v1/vpn/settings", `{"provider":{"server_selection":{"names":["`+strings.ReplaceAll(server, `"`, "")+`"],"countries":[],"cities":[]}}}`)
	}
	if ok {
		time.Sleep(e.settle)
		e.forget()
	}
	return ok
}

func (e *egressRotation) candidate() string {
	var known, others, fresh []string
	for _, n := range e.servers {
		if !e.unknown[n] {
			known = append(known, n)
		}
	}
	if len(known) == 0 {
		return ""
	}
	for _, n := range known {
		if n != e.current {
			others = append(others, n)
		}
	}
	if len(others) == 0 {
		others = known
	}
	for _, n := range others {
		if time.Since(e.refused[n]) >= time.Hour {
			fresh = append(fresh, n)
		}
	}
	if len(fresh) > 0 {
		return fresh[rand.IntN(len(fresh))]
	}
	best := others[0]
	for _, n := range others {
		if e.refused[n].Before(e.refused[best]) {
			best = n
		}
	}
	return best
}

func (e *egressRotation) rotate(ctx context.Context, reason, videoID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.rotatedAt) < 10*time.Minute {
		return false
	}
	e.rotatedAt = time.Now()
	if e.current != "" {
		e.refused[e.current] = time.Now()
	}
	for attempt := 1; attempt <= 3; attempt++ {
		server, moved := "", false
		for range e.servers {
			if server = e.candidate(); server == "" {
				break
			}
			if moved = e.move(ctx, server); moved {
				break
			}
			e.s.Log.Warn("youtube_egress unknown server", "server", server)
			e.unknown[server] = true
		}
		if !moved {
			if server = ""; !e.move(ctx, "") {
				e.s.Log.Warn("youtube_egress rotation failed", "reason", reason)
				return false
			}
		}
		e.current = server
		ok := e.accept(ctx, videoID)
		e.s.Log.Warn("youtube_egress rotated", "reason", reason, "attempt", attempt, "server", server, "accepted", ok)
		if ok {
			e.accepted = server
			return true
		}
		if server != "" {
			e.refused[server] = time.Now()
		}
	}
	if e.accepted != "" && e.accepted != e.current && e.move(ctx, e.accepted) {
		e.current = e.accepted
		e.s.Log.Warn("youtube_egress returned", "server", e.accepted)
	}
	return false
}
