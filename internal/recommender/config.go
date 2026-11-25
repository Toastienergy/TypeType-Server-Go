// Package recommender relays the home page and the recommender settings to TypeType-Signals, as Kotlin's
// RecommenderServices does.
package recommender

import (
	"os"
	"strconv"
	"strings"
	"time"

	"typetype.dev/server/internal/config"
)

// Config is RecommenderConfig: the service address and token, and the timeouts, cache lifetimes and limits of the
// pages built through it.
type Config struct {
	ServiceURL              string
	InternalToken           string
	RequestTimeout          time.Duration
	PageDeadline            time.Duration
	ParallelBuilds          int
	StreamRequestsPerMinute int
	StreamWallCooldown      time.Duration
	RelatedTTL              time.Duration
	SearchTTL               time.Duration
	ChannelTTL              time.Duration
	PageTTL                 time.Duration
	SettingsTTL             time.Duration
	HistoryKeyTTL           time.Duration
	HistoryMaxItems         int
	FavoritesMaxItems       int
	HistoryChunkItems       int
	PageVariants            int
	MaxSourceParallelism    int
}

// Installed reports a service address; the admin still has to turn the recommender on.
func (c Config) Installed() bool { return c.ServiceURL != "" }

func FromEnvironment() Config {
	return Config{
		ServiceURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("RECOMMENDER_SERVICE_URL")), "/"),
		InternalToken:           config.Secret("RECOMMENDER_INTERNAL_TOKEN"),
		RequestTimeout:          millis("RECOMMENDER_REQUEST_TIMEOUT_MS", 2_500),
		PageDeadline:            millis("RECOMMENDER_PAGE_DEADLINE_MS", 4_000),
		ParallelBuilds:          min(max(int(number("RECOMMENDER_PARALLEL_BUILDS", 3)), 1), 64),
		StreamRequestsPerMinute: int(number("RECOMMENDER_STREAM_REQUESTS_PER_MINUTE", 15)),
		StreamWallCooldown:      seconds("RECOMMENDER_STREAM_WALL_COOLDOWN_SECONDS", 900),
		RelatedTTL:              seconds("RECOMMENDER_RELATED_TTL_SECONDS", 86_400),
		SearchTTL:               seconds("RECOMMENDER_SEARCH_TTL_SECONDS", 21_600),
		ChannelTTL:              seconds("RECOMMENDER_CHANNEL_TTL_SECONDS", 1_800),
		PageTTL:                 seconds("RECOMMENDER_PAGE_TTL_SECONDS", 1_800),
		SettingsTTL:             seconds("RECOMMENDER_SETTINGS_TTL_SECONDS", 600),
		HistoryKeyTTL:           seconds("RECOMMENDER_HISTORY_KEY_TTL_SECONDS", 86_400),
		HistoryMaxItems:         max(int(number("RECOMMENDER_HISTORY_MAX_ITEMS", 100_000)), 1),
		FavoritesMaxItems:       max(int(number("RECOMMENDER_FAVORITES_MAX_ITEMS", 50_000)), 1),
		HistoryChunkItems:       max(int(number("RECOMMENDER_HISTORY_CHUNK_ITEMS", 20_000)), 1),
		PageVariants:            max(int(number("RECOMMENDER_PAGE_VARIANTS", 3)), 1),
		MaxSourceParallelism:    max(int(number("RECOMMENDER_SOURCE_PARALLELISM", 6)), 1),
	}
}

// number reads a non-negative integer, else the default, like Kotlin's long() helper.
func number(name string, def int64) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func millis(name string, def int64) time.Duration {
	return time.Duration(number(name, def)) * time.Millisecond
}
func seconds(name string, def int64) time.Duration {
	return time.Duration(number(name, def)) * time.Second
}
