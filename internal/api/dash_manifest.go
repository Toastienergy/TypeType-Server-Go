package api

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"typetype.dev/server/internal/httpx"
)

var (
	heightRe = regexp.MustCompile(`(\d+)[pP]`)
	bwRe     = regexp.MustCompile(`[?&]bw=(\d+)`)
)

func (s *Server) dashManifestRoutes(rt *Router) {
	rt.Handle("GET", "/streams/manifest", s.dashManifest)
	rt.Handle("GET", "/streams/native-manifest", s.dashManifest)
}

// dashManifest is ManifestService: a static MPD over the direct streams of a video, H.264 up to 1080p and every
// audio track, each pointing at its media handle with its index ranges. YouTube plays through SABR and has no
// direct streams, so this serves the providers. The native manifest was the same MPD built from PipePipe's own
// objects; with one extractor both are this one.
func (s *Server) dashManifest(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	resp, fail := s.streamFor(r, raw)
	if fail != nil {
		relay(w, fail)
		return
	}
	mpd, ok := buildMPD(resp)
	if !ok {
		httpx.Error(w, r, http.StatusUnprocessableEntity, "No compatible streams found for DASH manifest", "no_playable_streams")
		return
	}
	w.Header().Set("Content-Type", "application/dash+xml")
	w.Write([]byte(mpd))
}

func streamHeight(height int, resolution string) int {
	if height > 0 {
		return height
	}
	if m := heightRe.FindStringSubmatch(resolution); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func urlBandwidth(u string) int64 {
	if m := bwRe.FindStringSubmatch(u); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return n
	}
	return 0
}

func manifestMediaURL(u string) string {
	if strings.HasPrefix(u, "/media/") {
		return ".." + u
	}
	return "../proxy?url=" + url.QueryEscape(u)
}

func codecFamily(c string) string {
	switch {
	case strings.HasPrefix(c, "avc1"):
		return "avc"
	case strings.HasPrefix(c, "vp9"), strings.HasPrefix(c, "vp09"):
		return "vp9"
	case strings.HasPrefix(c, "av01"):
		return "av1"
	}
	return "other"
}

func mimeFor(kind, format string) string {
	if strings.Contains(strings.ToLower(format), "webm") {
		return kind + "/webm"
	}
	return kind + "/mp4"
}

func representationID(prefix string, itag, index int, track *string) string {
	n := index
	if itag > 0 {
		n = itag
	}
	id := prefix + strconv.Itoa(n)
	if track != nil {
		var b strings.Builder
		for _, c := range *track {
			if c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c > 127 {
				b.WriteRune(c)
			}
		}
		if b.Len() > 0 {
			id += "-" + b.String()
		}
	}
	return id
}

func segmentBase(b *strings.Builder, initStart, initEnd, indexStart, indexEnd int64) {
	if indexStart > 0 && indexEnd > 0 {
		fmt.Fprintf(b, "        <SegmentBase indexRange=\"%d-%d\">\n          <Initialization range=\"%d-%d\"/>\n        </SegmentBase>\n",
			indexStart, indexEnd, initStart, initEnd)
	}
}

