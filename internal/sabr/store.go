package sabr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// Store owns every playback session: lookup by id, the shared googlevideo client, limits and eviction.
type Store struct {
	token       *Token
	media       *http.Client
	maxSessions int
	idle        time.Duration
	budget      int64

	mu       sync.Mutex
	sessions map[string]*Session
	prewarm  map[string]prewarmed
	onHold   func(reason, videoID string)
}

type prewarmed struct {
	id    string
	start int64
	at    time.Time
}

type Options struct {
	TokenURL    string
	ProxyURL    string
	MaxSessions int
	Idle        time.Duration
	BudgetBytes int64
}

func NewStore(o Options) *Store {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ReadBufferSize:      64 << 10,
	}
	if o.ProxyURL != "" {
		if u, err := url.Parse(o.ProxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	st := &Store{token: NewToken(o.TokenURL), media: &http.Client{Transport: tr}, maxSessions: max(1, o.MaxSessions),
		idle: o.Idle, budget: o.BudgetBytes, sessions: map[string]*Session{}, prewarm: map[string]prewarmed{}}
	go st.sweep()
	return st
}

func (st *Store) Token() *Token { return st.token }

func newID() string {
	b := make([]byte, 18)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Create starts a session and its pump at startMs.
func (st *Store) Create(info *Info, videoID, userID string, video, audio *Format, startMs int64, audioOnly bool, gen int64) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{ID: newID(), VideoID: videoID, UserID: userID, Video: video, Audio: audio, AudioOnly: audioOnly,
		store: st, info: info, gen: gen, playerMs: max(0, startMs), changed: make(chan struct{}), wake: make(chan struct{}, 1),
		cancel: cancel, lastUsed: time.Now()}
	s.tracks[videoTrack], s.tracks[audioTrack] = newTrack(video), newTrack(audio)
	st.mu.Lock()
	st.sessions[s.ID] = s
	over := len(st.sessions) - st.maxSessions
	st.mu.Unlock()
	if over > 0 {
		st.evictOldest(over, s.ID)
	}
	go s.run(ctx)
	return s
}

func (st *Store) Lookup(id string) *Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sessions[id]
}

func (st *Store) Release(s *Session) {
	st.mu.Lock()
	if st.sessions[s.ID] == s {
		delete(st.sessions, s.ID)
	}
	st.mu.Unlock()
	s.cancel()
	s.mu.Lock()
	s.dropMediaLocked()
	s.notify()
	s.mu.Unlock()
}

// Remember keeps a prewarmed session for the create that follows it.
func (st *Store) Remember(key string, s *Session, start int64) {
	st.mu.Lock()
	st.prewarm[key] = prewarmed{id: s.ID, start: start, at: time.Now()}
	st.mu.Unlock()
}

// HasPrewarmed reports a prewarmed session waiting under key.
func (st *Store) HasPrewarmed(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.prewarm[key]
	return ok
}

func (st *Store) TakePrewarmed(key string) (*Session, int64) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.prewarm[key]
	if !ok {
		return nil, 0
	}
	delete(st.prewarm, key)
	s := st.sessions[p.id]
	if s == nil {
		return nil, 0
	}
	return s, p.start
}

func (st *Store) evictOldest(n int, keep string) {
	st.mu.Lock()
	all := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		if s.ID != keep {
			all = append(all, s)
		}
	}
	st.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return lastUsed(all[i]).Before(lastUsed(all[j])) })
	for i := 0; i < n && i < len(all); i++ {
		st.Release(all[i])
	}
}

func lastUsed(s *Session) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastUsed
}

// sweep releases sessions nobody asked anything for idle, and trims the largest ones while the store holds more
// than its byte budget.
func (st *Store) sweep() {
	for range time.Tick(5 * time.Second) {
		st.mu.Lock()
		all := make([]*Session, 0, len(st.sessions))
		for _, s := range st.sessions {
			all = append(all, s)
		}
		for k, p := range st.prewarm {
			if time.Since(p.at) > time.Minute {
				delete(st.prewarm, k)
			}
		}
		st.mu.Unlock()
		trimSpare(len(all))
		var total int64
		for _, s := range all {
			if time.Since(lastUsed(s)) > st.idle {
				st.Release(s)
				continue
			}
			s.mu.Lock()
			total += s.bytes
			s.mu.Unlock()
		}
		if st.budget > 0 && total > st.budget {
			sort.Slice(all, func(i, j int) bool { return lastUsed(all[i]).Before(lastUsed(all[j])) })
			for _, s := range all {
				if total <= st.budget {
					break
				}
				s.mu.Lock()
				before := s.bytes
				s.trimAheadLocked()
				total -= before - s.bytes
				s.mu.Unlock()
			}
		}
	}
}

// trimAheadLocked keeps only the lead window when memory is short: the client asks again for anything dropped.
func (s *Session) trimAheadLocked() {
	ceiling := s.playerMs + leadMs
	for _, t := range s.tracks {
		for k, sg := range t.segs {
			if sg.StartMs > ceiling {
				t.bytes -= int64(len(sg.data))
				s.bytes -= int64(len(sg.data))
				delete(t.segs, k)
				sg.Release()
			}
		}
	}
	s.evictLocked()
}
