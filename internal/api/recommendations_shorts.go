package api

import (
	"context"
	"time"

	"typetype.dev/server/internal/recommend"
)

// buildShorts is HomeRecommendationShortsCandidateService: subscription Shorts first, then Shorts related to them and
// to favorites. Kotlin's last resort of fake "saved short" cards built from watch-later URLs is left out.
func (p *recPools) buildShorts(ctx context.Context, pr *recommend.Profile, sig *signals, user string, svc int, intent recommend.Intent, full bool) recommend.Pool {
	empty := recommend.Pool{Subscriptions: []recommend.Video{}, Discovery: []recommend.Video{}, SourceByURL: map[string]recommend.Source{}}
	if !full {
		cached, _ := p.s.cachedShorts(ctx, user)
		return recommend.BuildShortsPool(pr, tag(cached[:min(60, len(cached))], recommend.Subscription, svc), nil, intent, time.Now())
	}
	if svc != 0 {
		return empty
	}
	feed, _ := p.s.shortsFeed(ctx, user)
	if len(feed) == 0 {
		for _, v := range p.subscriptionVideos(user, 90, false) {
			if recommend.IsShort(v) {
				feed = append(feed, v)
			}
		}
	}
	var subs []recommend.Tagged
	for _, v := range feed {
		if recommend.IsShort(v) && len(subs) < 90 {
			v.IsShortFormContent = true
			subs = append(subs, recommend.Tagged{Video: v, Source: recommend.Subscription})
		}
	}
	seeds := make([]string, len(subs))
	for i, t := range subs {
		seeds[i] = t.Video.URL
	}
	rel := recommend.MarkShorts(p.related(ctx, seeds, 8, recommend.Theme))
	rel = append(rel, recommend.MarkShorts(p.related(ctx, sig.favorites, 6, recommend.Exploration))...)
	disc := recommend.AssembleDiscovery(pr, rel, 180)
	subs = recommend.DedupeShorts(subs, sig.history, sig.subscriptions)
	disc = recommend.DedupeShorts(disc, sig.history, sig.subscriptions)
	disc = recommend.ShortsFallback(subs, disc[:min(160, len(disc))])
	return recommend.BuildShortsPool(pr, subs, disc, intent, time.Now())
}
