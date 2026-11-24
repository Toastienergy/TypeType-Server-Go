package api

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	switchRangeRe = regexp.MustCompile(`^bytes=(\d+)-(\d*)$`)
	md5HexRe      = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	multipartRe   = regexp.MustCompile(`^[0-9a-fA-F]{32}-\d+$`)
)

const (
	switchMinDelivered = 64 << 10
	switchMinRemaining = 32 << 10
	switchSample       = 32 << 10
	switchMinWait      = 1500 * time.Millisecond
	switchMaxProbe     = 4 * time.Second
	switchSlowBps      = 384 * 1024.0
	switchMinAltBps    = 128 * 1024.0
	switchSpeedup      = 2.0
)

func strictETag(v string) bool { return strongETag(v) && !strings.HasPrefix(v, "W/") }

// compatibleETag is isCompatibleEtag: the same tag, or a multipart upload tag on one side; two different MD5 tags
// are two different files.
func compatibleETag(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	ca, cb := strings.Trim(a, `"`), strings.Trim(b, `"`)
	if md5HexRe.MatchString(ca) && md5HexRe.MatchString(cb) {
		return false
	}
	return multipartRe.MatchString(ca) || multipartRe.MatchString(cb)
}

// maybeSwitch is BilibiliSlowMirrorSwitcher.maybeSwitch: once, when the current mirror delivers under 384 KiB/s after
// 1.5 s of waiting, it asks the other mirrors for the rest of the range and moves to one at least twice as fast.
func (b *retryingBody) maybeSwitch() {
	if b.switchChecked || len(b.resumeURLs) == 0 {
		return
	}
	m := switchRangeRe.FindStringSubmatch(b.rng)
	media, mok := parseContentRange(b.res.Header.Get("Content-Range"))
	etag := b.res.Header.Get("ETag")
	var start, end int64 = -1, -1
	if m != nil {
		start, _ = strconv.ParseInt(m[1], 10, 64)
		if m[2] != "" {
			end, _ = strconv.ParseInt(m[2], 10, 64)
		}
	}
	if m == nil || end >= 0 && end < start || !mok || media.total < 0 || media.end < media.start || media.total <= media.end || !strictETag(etag) ||
		b.res.StatusCode != http.StatusPartialContent || media.start != start || end >= 0 && end != media.end ||
		b.res.ContentLength != media.end-media.start+1 {
		b.switchChecked = true
		return
	}
	if b.delivered < switchMinDelivered || b.wait < switchMinWait {
		return
	}
	primaryBps := float64(b.delivered) * 1e9 / float64(b.wait.Nanoseconds())
	if primaryBps >= switchSlowBps {
		return
	}
	mirrorTracker.throughput(b.cur, primaryBps)
	offset := media.start + b.delivered
	b.switchChecked = true
	if offset > media.end || media.end-offset+1 < switchMinRemaining {
		return
	}
	select {
	case mirrorPermits <- struct{}{}:
		defer func() { <-mirrorPermits }()
	default:
		return
	}
	suffix := "bytes=" + strconv.FormatInt(offset, 10) + "-"
	if end >= 0 {
		suffix += strconv.FormatInt(end, 10)
	}
	want := contentRange{offset, media.end, media.total}
	length := media.end - offset + 1
	began := time.Now()
	for _, alt := range b.resumeURLs {
		if b.ctx.Err() != nil || time.Since(began) > switchMaxProbe {
			return
		}
		req := bilibiliRequest(b.ctx, alt, suffix)
		req.Header.Set("If-Range", etag)
		res, err := mirrorClient.Do(req)
		if err == nil && res.StatusCode == http.StatusOK {
			res.Body.Close()
			res, err = mirrorClient.Do(bilibiliRequest(b.ctx, alt, suffix))
		}
		if err != nil {
			mirrorTracker.failure(alt)
			continue
		}
		if time.Since(began) > switchMaxProbe {
			res.Body.Close()
			return
		}
		got, ok := parseContentRange(res.Header.Get("Content-Range"))
		oe := res.Header.Get("ETag")
		if res.StatusCode != http.StatusPartialContent || res.Request.URL.EscapedPath() != b.res.Request.URL.EscapedPath() || !ok || got != want ||
			!strictETag(oe) || !compatibleETag(etag, oe) || res.Header.Get("Content-Type") != b.res.Header.Get("Content-Type") ||
			res.Header.Get("Content-Encoding") != b.res.Header.Get("Content-Encoding") || res.ContentLength != length {
			res.Body.Close()
			mirrorTracker.failure(alt)
			continue
		}
		sample := make([]byte, min(int64(switchSample), length))
		sampled := time.Now()
		if _, err := io.ReadFull(res.Body, sample); err != nil {
			res.Body.Close()
			mirrorTracker.failure(alt)
			continue
		}
		if time.Since(began) > switchMaxProbe {
			res.Body.Close()
			return
		}
		took := max(time.Since(sampled), time.Nanosecond)
		altBps := float64(len(sample)) * 1e9 / float64(took.Nanoseconds())
		mirrorTracker.throughput(alt, altBps)
		if altBps < switchMinAltBps || altBps < primaryBps*switchSpeedup {
			res.Body.Close()
			continue
		}
		mirrorTracker.success(alt, took.Milliseconds())
		b.res.Body.Close()
		b.res, b.src, b.cur = res, io.MultiReader(bytes.NewReader(sample), res.Body), alt
		return
	}
}
