package api

import (
	"net/http"
	"strings"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

// searchSelection is a resolved content filter and its sort filters.
type searchSelection struct {
	content      *filterItem
	contentGiven bool
	sorts        []*filterItem
}

// selectFilters resolves contentFilter, every filter and sortFilter like PipePipeSearchService, answering 400 with
// its messages.
func selectFilters(w http.ResponseWriter, r *http.Request, tree *filterTree) (searchSelection, bool) {
	q := r.URL.Query()
	content, msg := tree.resolveContent(q.Get("contentFilter"), q.Has("contentFilter"))
	if msg != "" {
		plain(w, r, http.StatusBadRequest, msg)
		return searchSelection{}, false
	}
	var values []string
	seen := map[string]bool{}
	for _, v := range append(q["filter"], q.Get("sortFilter")) {
		if strings.TrimSpace(v) != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	sorts, msg := resolveSort(tree.sortFor(content), values)
	if msg != "" {
		plain(w, r, http.StatusBadRequest, msg)
		return searchSelection{}, false
	}
	return searchSelection{content: content[0], contentGiven: q.Has("contentFilter"), sorts: sorts}, true
}

// extra is BilibiliFilters' sort query: "&order=...&duration=...".
func (s searchSelection) extra() string {
	var b strings.Builder
	for _, it := range s.sorts {
		if it.query != "" {
			b.WriteString("&" + it.query)
		}
	}
	return b.String()
}

// niconicoQuery is NiconicoFilters.evaluateSelectedFilters for videos: each sort with order=a when ascending is
// selected, order=d otherwise.
func (s searchSelection) niconicoQuery() string {
	order := "&order=d"
	for _, it := range s.sorts {
		if it.id == niconicoAscending {
			order = "&order=a"
		}
	}
	var parts []string
	for _, it := range s.sorts {
		if it.query == "" || it.id == niconicoAscending {
			continue
		}
		if strings.Contains(it.query, "sortKey") {
			parts = append(parts, it.query)
		} else {
			parts = append(parts, it.query+order)
		}
	}
	return strings.Join(parts, "&")
}

// kindFilter is SearchContentKind.filteredBy: a typed content filter keeps only its kind of results.
func kindFilter(page *youtube.SearchPage, name string) {
	switch name {
	case "channels", "music_artists":
		page.Items, page.Playlists = []youtube.VideoItem{}, []youtube.PlaylistResult{}
	case "playlists", "music_playlists", "music_albums":
		page.Items, page.Channels = []youtube.VideoItem{}, []youtube.ChannelResult{}
	case "videos", "lives", "music_songs", "music_videos", "animes", "movies_and_tv":
		page.Playlists, page.Channels = []youtube.PlaylistResult{}, []youtube.ChannelResult{}
	}
}

func (s *Server) searchFilters(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.publicAccess(w, r); !ok {
		return
	}
	svc, ok := serviceParam(w, r)
	if !ok {
		return
	}
	tree := filtersFor(svc)
	if tree == nil {
		notWritten(w, r)
		return
	}
	content, msg := tree.resolveContent(r.URL.Query().Get("contentFilter"), r.URL.Query().Has("contentFilter"))
	if msg != "" {
		plain(w, r, http.StatusBadRequest, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, tree.response(content))
}
