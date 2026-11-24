package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type hlsSeg struct {
	handle  string
	startMs int64
}

type hlsPlan struct {
	headers  []string
	segments []hlsSeg
}

var hlsURIAttr = regexp.MustCompile(`URI="([^"]+)"`)

func handleOf(uri string) string {
	p, _, _ := strings.Cut(uri, "?")
	h := p[strings.LastIndex(p, "/")+1:]
	if mediaHandleRe.MatchString(h) {
		return h
	}
	return ""
}

// planHLS is HlsWarmupPlan.fromMediaPlaylist: the map and key handles, and every segment handle with its start.
func planHLS(playlist string) hlsPlan {
	var p hlsPlan
	seen := map[string]bool{}
	var start, dur int64
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP") || strings.HasPrefix(line, "#EXT-X-KEY"):
			if m := hlsURIAttr.FindStringSubmatch(line); m != nil {
				if h := handleOf(m[1]); h != "" && !seen[h] {
					seen[h] = true
					p.headers = append(p.headers, h)
				}
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			f, _ := strconv.ParseFloat(v, 64)
			dur = int64(f * 1000)
		case line != "" && !strings.HasPrefix(line, "#"):
			if h := handleOf(line); h != "" {
				p.segments = append(p.segments, hlsSeg{h, start})
			}
			start += dur
			dur = 0
		}
	}
	return p
}

type hlsGroup struct {
	playlists [][]hlsSeg
	audio     []hlsSeg
	size      int
	key       string
}

type hlsPos struct {
	group           *hlsGroup
	playlist, index int
}

// hlsChain is HlsSegmentChain: which NicoNico segment follows which, and the audio segment that plays beside a
// video one, 60,000 segments at most.
type hlsChain struct {
	mu        sync.Mutex
	groups    map[string]*hlsGroup
	order     []string
	positions map[string]hlsPos
	size      int
}

func newHLSChain() *hlsChain {
	return &hlsChain{groups: map[string]*hlsGroup{}, positions: map[string]hlsPos{}}
}

func (c *hlsChain) remember(video [][]hlsSeg, audio []hlsSeg) {
	var lists [][]hlsSeg
	for _, v := range append(append([][]hlsSeg{}, video...), audio) {
		if len(v) > 0 {
			lists = append(lists, v)
		}
	}
	if len(lists) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := lists[0][0].handle
	if c.groups[key] != nil {
		return
	}
	g := &hlsGroup{playlists: lists, key: key}
	if len(audio) > 0 {
		g.audio = audio
	}
	for _, l := range lists {
		g.size += len(l)
	}
	if g.size > 60_000 {
		return
	}
	c.groups[key] = g
	c.order = append(c.order, key)
	c.size += g.size
	for pi, l := range lists {
		for i, sg := range l {
			c.positions[sg.handle] = hlsPos{g, pi, i}
		}
	}
	for c.size > 60_000 && len(c.order) > 0 {
		old := c.groups[c.order[0]]
		delete(c.groups, c.order[0])
		c.order = c.order[1:]
		c.size -= old.size
		for _, l := range old.playlists {
			for _, sg := range l {
				if c.positions[sg.handle].group == old {
					delete(c.positions, sg.handle)
				}
			}
		}
	}
}

func (c *hlsChain) following(handle string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	pos, ok := c.positions[handle]
	if !ok {
		return nil
	}
	segs := pos.group.playlists[pos.playlist]
	var out []string
	if pos.index+1 < len(segs) {
		out = append(out, segs[pos.index+1].handle)
	}
	if a := pos.group.audio; a != nil && &a[0] != &segs[0] {
		at := segs[pos.index].startMs
		i := sort.Search(len(a), func(i int) bool { return a[i].startMs > at }) - 1
		if i >= 0 {
			out = append(out, a[i].handle)
		}
	}
	return out
}

