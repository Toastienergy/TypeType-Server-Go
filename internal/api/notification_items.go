package api

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// notificationMemo is the item list of one feed snapshot for one set of enabled channels. Building it parses the
// address and hashes the key of every video in the feed, and the web asks for the unread count on every page.
type notificationMemo struct {
	enabled string
	items   []notificationItem
}

// notificationItems returns the snapshot's items for these channels, newest first; they are shared, so callers copy
// before changing one.
func (f *feedSnapshot) notificationItems(enabled map[string]bool) []notificationItem {
	key := enabledKey(enabled)
	if m := f.notes.Load(); m != nil && m.enabled == key {
		return m.items
	}
	items := f.buildNotificationItems(enabled)
	f.notes.Store(&notificationMemo{enabled: key, items: items})
	return items
}

func enabledKey(enabled map[string]bool) string {
	keys := make([]string, 0, len(enabled))
	for k := range enabled {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, "\n")
}

func (f *feedSnapshot) buildNotificationItems(enabled map[string]bool) []notificationItem {
	items := []notificationItem{}
	if len(enabled) > 0 {
		seen := map[string]bool{}
		canon := map[string]string{}
		for _, v := range f.videos {
			sources := f.sources[feedKey(v)]
			if len(sources) == 0 {
				sources = []string{v.UploaderURL}
			}
			if !anyIn(sources, enabled, canon) {
				continue
			}
			created := publishedMillis(v)
			if created <= 0 {
				continue
			}
			sid := videoServiceID(v)
			channel := strings.TrimSpace(v.UploaderURL)
			if channel == "" {
				channel = strings.TrimSpace(v.UploaderAvatarURL)
			}
			if channel == "" {
				channel = strings.TrimSpace(v.UploaderName)
			}
			ident := strings.TrimSpace(v.URL)
			if ident == "" {
				ident = strings.TrimSpace(v.ID)
			}
			if ident == "" {
				ident = strings.TrimSpace(v.Title) + ":" + strconv.FormatInt(created, 10)
			}
			key := strconv.Itoa(sid) + "|" + channel + "|" + ident
			if seen[key] {
				continue
			}
			seen[key] = true
			sum := sha256.Sum256([]byte(key))
			items = append(items, notificationItem{ID: hex.EncodeToString(sum[:]), Type: "subscription_new_video",
				Title: v.UploaderName + " uploaded a new video", CreatedAt: created, PublishedAt: created, ChannelURL: v.UploaderURL,
				ChannelName: v.UploaderName, ChannelAvatarURL: v.UploaderAvatarURL, ServiceID: sid, ServiceName: serviceName(sid), Video: v})
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	}
	return items
}