func buildMPD(resp *streamResponse) (string, bool) {
	var videos []videoStream
	for _, v := range resp.VideoOnlyStreams {
		h := streamHeight(v.Height, v.Resolution)
		if v.URL != "" && v.Codec != nil && strings.HasPrefix(*v.Codec, "avc1") && h <= 1080 {
			videos = append(videos, v)
		}
	}
	bw := func(v videoStream) int64 {
		if v.Bitrate != nil {
			return *v.Bitrate
		}
		return urlBandwidth(v.URL)
	}
	sort.SliceStable(videos, func(i, j int) bool { return bw(videos[i]) > bw(videos[j]) })
	var audios []audioStream
	for _, a := range resp.AudioStreams {
		if a.URL != "" && a.Codec != nil && *a.Codec != "" {
			audios = append(audios, a)
		}
	}
	pref := resp.PreferredDefaultAudioTrackID
	notPreferred := func(a audioStream) bool { return pref != nil && (a.AudioTrackID == nil || *a.AudioTrackID != *pref) }
	sort.SliceStable(audios, func(i, j int) bool {
		if pi, pj := notPreferred(audios[i]), notPreferred(audios[j]); pi != pj {
			return !pi
		}
		return deref(audios[i].Bitrate) > deref(audios[j].Bitrate)
	})
	if len(videos) == 0 && len(audios) == 0 {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<MPD xmlns=\"urn:mpeg:dash:schema:mpd:2011\"\n     profiles=\"urn:mpeg:dash:profile:full:2011\"\n     type=\"static\"\n     mediaPresentationDuration=\"PT%dS\"\n     minBufferTime=\"PT4S\">\n  <Period>\n", resp.Duration)
	type vkey struct{ mime, family string }
	var vorder []vkey
	vgroups := map[vkey][]videoStream{}
	for _, v := range videos {
		k := vkey{mimeFor("video", v.Format), codecFamily(deref(v.Codec))}
		if _, ok := vgroups[k]; !ok {
			vorder = append(vorder, k)
		}
		vgroups[k] = append(vgroups[k], v)
	}
	for _, k := range vorder {
		fmt.Fprintf(&b, "    <AdaptationSet mimeType=\"%s\" startWithSAP=\"1\">\n", k.mime)
		for i, v := range vgroups[k] {
			h := streamHeight(v.Height, v.Resolution)
			wd := v.Width
			if wd <= 0 && h > 0 {
				wd = h * 16 / 9
			}
			band := bw(v)
			if band == 0 {
				band = int64(h * 1000)
			}
			size := ""
			if wd > 0 && h > 0 {
				size = fmt.Sprintf(" width=\"%d\" height=\"%d\"", wd, h)
			}
			fmt.Fprintf(&b, "      <Representation id=\"%s\" bandwidth=\"%d\"%s codecs=\"%s\">\n        <BaseURL>%s</BaseURL>\n",
				representationID("v-", v.Itag, i, nil), max(band, 1), size, html.EscapeString(deref(v.Codec)), html.EscapeString(manifestMediaURL(v.URL)))
			segmentBase(&b, v.InitStart, v.InitEnd, v.IndexStart, v.IndexEnd)
			b.WriteString("      </Representation>\n")
		}
		b.WriteString("    </AdaptationSet>\n")
	}
	byTrack := false
	for _, a := range audios {
		byTrack = byTrack || a.AudioTrackID != nil
	}
	type akey struct{ track, mime string }
	var aorder []akey
	agroups := map[akey][]audioStream{}
	for _, a := range audios {
		k := akey{mime: mimeFor("audio", a.Format)}
		if byTrack {
			k.track = "-"
			if a.AudioTrackID != nil {
				k.track = *a.AudioTrackID
			}
		}
		if _, ok := agroups[k]; !ok {
			aorder = append(aorder, k)
		}
		agroups[k] = append(agroups[k], a)
	}
	for _, k := range aorder {
		g := agroups[k]
		attrs := ""
		if byTrack && g[0].AudioLocale != nil {
			attrs += " lang=\"" + html.EscapeString(*g[0].AudioLocale) + "\""
		}
		if byTrack && g[0].AudioTrackName != nil {
			attrs += " label=\"" + html.EscapeString(*g[0].AudioTrackName) + "\""
		}
		fmt.Fprintf(&b, "    <AdaptationSet mimeType=\"%s\"%s>\n", k.mime, attrs)
		for i, a := range g {
			br := int64(128)
			if a.Bitrate != nil {
				br = *a.Bitrate
			}
			c := deref(a.Codec)
			if c == "mp4a" {
				c = "mp4a.40.2"
			}
			fmt.Fprintf(&b, "      <Representation id=\"%s\" bandwidth=\"%d\" codecs=\"%s\">\n        <BaseURL>%s</BaseURL>\n",
				representationID("a-", a.Itag, i, a.AudioTrackID), max(br*1000, 1), html.EscapeString(c), html.EscapeString(manifestMediaURL(a.URL)))
			segmentBase(&b, a.InitStart, a.InitEnd, a.IndexStart, a.IndexEnd)
			b.WriteString("      </Representation>\n")
		}
		b.WriteString("    </AdaptationSet>\n")
	}
	b.WriteString("  </Period>\n</MPD>")
	return b.String(), true
}
