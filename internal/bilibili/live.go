package bilibili

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

var (
	ErrLiveNotStarted = errors.New("Live is not started.")
	ErrRoundPlay      = errors.New("bilibili video unavailable: the room replays past videos")
)

// Live is BillibiliStreamExtractor's live branch: the room, and its HLS master playlist (every quality ladder) or,
// for the few rooms without one, the single FLV stream.
type Live struct {
	RoomID                                    int64
	Title, Uploader, Face, Cover, Description string
	UID, Online                               int64
	HLS, FLV                                  string
}

func IsLiveURL(raw string) bool { return strings.Contains(strings.ToLower(raw), "live.bilibili.com") }

func liveRoomParam(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	seg := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(seg) == 0 {
		return ""
	}
	if _, err := strconv.ParseInt(seg[0], 10, 64); err != nil {
		return ""
	}
	return seg[0]
}

func (c *Client) Live(ctx context.Context, rawURL string) (*Live, error) {
	room := liveRoomParam(rawURL)
	if room == "" {
		return nil, fmt.Errorf("%w: no live room", ErrUnavailable)
	}
	if init, err := c.Get(ctx, "https://api.live.bilibili.com/room/v1/Room/room_init?id="+room, rawURL); err == nil {
		if id := num(obj(init, "data")["room_id"]); id > 0 {
			room = strconv.FormatInt(id, 10)
		}
	}
	base, err := c.Get(ctx, "https://api.live.bilibili.com/xlive/web-room/v1/index/getRoomBaseInfo?room_ids="+room+"&req_biz=web_room_componet", rawURL)
	if err != nil {
		return nil, err
	}
	rooms := obj(base, "data", "by_room_ids")
	d := obj(rooms, room)
	if d == nil && len(rooms) == 1 {
		for _, v := range rooms {
			d, _ = v.(map[string]any)
		}
	}
	if num(base["code"]) != 0 || len(d) == 0 {
		return nil, fmt.Errorf("%w: can not get live room info: %s", ErrUnavailable, str(base, "message"))
	}
	cover := str(d, "cover_from_user")
	if _, ok := d["cover_from_user"]; !ok {
		cover = str(d, "cover")
	}
	face := str(d, "face")
	if _, ok := d["face"]; !ok {
		face = str(d, "cover")
	}
	l := &Live{RoomID: num(d["room_id"]), Title: str(d, "title"), Uploader: str(d, "uname"), Description: str(d, "description"),
		UID: num(d["uid"]), Online: num(d["online"]), Cover: strings.ReplaceAll(cover, "http:", "https:"), Face: strings.ReplaceAll(face, "http:", "https:")}
	switch num(d["live_status"]) {
	case 0:
		return nil, ErrLiveNotStarted
	case 2:
		return nil, ErrRoundPlay
	}
	c.livePlayback(ctx, l, rawURL)
	if l.HLS == "" && l.FLV == "" {
		return nil, fmt.Errorf("%w: the room publishes no stream now", ErrUnavailable)
	}
	return l, nil
}

// livePlayback is fetchLivePlaybackUrls: one getRoomPlayInfo for the fmp4 HLS master playlist and the FLV fallback.
func (c *Client) livePlayback(ctx context.Context, l *Live, rawURL string) {
	play, err := c.Get(ctx, "https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo?room_id="+strconv.FormatInt(l.RoomID, 10)+
		"&protocol=0,1&format=0,1,2&codec=0,1&qn=10000&platform=web&dm_disabled=1", rawURL)
	if err != nil {
		return
	}
	for _, p := range arr(obj(play, "data", "playurl_info", "playurl"), "stream") {
		pm, _ := p.(map[string]any)
		hls, stream := str(pm, "protocol_name") == "http_hls", str(pm, "protocol_name") == "http_stream"
		for _, f := range arr(pm, "format") {
			fm, _ := f.(map[string]any)
			switch name := str(fm, "format_name"); {
			case hls && name == "fmp4" && l.HLS == "":
				l.HLS = str(fm, "master_url")
			case stream && name == "flv" && l.FLV == "":
				l.FLV = pickLiveFLV(fm)
			}
		}
	}
}

func pickLiveFLV(format map[string]any) string {
	hevc := ""
	for _, c := range arr(format, "codec") {
		cm, _ := c.(map[string]any)
		mirrors := arr(cm, "url_info")
		base := str(cm, "base_url")
		if base == "" || len(mirrors) == 0 {
			continue
		}
		m, _ := mirrors[0].(map[string]any)
		u := str(m, "host") + base + str(m, "extra")
		if str(cm, "codec_name") == "avc" {
			return u
		}
		if hevc == "" {
			hevc = u
		}
	}
	return hevc
}
