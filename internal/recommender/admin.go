package recommender

import (
	"context"
	"time"
)

// Status is RecommenderAdminService.status for an installed recommender: reachability, offered algorithms and the
// counters since the server started.
func Status(ctx context.Context, cfg Config, admin Admin, client *Client, catalog *Catalog, m *Metrics, limiter *Limiter, locale string) map[string]any {
	started := time.Now()
	reachable := client.Health(ctx)
	var healthMs any
	algorithms := []AlgorithmName{}
	var defaultAlgorithm any
	if reachable {
		healthMs = time.Since(started).Milliseconds()
		if names := catalog.AllNames(ctx, locale); names != nil {
			algorithms = names
		}
		if d := catalog.DefaultID(ctx, locale); d != "" {
			defaultAlgorithm = d
		}
	}
	built := m.PagesBuilt.Load()
	avg := func(total int64) int64 {
		if built > 0 {
			return total / built
		}
		return 0
	}
	limit := cfg.StreamRequestsPerMinute
	if admin.StreamRequestsPerMinute > 0 {
		limit = admin.StreamRequestsPerMinute
	}
	timeout := cfg.RequestTimeout.Milliseconds()
	if admin.TimeoutMs > 0 {
		timeout = int64(admin.TimeoutMs)
	}
	return map[string]any{
		"installed": true, "enabled": admin.Enabled, "reachable": reachable, "healthMs": healthMs,
		"algorithms": algorithms, "defaultAlgorithm": defaultAlgorithm,
		"pagesBuilt": built, "pagesFromCache": m.PagesFromCache.Load(), "fallbacks": m.Fallbacks.Load(),
		"averageBuildMs": avg(m.BuildMillis.Load()), "averageRecommenderMs": avg(m.RecommenderMillis.Load()),
		"averageSourcesMs": avg(m.SourceMillis.Load()), "buildMsTotal": m.BuildMillis.Load(),
		"recommenderMsTotal": m.RecommenderMillis.Load(), "sourcesMsTotal": m.SourceMillis.Load(),
		"sourceRequests": m.SourceRequests.Load(), "sourceCacheHits": m.SourceCacheHits.Load(),
		"streamRequests": m.StreamRequests.Load(), "streamRequestsRefused": m.StreamRequestsRefused.Load(),
		"streamRequestsThisMinute": limiter.UsedThisMinute(ctx), "streamRequestsPaused": limiter.Walled(ctx),
		"streamRequestsPerMinuteLimit": limit, "requestTimeoutMs": timeout,
	}
}
