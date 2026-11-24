// Package portability imports and exports account data in TypeType's own format and the formats of other video
// apps, as the Kotlin server's portability engine does: an upload is analysed into a spool, previewed, then
// applied; an export collects into a spool and encodes it.
package portability

import (
	"encoding/json"
	"strconv"
	"strings"

	"typetype.dev/server/internal/db"
)

type Category string

const (
	Subscriptions      Category = "subscriptions"
	SubscriptionGroups Category = "subscriptionGroups"
	History            Category = "history"
	Playlists          Category = "playlists"
	WatchLater         Category = "watchLater"
	Favorites          Category = "favorites"
	Progress           Category = "progress"
	SearchHistory      Category = "searchHistory"
	SavedPlaylists     Category = "savedPlaylists"
	Settings           Category = "settings"
	ContentFilters     Category = "contentFilters"
)

// Categories is PortabilityCategory in declaration order.
var Categories = []Category{Subscriptions, SubscriptionGroups, History, Playlists, WatchLater, Favorites, Progress,
	SearchHistory, SavedPlaylists, Settings, ContentFilters}

func ValidCategory(c Category) bool {
	for _, x := range Categories {
		if x == c {
			return true
		}
	}
	return false
}

type Format string

const (
	TypeType      Format = "typetype"
	PipePipe      Format = "pipepipe"
	NewPipe       Format = "newpipe"
	Invidious     Format = "invidious"
	Piped         Format = "piped"
	LibreTube     Format = "libretube"
	ViewTube      Format = "viewtube"
	Materialious  Format = "materialious"
	YoutubeLocal  Format = "youtube-local"
	Flow          Format = "flow"
	SkyTube       Format = "skytube"
	Grayjay       Format = "grayjay"
	YoutubeTakout Format = "youtube-takeout"
	OPML          Format = "opml"
)

// Formats is PortabilityFormat in declaration order.
var Formats = []Format{TypeType, PipePipe, NewPipe, Invidious, Piped, LibreTube, ViewTube, Materialious, YoutubeLocal,
	Flow, SkyTube, Grayjay, YoutubeTakout, OPML}

// Video is PortabilityVideo.
type Video struct {
	URL              string `json:"url"`
	Title            string `json:"title,omitempty"`
	ThumbnailURL     string `json:"thumbnailUrl,omitempty"`
	DurationSeconds  int64  `json:"durationSeconds,omitempty"`
	ChannelName      string `json:"channelName,omitempty"`
	ChannelURL       string `json:"channelUrl,omitempty"`
	ChannelAvatarURL string `json:"channelAvatarUrl,omitempty"`
	ViewCount        int64  `json:"viewCount,omitempty"`
	PublishedAt      int64  `json:"publishedAt"`
}

func (v Video) Normalized() Video {
	v.URL, v.Title, v.ThumbnailURL = strings.TrimSpace(v.URL), strings.TrimSpace(v.Title), strings.TrimSpace(v.ThumbnailURL)
	v.ChannelName, v.ChannelURL, v.ChannelAvatarURL = strings.TrimSpace(v.ChannelName), strings.TrimSpace(v.ChannelURL), strings.TrimSpace(v.ChannelAvatarURL)
	v.DurationSeconds, v.ViewCount = max(v.DurationSeconds, 0), max(v.ViewCount, 0)
	return v
}

