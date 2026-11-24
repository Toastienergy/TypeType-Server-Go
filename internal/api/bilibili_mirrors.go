package api

import (
	"net/url"
	"os"
	"regexp"
	"strings"
)

var uposHost = regexp.MustCompile(`^upos-[a-z0-9-]+\.(bilivideo\.com|akamaized\.net)$`)

var (
	defaultUpos  = []string{"upos-sz-mirrorcos.bilivideo.com", "upos-sz-mirrorali.bilivideo.com", "upos-sz-mirrorhw.bilivideo.com", "upos-hz-mirrorakam.akamaized.net"}
	overseasUpos = []string{"upos-sz-mirroraliov.bilivideo.com", "upos-sz-mirrorcosov.bilivideo.com"}
	domesticUpos = []string{"upos-sz-mirrorali.bilivideo.com", "upos-sz-mirrorhw.bilivideo.com", "upos-sz-mirrorcos.bilivideo.com"}
)

func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// akamaiBlacklisted is BilibiliAkamaiConfig: some networks cannot reach Bilibili's Akamai mirror.
func akamaiBlacklisted() bool { return envTrue("BILIBILI_BLACKLIST_AKAMAI_CDN") }

func isAkamaiURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".akamaized.net")
}

// bilibiliRegion is BilibiliCdnConfig.defaultRegion.
func bilibiliRegion() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BILIBILI_CDN_REGION"))) {
	case "overseas", "oversea", "ov", "foreign":
		return "overseas"
	case "domestic", "cn", "china", "mainland":
		return "domestic"
	}
	return ""
}

// uposMirrors is bilibiliUposMirrorAlternates: the same file on the other upos mirrors, in the region's order.
func uposMirrors(raw string, akamaiBlocked bool, region string) []string {
	u, err := url.Parse(raw)
	if err != nil || !uposHost.MatchString(strings.ToLower(u.Hostname())) {
		return nil
	}
	akam := "upos-hz-mirrorakam.akamaized.net"
	order := defaultUpos
	switch region {
	case "domestic":
		order = append(append(slicesClone(domesticUpos), overseasUpos...), akam)
	case "overseas":
		order = append(append(slicesClone(overseasUpos), domesticUpos...), akam)
	}
	var out []string
	for _, h := range order {
		if h == strings.ToLower(u.Hostname()) || akamaiBlocked && strings.HasSuffix(h, ".akamaized.net") {
			continue
		}
		c := *u
		c.Host = h
		out = append(out, c.String())
	}
	return out
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }
