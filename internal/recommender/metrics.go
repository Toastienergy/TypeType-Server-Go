package recommender

import "sync/atomic"

// Metrics is RecommenderMetrics, shown on the admin status page.
type Metrics struct {
	PagesFromCache        atomic.Int64
	PagesBuilt            atomic.Int64
	Fallbacks             atomic.Int64
	BuildMillis           atomic.Int64
	RecommenderMillis     atomic.Int64
	SourceMillis          atomic.Int64
	SourceRequests        atomic.Int64
	SourceCacheHits       atomic.Int64
	StreamRequests        atomic.Int64
	StreamRequestsRefused atomic.Int64
}
