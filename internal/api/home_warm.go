package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"typetype.dev/server/internal/config"
	"typetype.dev/server/internal/recommend"
)

// homeWarm is HomeRecommendationWarmupService: after a change that moves a user's home (a subscription, Focus),
// the pools are dropped, the new subscription feed awaited and the home and Shorts pools built again; users stay
// warmed every ten minutes for an hour, at most once per ten minutes unless forced.
type homeWarm struct {
	mu      sync.Mutex
	active  map[string]time.Time
	started map[string]time.Time
}

const (
	homeWarmThrottle = 10 * time.Minute
	homeWarmActive   = time.Hour
)

func (s *Server) startHomeWarm() {
	s.homeWarm = &homeWarm{active: map[string]time.Time{}, started: map[string]time.Time{}}
	go func() {
		for range time.Tick(homeWarmThrottle) {
			h := s.homeWarm
			h.mu.Lock()
			var users []string
			for u, at := range h.active {
				if time.Since(at) > homeWarmActive {
					delete(h.active, u)
					delete(h.started, u)
					continue
				}
				users = append(users, u)
			}
			h.mu.Unlock()
			for _, u := range users {
				s.scheduleHomeWarm(u, false)
			}
		}
	}()
}

// invalidateAndWarm is HomeRecommendationWarmup.invalidateAndWarm.
func (s *Server) invalidateAndWarm(user string) {
	if s.homeWarm == nil {
		return
	}
	s.homeWarm.mu.Lock()
	s.homeWarm.active[user] = time.Now()
	s.homeWarm.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.recs.invalidate(ctx, user)
		select {
		case <-s.feeds.refresh(user):
		case <-ctx.Done():
		}
		s.scheduleHomeWarm(user, true)
	}()
}

func (s *Server) scheduleHomeWarm(user string, force bool) {
	h := s.homeWarm
	h.mu.Lock()
	if at, ok := h.started[user]; !force && ok && time.Since(at) < homeWarmThrottle {
		h.mu.Unlock()
		return
	}
	h.started[user] = time.Now()
	h.mu.Unlock()
	go s.warmHome(user)
}

func (s *Server) warmHome(user string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", "/recommendations/home", nil)
	pr, sig, err := s.recs.profile(r, user)
	if err != nil {
		return
	}
	for _, shorts := range []bool{false, true} {
		if !shorts && s.homeServedElsewhere(ctx, user) {
			continue
		}
		pool := s.recs.build(ctx, pr, sig, user, 0, recommend.IntentAuto, true, shorts)
		if b, err := json.Marshal(pool); err == nil {
			key := poolKey(user, 0, shorts)
			s.Cache.Set(ctx, key, string(b), poolTTL)
			s.Cache.Set(ctx, key+":stale", string(b), poolStaleTTL)
		}
	}
}

// homeServedElsewhere: the external recommender is offered and the user has it on (on by default when the admin
// says so), so the home page comes from it, not from this pool.
func (s *Server) homeServedElsewhere(ctx context.Context, user string) bool {
	st, err := s.Admin.Get(ctx)
	if err != nil || config.TrimmedURL("RECOMMENDER_SERVICE_URL") == "" || !st.RecommenderEnabled {
		return false
	}
	var enabled bool
	err = s.DB.QueryRow(ctx, "SELECT enabled FROM recommender_user_settings WHERE user_id = $1", user).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return st.RecommenderDefaultOn
	}
	return err == nil && enabled
}
