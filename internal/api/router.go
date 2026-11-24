package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"typetype.dev/server/internal/httpx"
)

// Router is a segment trie with Ktor's precedence: a literal segment beats a parameter, which beats a tailcard,
// with backtracking when a more specific branch dead-ends. net/http.ServeMux refuses several Kotlin route pairs
// as ambiguous, so it cannot carry this table. Path values are set on the request (r.PathValue works).
//
// Every route of the Kotlin catalog exists from the start: the ones not written yet answer 501.
type Router struct {
	root    *node
	written map[string]bool
}

type node struct {
	static   map[string]*node
	param    *node
	suffix   string
	tail     *node
	handlers map[string]route
}

// route keeps the parameter names of its own Kotlin path: two routes may name the same position differently.
type route struct {
	h     http.HandlerFunc
	names []string
}

func NewRouter() *Router {
	return &Router{root: &node{}, written: map[string]bool{}}
}

var segParam = regexp.MustCompile(`^\{([A-Za-z]+)\}(\.[a-z]+)?$`)
var segTail = regexp.MustCompile(`^\{([A-Za-z]*)\.\.\.\}$`)

func (rt *Router) add(method, path string, h http.HandlerFunc) {
	if method == "WEBSOCKET" {
		method = "GET"
	}
	n := rt.root
	var names []string
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if m := segTail.FindStringSubmatch(seg); m != nil {
			if n.tail == nil {
				n.tail = &node{}
			}
			name := m[1]
			if name == "" {
				name = "rest"
			}
			names = append(names, name)
			n = n.tail
			break
		}
		if m := segParam.FindStringSubmatch(seg); m != nil {
			if n.param == nil {
				n.param = &node{}
				n.suffix = m[2]
			}
			names = append(names, m[1])
			n = n.param
			continue
		}
		if n.static == nil {
			n.static = map[string]*node{}
		}
		next := n.static[seg]
		if next == nil {
			next = &node{}
			n.static[seg] = next
		}
		n = next
	}
	if n.handlers == nil {
		n.handlers = map[string]route{}
	}
	n.handlers[method] = route{h: h, names: names}
	if method == "GET" {
		if _, ok := n.handlers["HEAD"]; !ok {
			n.handlers["HEAD"] = route{h: h, names: names}
		}
	}
}

// Handle registers a written route under its Kotlin method and path.
func (rt *Router) Handle(method, path string, h http.HandlerFunc) {
	rt.add(method, path, h)
	rt.written[method+" "+path] = true
}

type match struct {
	values []string
}

func (n *node) find(segs []string, m *match) *node {
	if len(segs) == 0 {
		if n.handlers != nil {
			return n
		}
		if n.tail != nil && n.tail.handlers != nil {
			m.values = append(m.values, "")
			return n.tail
		}
		return nil
	}
	seg := segs[0]
	if next := n.static[seg]; next != nil {
		if found := next.find(segs[1:], m); found != nil {
			return found
		}
	}
	if n.param != nil && seg != "" {
		value := seg
		if n.suffix == "" || strings.HasSuffix(seg, n.suffix) && len(seg) > len(n.suffix) {
			value = strings.TrimSuffix(seg, n.suffix)
			if v, err := url.PathUnescape(value); err == nil {
				value = v
			}
			mark := len(m.values)
			m.values = append(m.values, value)
			if found := n.param.find(segs[1:], m); found != nil {
				return found
			}
			m.values = m.values[:mark]
		}
	}
	if n.tail != nil && n.tail.handlers != nil {
		rest := strings.Join(segs, "/")
		if v, err := url.PathUnescape(rest); err == nil {
			rest = v
		}
		m.values = append(m.values, rest)
		return n.tail
	}
	return nil
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	stripAPIPrefix(r)
	raw := r.URL.EscapedPath()
	var m match
	n := rt.root.find(strings.Split(strings.TrimPrefix(raw, "/"), "/"), &m)
	if n == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rte, ok := n.handlers[r.Method]
	if !ok {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	for i, name := range rte.names {
		if i < len(m.values) {
			r.SetPathValue(name, m.values[i])
		}
	}
	rte.h(w, r)
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		userMemos.invalidate()
	}
}

// Handler fills every catalog route not written yet with the 501 answer.
func (rt *Router) Handler() http.Handler {
	for _, c := range catalog {
		if !rt.written[c[0]+" "+c[1]] {
			rt.add(c[0], c[1], notWritten)
		}
	}
	return rt
}

func notWritten(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotImplemented, "Not implemented yet in the Go server", "not_implemented")
}

// Written reports how many catalog routes have a real handler.
func (rt *Router) Written() (int, int) {
	n := 0
	seen := map[string]bool{}
	for _, c := range catalog {
		k := c[0] + " " + c[1]
		if seen[k] {
			continue
		}
		seen[k] = true
		if rt.written[k] {
			n++
		}
	}
	return n, len(seen)
}

// Missing lists the catalog routes still answered by notWritten.
func (rt *Router) Missing() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range catalog {
		k := c[0] + " " + c[1]
		if !seen[k] && !rt.written[k] {
			out = append(out, k)
		}
		seen[k] = true
	}
	return out
}

// stripAPIPrefix serves /api/... as /...: production puts the server behind a proxy that strips /api, and the
// server writes /api/... media and feed URLs for that layout. A client that reaches the port directly (a LAN test)
// finds the API under /api/ too, so those URLs stay inside the base it discovered.
func stripAPIPrefix(r *http.Request) {
	p := r.URL.Path
	if p != "/api" && !strings.HasPrefix(p, "/api/") {
		return
	}
	r.URL.Path = strings.TrimPrefix(p, "/api")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	if r.URL.RawPath != "" {
		r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/api")
	}
}
