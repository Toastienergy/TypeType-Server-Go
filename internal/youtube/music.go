package youtube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// WebRemix is the YouTube Music web client PipePipe searches with, at its hardcoded version.
var WebRemix = ClientInfo{Name: "WEB_REMIX", Version: "1.20250122.01.00", ID: 67,
	UA: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"}

type musicPage struct {
	Continuation string `json:"c"`
	Type         string `json:"t"`
}

// MusicSearch is YoutubeMusicSearchExtractor: one shelf of songs, videos, albums, playlists or artists from
// music.youtube.com, searchType being the filter name and params its fixed search params.
func (c *Client) MusicSearch(ctx context.Context, query, searchType, params, page string) (SearchPage, error) {
	out := SearchPage{Items: []VideoItem{}, Playlists: []PlaylistResult{}, Channels: []ChannelResult{}}
	endpoint := "https://music.youtube.com/youtubei/v1/search?prettyPrint=false"
	body := map[string]any{}
	if page != "" {
		rest, ok := strings.CutPrefix(page, "ytm1.")
		b, err := base64.RawURLEncoding.DecodeString(rest)
		var pg musicPage
		if !ok || err != nil || json.Unmarshal(b, &pg) != nil || pg.Continuation == "" {
			return out, ErrBadPage
		}
		searchType = pg.Type
		ct := url.QueryEscape(pg.Continuation)
		endpoint = "https://music.youtube.com/youtubei/v1/search?ctoken=" + ct + "&continuation=" + ct + "&prettyPrint=false"
	} else {
		body["query"], body["params"] = query, params
	}
	res, err := c.musicCall(ctx, endpoint, body)
	if err != nil {
		return out, err
	}
	var shelf map[string]any
	if page != "" {
		shelf = Obj(res, "continuationContents", "musicShelfContinuation")
	} else {
		for _, s := range List(res, "contents", "tabbedSearchResultsRenderer", "tabs", 0, "tabRenderer", "content", "sectionListRenderer", "contents") {
			if m := Obj(s, "musicShelfRenderer"); m != nil {
				shelf = m
			}
			if d := Obj(s, "itemSectionRenderer", "contents", 0, "didYouMeanRenderer"); d != nil {
				t := Text(d["correctedQuery"])
				out.SearchSuggestion = &t
			}
			if d := Obj(s, "itemSectionRenderer", "contents", 0, "showingResultsForRenderer"); d != nil {
				t := Str(d, "correctedQueryEndpoint", "searchEndpoint", "query")
				out.SearchSuggestion, out.IsCorrected = &t, true
			}
		}
	}
	for _, x := range List(shelf, "contents") {
		r := Obj(x, "musicResponsiveListItemRenderer")
		if r == nil || Str(r, "musicItemRendererDisplayPolicy") == "MUSIC_ITEM_RENDERER_DISPLAY_POLICY_GREY_OUT" {
			continue
		}
		desc := List(r, "flexColumns", 1, "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
		switch searchType {
		case "music_songs", "music_videos":
			if v, ok := musicVideo(r, desc, searchType); ok {
				out.Items = append(out.Items, v)
			}
		case "music_artists":
			if ch, ok := musicArtist(r); ok {
				out.Channels = append(out.Channels, ch)
			}
		case "music_albums", "music_playlists":
			if pl, ok := musicPlaylist(r, desc, searchType); ok {
				out.Playlists = append(out.Playlists, pl)
			}
		}
	}
	if ct := Str(shelf, "continuations", 0, "nextContinuationData", "continuation"); ct != "" {
		b, _ := json.Marshal(musicPage{Continuation: ct, Type: searchType})
		n := "ytm1." + base64.RawURLEncoding.EncodeToString(b)
		out.NextPage = &n
	}
	return out, nil
}

func (c *Client) musicCall(ctx context.Context, endpoint string, body map[string]any) (map[string]any, error) {
	body["context"] = map[string]any{
		"client": map[string]any{"clientName": WebRemix.Name, "clientVersion": WebRemix.Version, "hl": "en-GB", "gl": "US",
			"platform": "DESKTOP", "utcOffsetMinutes": 0},
		"request": map[string]any{"internalExperimentFlags": []any{}, "useSsl": true},
		"user":    map[string]any{"lockedSafetyMode": false},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", WebRemix.UA)
	req.Header.Set("Origin", "https://music.youtube.com")
	req.Header.Set("Referer", "https://music.youtube.com")
	req.Header.Set("X-YouTube-Client-Name", strconv.Itoa(WebRemix.ID))
	req.Header.Set("X-YouTube-Client-Version", WebRemix.Version)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		c.noteStatus(res.StatusCode)
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, fmt.Errorf("%w %d on music search", ErrStatus, res.StatusCode)
	}
	var out map[string]any
	return out, json.NewDecoder(res.Body).Decode(&out)
}

func musicThumb(r map[string]any) string {
	return FixURL(Str(r, "thumbnail", "musicThumbnailRenderer", "thumbnail", "thumbnails", 0, "url"))
}

func musicTitle(r map[string]any) string {
	return Text(Dig(r, "flexColumns", 0, "musicResponsiveListItemFlexColumnRenderer", "text"))
}

// artistEndpoint is the menu entry with the ARTIST icon, which music videos and albums point their uploader at.
func artistEndpoint(r map[string]any) any {
	for _, it := range List(r, "menu", "menuRenderer", "items") {
		if Str(it, "menuNavigationItemRenderer", "icon", "iconType") == "ARTIST" {
			return Dig(it, "menuNavigationItemRenderer", "navigationEndpoint")
		}
	}
	return nil
}

// navURL is getUrlFromNavigationEndpoint for the endpoints music results carry.
func navURL(ep any) string {
	if ep == nil {
		return ""
	}
	if id := Str(ep, "browseEndpoint", "browseId"); strings.HasPrefix(id, "UC") {
		return ChannelURL(id)
	} else if strings.HasPrefix(id, "VL") {
		return "https://www.youtube.com/playlist?list=" + id[2:]
	}
	if u := Str(ep, "browseEndpoint", "canonicalBaseUrl"); u != "" {
		return "https://www.youtube.com" + u
	}
	return endpointURL(ep)
}

// musicVideo is YoutubeMusicSongOrVideoInfoItemExtractor.
func musicVideo(r map[string]any, desc []any, searchType string) (VideoItem, bool) {
	id, title := Str(r, "playlistItemData", "videoId"), musicTitle(r)
	if id == "" || title == "" || len(desc) == 0 {
		return VideoItem{}, false
	}
	u := "https://music.youtube.com/watch?v=" + id
	it := VideoItem{ID: u, Title: title, URL: u, ThumbnailURL: musicThumb(r), UploaderName: Str(desc[0], "text"),
		Duration: durationSeconds(Str(desc[len(desc)-1], "text")), ViewCount: -1, Uploaded: -1, StreamType: "video_stream"}
	if searchType == "music_videos" {
		it.UploaderURL = navURL(artistEndpoint(r))
		if len(desc) >= 3 {
			it.ViewCount, _ = MixedNumber(Str(desc[len(desc)-3], "text"))
		}
	} else {
		it.UploaderURL = navURL(Dig(desc[0], "navigationEndpoint"))
	}
	return it, true
}

// musicArtist is YoutubeMusicArtistInfoItemExtractor.
func musicArtist(r map[string]any) (ChannelResult, bool) {
	u, name := navURL(r["navigationEndpoint"]), musicTitle(r)
	if u == "" || name == "" {
		return ChannelResult{}, false
	}
	cols := List(r, "flexColumns")
	if len(cols) == 0 {
		return ChannelResult{}, false
	}
	runs := List(cols[len(cols)-1], "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
	var subs int64
	if len(runs) > 0 {
		subs, _ = MixedNumber(Str(runs[len(runs)-1], "text"))
	}
	return ChannelResult{ID: u, Name: name, URL: u, ThumbnailURL: musicThumb(r), SubscriberCount: subs, StreamCount: -1, IsVerified: true}, true
}

// musicPlaylist is YoutubeMusicAlbumOrPlaylistInfoItemExtractor.
func musicPlaylist(r map[string]any, desc []any, searchType string) (PlaylistResult, bool) {
	id := Str(r, "menu", "menuRenderer", "items", 4, "toggleMenuServiceItemRenderer", "toggledServiceEndpoint", "likeEndpoint", "target", "playlistId")
	if id == "" {
		id = Str(r, "overlay", "musicItemThumbnailOverlayRenderer", "content", "musicPlayButtonRenderer", "playNavigationEndpoint", "watchPlaylistEndpoint", "playlistId")
	}
	title := musicTitle(r)
	if id == "" || title == "" {
		return PlaylistResult{}, false
	}
	u := "https://music.youtube.com/playlist?list=" + id
	pl := PlaylistResult{ID: u, Title: title, URL: u, ThumbnailURL: musicThumb(r), StreamCount: -1, PlaylistType: "normal"}
	if searchType == "music_albums" {
		if len(desc) > 2 {
			pl.UploaderName = Str(desc[2], "text")
		}
		return pl, true
	}
	if len(desc) > 0 {
		pl.UploaderName = Str(desc[0], "text")
	}
	if len(desc) > 2 {
		if count := Str(desc[2], "text"); strings.Contains(count, "100+") {
			pl.StreamCount = -3
		} else {
			pl.StreamCount = Digits(count)
		}
	}
	return pl, true
}
