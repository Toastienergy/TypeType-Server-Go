package recommender

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Subscription is a subscribed channel as the snapshot names it.
type Subscription struct {
	ChannelURL string
	Name       string
}

// PageRequest is RecommenderPageRequest.
type PageRequest struct {
	User        string
	AlgorithmID string
	Settings    Settings
	Seed        int32
	Locale      string
	Timezone    string
	Timeout     time.Duration
}

// Snapshots is RecommenderSnapshotService: what the service needs to plan a page, narrowed by the data filter.
type Snapshots struct {
	history       *History
	subscriptions func(ctx context.Context, user string) ([]Subscription, error)
	blocked       func(ctx context.Context, user string) (videos, channels []string, err error)
	notForMe      func(ctx context.Context, user string) []string
	dataUsedBy    func(ctx context.Context, locale, algorithmID string) map[string]bool
}

func NewSnapshots(history *History, subscriptions func(ctx context.Context, user string) ([]Subscription, error),
	blocked func(ctx context.Context, user string) ([]string, []string, error), notForMe func(ctx context.Context, user string) []string,
	dataUsedBy func(ctx context.Context, locale, algorithmID string) map[string]bool) *Snapshots {
	return &Snapshots{history: history, subscriptions: subscriptions, blocked: blocked, notForMe: notForMe, dataUsedBy: dataUsedBy}
}

// Snapshot builds the plan request; nil when the algorithm is not offered or the history could not be sent.
func (s *Snapshots) Snapshot(ctx context.Context, req PageRequest, resend bool) map[string]any {
	now := time.Now().UnixMilli()
	uses := s.dataUsedBy(ctx, req.Locale, req.AlgorithmID)
	if uses == nil {
		return nil
	}
	f := NewDataFilter(req.Settings, now, uses)
	historyKey := ""
	if f.NeedsHistorySnapshot() {
		key, ok := s.history.EnsureSent(ctx, req.User, req.Timeout, f, resend)
		if !ok {
			return nil
		}
		historyKey = key
	}
	var subs []Subscription
	if f.Uses(CatSubscriptions) {
		var err error
		if subs, err = s.subscriptions(ctx, req.User); err != nil {
			return nil
		}
	}
	var notInterested, muted []string
	if f.Uses(CatBlocked) {
		videos, channels, err := s.blocked(ctx, req.User)
		if err != nil {
			return nil
		}
		notInterested, muted = videos, channels
	}
	if f.Uses(CatNotInterested) {
		notInterested = append(notInterested, s.notForMe(ctx, req.User)...)
	}
	return snapshotBody(req, now, historyKey, subs, distinct(notInterested), muted)
}

func snapshotBody(req PageRequest, nowMs int64, historyKey string, subs []Subscription, notInterested, muted []string) map[string]any {
	params := map[string]json.RawMessage{}
	for k, v := range req.Settings.Parameters {
		params[k] = v
	}
	out := map[string]any{
		"algorithm": map[string]any{"id": req.AlgorithmID, "parameters": params},
		"context":   map[string]any{"now": nowMs, "timezone": req.Timezone, "locale": req.Locale, "seed": req.Seed},
	}
	if historyKey != "" {
		out["history_key"] = historyKey
	}
	subscriptions := make([]any, 0, len(subs))
	for _, sub := range subs {
		subscriptions = append(subscriptions, map[string]any{"id": "subscription:" + sub.ChannelURL, "channel_url": sub.ChannelURL, "name": sub.Name})
	}
	out["subscriptions"] = subscriptions
	refs := make([]any, 0, len(notInterested))
	for _, u := range notInterested {
		refs = append(refs, map[string]any{"id": "not-interested:" + u, "video_url": u})
	}
	out["not_interested"] = refs
	overrides := map[string]any{
		"muted_channels":   orEmpty(muted),
		"muted_groups":     orEmpty(req.Settings.MutedGroups),
		"ignored_signals":  orEmpty(req.Settings.IgnoredSignals),
		"pinned_groups":    orEmpty(req.Settings.PinnedGroups),
		"custom_interests": orEmpty(req.Settings.CustomInterests),
	}
	if req.Settings.SessionResetAt != nil {
		overrides["session_reset_at"] = *req.Settings.SessionResetAt
	}
	out["overrides"] = overrides
	return out
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func distinct(l []string) []string {
	var out []string
	for _, s := range l {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
