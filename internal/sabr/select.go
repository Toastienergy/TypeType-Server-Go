package sabr

import "strings"

// SelectVideo is SabrFormatSelector.video: the asked itag if it is a supported codec, else the tallest one.
func (in *Info) SelectVideo(itag int) *Format {
	var best *Format
	for i := range in.Formats {
		f := &in.Formats[i]
		if !supportedVideo(f) {
			continue
		}
		if itag != 0 {
			if f.Itag == itag {
				return f
			}
			continue
		}
		if best == nil || f.Height > best.Height {
			best = f
		}
	}
	return best
}

// LightestVideo is the smallest supported video, the one an audio-only session still names.
func (in *Info) LightestVideo() *Format {
	var best *Format
	for i := range in.Formats {
		f := &in.Formats[i]
		if supportedVideo(f) && (best == nil || f.Height < best.Height || f.Height == best.Height && f.Bitrate < best.Bitrate) {
			best = f
		}
	}
	return best
}

func supportedVideo(f *Format) bool {
	if !f.IsVideo() {
		return false
	}
	m := strings.ToLower(f.MimeType)
	return strings.Contains(m, "avc1") || strings.Contains(m, "vp9") || strings.Contains(m, "vp09") || strings.Contains(m, "av01")
}

func isAAC(f *Format) bool {
	return f.IsAudio() && strings.Contains(f.MimeType, "mp4") && strings.Contains(f.MimeType, "mp4a")
}

// audioBetter orders like the Kotlin comparator: original language, then no xtags, then not DRC, then bitrate.
func audioBetter(a, b *Format) bool {
	if a.IsOriginal() != b.IsOriginal() {
		return a.IsOriginal()
	}
	if (a.Xtags == "") != (b.Xtags == "") {
		return a.Xtags == ""
	}
	if a.IsDrc != b.IsDrc {
		return !a.IsDrc
	}
	return a.Bitrate > b.Bitrate
}

func trackMatches(f *Format, trackID string) bool {
	if trackID == "" {
		return true
	}
	id := f.AudioTrackID()
	return id != nil && *id == trackID
}

// SelectAudio is SabrFormatSelector.audio with requireAac false.
func (in *Info) SelectAudio(itag int, trackID string) *Format {
	pick := func(ok func(*Format) bool) *Format {
		var best *Format
		for i := range in.Formats {
			f := &in.Formats[i]
			if ok(f) && (best == nil || audioBetter(f, best)) {
				best = f
			}
		}
		return best
	}
	if itag != 0 {
		return pick(func(f *Format) bool { return f.IsAudio() && f.Itag == itag && trackMatches(f, trackID) })
	}
	if f := pick(func(f *Format) bool { return isAAC(f) && trackMatches(f, trackID) }); f != nil {
		return f
	}
	return pick(func(f *Format) bool { return f.IsAudio() && !f.IsDrc })
}