// warmHLS is ProviderMediaWarmup.warmHls: every playlist, then the map and key of each and the first segment of
// the ones that start playback.
func (w *warmup) warmHLS(ctx context.Context, media []warmMedia, videoCount int) {
	plans := make([]*hlsPlan, len(media))
	var wg sync.WaitGroup
	for i, m := range media {
		wg.Go(func() {
			t := w.targetOf(ctx, m.url)
			if t == nil {
				return
			}
			if b := w.prefetch(ctx, t, "", warmPlaylistMax); b != nil {
				p := planHLS(string(b))
				plans[i] = &p
			}
		})
	}
	wg.Wait()
	var video [][]hlsSeg
	for i := 0; i < videoCount && i < len(plans); i++ {
		if plans[i] != nil {
			video = append(video, plans[i].segments)
		}
	}
	var audio []hlsSeg
	if videoCount < len(plans) && plans[videoCount] != nil {
		audio = plans[videoCount].segments
	}
	w.chain.remember(video, audio)
	seen := map[string]bool{}
	var files []string
	for i, p := range plans {
		if p == nil {
			continue
		}
		list := append([]string{}, p.headers...)
		if media[i].starts && len(p.segments) > 0 {
			list = append(list, p.segments[0].handle)
		}
		for _, f := range list {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	for _, f := range files {
		wg.Go(func() {
			if t := w.targetOf(ctx, "/media/"+f); t != nil {
				w.prefetch(ctx, t, "", warmSegmentMax)
			}
		})
	}
	wg.Wait()
}

var rangeStart = regexp.MustCompile(`^bytes=(\d+)-\d*$`)

// onServed is ProviderMediaReadAhead: the next HLS files after a whole-file request, or the next DASH segment after
// a range request.
func (w *warmup) onServed(handle string, t *mediaTarget, rng string) {
	if rng == "" {
		next := w.chain.following(handle)
		if len(next) == 0 || !w.available() {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), warmTimeout)
			defer cancel()
			var wg sync.WaitGroup
			for _, f := range next {
				wg.Go(func() {
					if nt := w.targetOf(ctx, "/media/"+f); nt != nil {
						w.prefetch(ctx, nt, "", warmSegmentMax)
					}
				})
			}
			wg.Wait()
		}()
		return
	}
	m := rangeStart.FindStringSubmatch(strings.TrimSpace(rng))
	if m == nil {
		return
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	if seg, ok := w.after(t.URL, start); ok && seg[1]-seg[0]+1 <= warmSegmentMax {
		w.prefetchSegment(t, seg)
	}
}

// serveWarm is startupCacheOrUpstream: the cache, or after 1.2 s of waiting on a warmup still running, whichever of
// the warmup and a fresh upstream request answers first.
func (s *Server) serveWarm(w http.ResponseWriter, r *http.Request, t *mediaTarget) {
	c, rng := s.warm.cache, r.Header.Get("Range")
	if m := c.respond(t.URL, rng); m != nil {
		writeMediaReply(w, r, m)
		return
	}
	if c.isPending(t.URL) {
		c.waitPending(t.URL, startHedgeAfter)
		if m := c.respond(t.URL, rng); m != nil {
			writeMediaReply(w, r, m)
			return
		}
	}
	if !c.isPending(t.URL) {
		s.pipeMedia(w, r, append([]string{t.URL}, t.Alternates...), t.DomandBid)
		return
	}
	type up struct {
		m    *mediaReply
		cerr *commentErr
	}
	relay := make(chan up, 1)
	go func() {
		m, cerr := s.openMedia(context.WithoutCancel(r.Context()), append([]string{t.URL}, t.Alternates...), t.DomandBid, rng)
		relay <- up{m, cerr}
	}()
	warmed := make(chan *mediaReply, 1)
	go func() {
		c.waitPending(t.URL, startPendingWait)
		warmed <- c.respond(t.URL, rng)
	}()
	select {
	case m := <-warmed:
		if m != nil {
			go func() { (<-relay).m.close() }()
			writeMediaReply(w, r, m)
			return
		}
	case <-r.Context().Done():
		go func() { (<-relay).m.close() }()
		return
	case u := <-relay:
		if u.cerr != nil {
			if u.cerr.status != 499 {
				u.cerr.write(w, r)
			}
			return
		}
		writeMediaReply(w, r, u.m)
		return
	}
	select {
	case u := <-relay:
		if u.cerr != nil {
			if u.cerr.status != 499 {
				u.cerr.write(w, r)
			}
			return
		}
		writeMediaReply(w, r, u.m)
	case <-r.Context().Done():
		go func() { (<-relay).m.close() }()
	}
}
