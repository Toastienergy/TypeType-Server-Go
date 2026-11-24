package api

import (
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"typetype.dev/server/internal/db"
	"typetype.dev/server/internal/httpx"
)

// membershipPage is SubscriptionMembershipPageService: the subscriptions a group editor pages through, filtered by
// group, by absence from a group or from every group, and by name or URL.
func (s *Server) membershipRoutes(rt *Router) {
	rt.Handle("GET", "/subscriptions/group-memberships/page", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		q := r.URL.Query()
		bad := func() {
			httpx.Error(w, r, http.StatusBadRequest, "Invalid membership page filter", "subscription_group_invalid_filter")
		}
		page, limit := 0, 20
		var err error
		if q.Has("page") {
			if page, err = strconv.Atoi(q.Get("page")); err != nil {
				bad()
				return
			}
		}
		if q.Has("limit") {
			if limit, err = strconv.Atoi(q.Get("limit")); err != nil {
				bad()
				return
			}
		}
		flag := func(name string) (bool, bool) {
			if !q.Has(name) {
				return false, true
			}
			v := q.Get(name)
			return v == "true", v == "true" || v == "false"
		}
		ungrouped, ok1 := flag("ungrouped")
		excluded, ok2 := flag("excluded")
		search := strings.TrimSpace(q.Get("search"))
		group, hasGroup := q.Get("groupId"), q.Has("groupId")
		if !ok1 || !ok2 || page < 0 || page > 1_000_000 || limit < 1 || limit > 100 || len([]rune(search)) > 200 ||
			hasGroup && strings.TrimSpace(group) == "" || hasGroup && ungrouped || excluded && !hasGroup {
			bad()
			return
		}
		if hasGroup && !s.groupExists(r, user, group) {
			groupNotFound(w, r)
			return
		}
		all, err := s.subscriptions(r, user, "", false, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		lower := strings.ToLower(search)
		var matched []subscriptionItem
		ungroupedCount := 0
		for _, it := range all {
			gs := *it.GroupIDs
			if len(gs) == 0 {
				ungroupedCount++
			}
			in := hasGroup && contains(gs, group)
			switch {
			case ungrouped && len(gs) > 0, excluded && in, hasGroup && !excluded && !in:
				continue
			}
			if lower != "" && !strings.Contains(strings.ToLower(it.Name), lower) && !strings.Contains(strings.ToLower(it.ChannelURL), lower) {
				continue
			}
			sort.Strings(gs)
			matched = append(matched, it)
		}
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := strings.ToLower(matched[i].Name), strings.ToLower(matched[j].Name)
			if a != b {
				return a < b
			}
			return matched[i].ChannelURL < matched[j].ChannelURL
		})
		from := min(page*limit, len(matched))
		items := matched[from:min(from+limit, len(matched))]
		if items == nil {
			items = []subscriptionItem{}
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(matched), "totalSubscriptions": len(all),
			"ungroupedCount": ungroupedCount, "page": page, "limit": limit})
	}))
	rt.Handle("POST", "/subscriptions/group-memberships/lookup", s.authed(func(w http.ResponseWriter, r *http.Request, user string) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		urls, ok := membershipChannels(w, r, body)
		if !ok {
			return
		}
		want := map[string]bool{}
		for _, u := range urls {
			want[db.CanonicalChannelURL(u)] = true
		}
		all, err := s.subscriptions(r, user, "", false, true)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out := []subscriptionItem{}
		for _, it := range all {
			if want[it.ChannelURL] {
				sort.Strings(*it.GroupIDs)
				out = append(out, it)
			}
		}
		httpx.JSON(w, http.StatusOK, out)
	}))
}
