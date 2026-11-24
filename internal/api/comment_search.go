package api

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"

	"typetype.dev/server/internal/httpx"
	"typetype.dev/server/internal/youtube"
)

var (
	searchToken = regexp.MustCompile(`"([^"]+)"|(\S+)`)
	spaceRun    = regexp.MustCompile(`\s+`)
	// commentSearches is the Kotlin semaphore of four: a search walks up to twenty pages upstream.
	commentSearches = make(chan struct{}, 4)
)

// normalizeSearch is CommentSearchQuery.normalize: NFKC, every capital or dotless I folded to i, lower case,
// whitespace runs collapsed.
func normalizeSearch(v string) string {
	v = norm.NFKC.String(v)
	v = strings.NewReplacer("İ", "i", "I", "i", "ı", "i").Replace(v)
	v = strings.ReplaceAll(strings.ToLower(v), "i̇", "i")
	return spaceRun.ReplaceAllString(v, " ")
}

// parseCommentQuery is CommentSearchQuery.parse: up to 12 distinct terms, a quoted phrase counting as one.
func parseCommentQuery(raw string) []string {
	text := strings.TrimSpace(raw)
	if text == "" || len(utf16.Encode([]rune(text))) > 200 {
		return nil
	}
	var terms []string
	for _, m := range searchToken.FindAllStringSubmatch(text, -1) {
		t := m[0]
		if m[1] != "" {
			t = m[1]
		}
		t = normalizeSearch(strings.TrimSpace(t))
		if strings.TrimSpace(t) != "" && !slices.Contains(terms, t) && len(terms) < 12 {
			terms = append(terms, t)
		}
	}
	return terms
}

func commentMatches(c youtube.Comment, terms []string) bool {
	hay := normalizeSearch(c.Author + "\n" + c.Text)
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// commentSearch is /comments/search, CommentSearchService: it reads pages from the cursor until 20 matches or 20
// pages, and answers the matches with the cursor to resume from.
func (s *Server) commentSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.publicAccess(w, r); !ok {
		return
	}
	q := r.URL.Query()
	if !q.Has("url") {
		plain(w, r, http.StatusBadRequest, "Missing 'url' parameter")
		return
	}
	terms := parseCommentQuery(q.Get("q"))
	if terms == nil {
		plain(w, r, http.StatusBadRequest, "Invalid 'q' parameter")
		return
	}
	select {
	case commentSearches <- struct{}{}:
		defer func() { <-commentSearches }()
	case <-r.Context().Done():
		return
	}
	raw, page := q.Get("url"), q.Get("cursor")
	matches := []youtube.Comment{}
	var next *string
	if page != "" {
		next = &page
	}
	scanned, disabled := 0, false
	for scanned < 20 && len(matches) < 20 {
		cur := ""
		if next != nil {
			cur = *next
		}
		res, cerr := s.commentPage(r.Context(), raw, cur)
		if cerr != nil {
			if scanned == 0 {
				cerr.write(w, r)
				return
			}
			break
		}
		scanned++
		disabled = res.CommentsDisabled
		for _, c := range res.Comments {
			if commentMatches(c, terms) {
				matches = append(matches, c)
			}
		}
		next = res.NextPage
		if next == nil {
			break
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"comments": matches, "nextpage": next, "scannedPages": scanned, "commentsDisabled": disabled})
}
