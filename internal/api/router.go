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
	static    map[string]*node
	param     *node
	paramName string
	suffix    string
	tail      *node
	tailName  string
	handlers  map[string]http.HandlerFunc
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
	for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if m := segTail.FindStringSubmatch(seg); m != nil {
			if n.tail == nil {
				n.tail = &node{}
				n.tailName = m[1]
				if n.tailName == "" {
					n.tailName = "rest"
				}
			}
			n = n.tail
			break
		}
		if m := segParam.FindStringSubmatch(seg); m != nil {
			if n.param == nil {
				n.param = &node{}
				n.paramName, n.suffix = m[1], m[2]
			}
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
		n.handlers = map[string]http.HandlerFunc{}
	}
	n.handlers[method] = h
	if method == "GET" {
		if _, ok := n.handlers["HEAD"]; !ok {
			n.handlers["HEAD"] = h
		}
	}
}

// Handle registers a written route under its Kotlin method and path.
func (rt *Router) Handle(method, path string, h http.HandlerFunc) {
	rt.add(method, path, h)
	rt.written[method+" "+path] = true
}

type match struct {
	names, values []string
}

func (n *node) find(segs []string, m *match) *node {
	if len(segs) == 0 {
		if n.handlers != nil {
			return n
		}
		if n.tail != nil && n.tail.handlers != nil {
			m.names, m.values = append(m.names, n.tailName), append(m.values, "")
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
			mark := len(m.names)
			m.names, m.values = append(m.names, n.paramName), append(m.values, value)
			if found := n.param.find(segs[1:], m); found != nil {
				return found
			}
			m.names, m.values = m.names[:mark], m.values[:mark]
		}
	}
	if n.tail != nil && n.tail.handlers != nil {
		rest := strings.Join(segs, "/")
		if v, err := url.PathUnescape(rest); err == nil {
			rest = v
		}
		m.names, m.values = append(m.names, n.tailName), append(m.values, rest)
		return n.tail
	}
	return nil
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.EscapedPath()
	var m match
	n := rt.root.find(strings.Split(strings.TrimPrefix(raw, "/"), "/"), &m)
	if n == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h := n.handlers[r.Method]
	if h == nil {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	for i, name := range m.names {
		r.SetPathValue(name, m.values[i])
	}
	h(w, r)
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