// Record is one PortabilityRecord; Type names the variant (its kotlinx serial name) and only that variant's fields
// are set.
type Record struct {
	Type string `json:"type"`

	ChannelURL   string `json:"channelUrl,omitempty"`
	Name         string `json:"name,omitempty"`
	AvatarURL    string `json:"avatarUrl,omitempty"`
	SubscribedAt int64  `json:"subscribedAt,omitempty"`
	GroupName    string `json:"groupName,omitempty"`

	Video           *Video `json:"video,omitempty"`
	WatchedAt       int64  `json:"watchedAt,omitempty"`
	PositionSeconds int64  `json:"positionSeconds,omitempty"`

	SourceID         string `json:"sourceId,omitempty"`
	Description      string `json:"description,omitempty"`
	CreatedAt        int64  `json:"createdAt,omitempty"`
	PlaylistSourceID string `json:"playlistSourceId,omitempty"`
	Position         int    `json:"position,omitempty"`
	AddedAt          int64  `json:"addedAt,omitempty"`
	FavoritedAt      int64  `json:"favoritedAt,omitempty"`

	VideoURL  string `json:"videoUrl,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`

	Term       string `json:"term,omitempty"`
	SearchedAt int64  `json:"searchedAt,omitempty"`

	URL          string `json:"url,omitempty"`
	Title        string `json:"title,omitempty"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	UploaderName string `json:"uploaderName,omitempty"`
	StreamCount  int64  `json:"streamCount,omitempty"`
	PlaylistType string `json:"playlistType,omitempty"`
	SavedAt      int64  `json:"savedAt,omitempty"`

	Values json.RawMessage `json:"values,omitempty"`

	Kind     string          `json:"kind,omitempty"`
	Value    string          `json:"value,omitempty"`
	Label    string          `json:"label,omitempty"`
	ImageURL string          `json:"imageUrl,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

const (
	TypeSubscription    = "subscription"
	TypeGroup           = "subscriptionGroup"
	TypeGroupMembership = "subscriptionGroupMembership"
	TypeHistory         = "history"
	TypePlaylist        = "playlist"
	TypePlaylistVideo   = "playlistVideo"
	TypeWatchLater      = "watchLater"
	TypeFavorite        = "favorite"
	TypeProgress        = "progress"
	TypeSearchHistory   = "searchHistory"
	TypeSavedPlaylist   = "savedPlaylist"
	TypeSettings        = "settings"
	TypeRecommender     = "recommender"
	TypeContentFilter   = "contentFilter"
)

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func (r *Record) Category() Category {
	switch r.Type {
	case TypeSubscription:
		return Subscriptions
	case TypeGroup, TypeGroupMembership:
		return SubscriptionGroups
	case TypeHistory:
		return History
	case TypePlaylist, TypePlaylistVideo:
		return Playlists
	case TypeWatchLater:
		return WatchLater
	case TypeFavorite:
		return Favorites
	case TypeProgress:
		return Progress
	case TypeSearchHistory:
		return SearchHistory
	case TypeSavedPlaylist:
		return SavedPlaylists
	case TypeSettings, TypeRecommender:
		return Settings
	case TypeContentFilter:
		return ContentFilters
	}
	return ""
}

func (r *Record) videoURL() string {
	if r.Video == nil {
		return ""
	}
	return lower(r.Video.URL)
}

// StableKey is the record's identity within its category; a second record with the same key is a duplicate.
func (r *Record) StableKey() string {
	switch r.Type {
	case TypeSubscription:
		return strings.ToLower(db.CanonicalChannelURL(r.ChannelURL))
	case TypeGroup:
		return "group:" + lower(r.Name)
	case TypeGroupMembership:
		return "member:" + lower(r.GroupName) + ":" + lower(r.ChannelURL)
	case TypeHistory:
		return r.videoURL() + ":" + strconv.FormatInt(r.WatchedAt, 10)
	case TypePlaylist:
		id := r.SourceID
		if strings.TrimSpace(id) == "" {
			id = r.Name
		}
		return "playlist:" + lower(id)
	case TypePlaylistVideo:
		return "playlist-video:" + lower(r.PlaylistSourceID) + ":" + strconv.Itoa(r.Position)
	case TypeWatchLater, TypeFavorite:
		return r.videoURL()
	case TypeProgress:
		return lower(r.VideoURL)
	case TypeSearchHistory:
		return lower(r.Term) + ":" + strconv.FormatInt(r.SearchedAt, 10)
	case TypeSavedPlaylist:
		return lower(r.URL)
	case TypeSettings:
		return "settings"
	case TypeRecommender:
		return "recommender"
	case TypeContentFilter:
		return lower(r.Kind) + ":" + lower(r.Value)
	}
	return ""
}

// ParentKey groups memberships under their group and playlist videos under their playlist.
func (r *Record) ParentKey() string {
	switch r.Type {
	case TypeGroupMembership:
		return lower(r.GroupName)
	case TypePlaylistVideo:
		return lower(r.PlaylistSourceID)
	}
	return ""
}

// Issue is PortabilityIssue; an empty Category is serialized as null.
type Issue struct {
	Category Category `json:"category"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Count    int64    `json:"count"`
}

func (i Issue) MarshalJSON() ([]byte, error) {
	var c any
	if i.Category != "" {
		c = i.Category
	}
	return json.Marshal(struct {
		Category any    `json:"category"`
		Code     string `json:"code"`
		Message  string `json:"message"`
		Count    int64  `json:"count"`
	}{c, i.Code, i.Message, i.Count})
}
