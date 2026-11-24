package sabr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ProbeUser owns the sessions opened to judge an exit; their refusals are verdicts, not signals.
const ProbeUser = "egress-exit-check"

// SetOnHold registers what runs when YouTube puts a playback session on hold or refuses it: SabrSessionStore's
// reportProtectionPending and recoverProtectedPlaybackInfo, which hand the video to TokenSessionAcceptance.
func (st *Store) SetOnHold(f func(reason, videoID string)) {
	st.mu.Lock()
	st.onHold = f
	st.mu.Unlock()
}

func (st *Store) hold(reason, videoID, userID string) {
	if userID == ProbeUser {
		return
	}
	st.mu.Lock()
	f := st.onHold
	st.mu.Unlock()
	if f != nil {
		go f(reason, videoID)
	}
}

// Protection is the highest stream protection status YouTube gave the session: 1 accepted, 2 on hold (only the
// start of a video plays), 3 attestation required.
func (s *Session) Protection() int32 { return s.protection.Load() }

func (s *Session) noteProtection(status int32) {
	for {
		cur := s.protection.Load()
		if status <= cur || s.protection.CompareAndSwap(cur, status) {
			break
		}
	}
	if status == 2 {
		s.store.hold("sabr_attestation_pending", s.VideoID, s.UserID)
	}
}

// Generation is the token service's current session generation for a video, as TypetypeTokenSabrTokenClient.fetch.
func (t *Token) Generation(ctx context.Context, videoID string, refresh bool) (string, error) {
	if t.base == "" {
		return "", ErrNoToken
	}
	u := t.base + "/potoken?videoId=" + url.QueryEscape(videoID)
	if refresh {
		u += "&refresh=true"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	res, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return "", fmt.Errorf("token /potoken HTTP %d", res.StatusCode)
	}
	var out struct {
		Generation string `json:"generation"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	return out.Generation, nil
}

// Renew is SabrSessionStore.renewTokenSession: ask the token service for a new session and wait, at most 45 s,
// for its generation to change, then drop the playback infos minted with the old one.
func (t *Token) Renew(ctx context.Context, videoID string) {
	prev, _ := t.Generation(ctx, videoID, false)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if g, err := t.Generation(ctx, videoID, true); err == nil && g != "" && g != prev {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	t.Forget()
}
