package api

import (
	"net/http/httptest"
	"testing"
)

func TestStripAPIPrefix(t *testing.T) {
	for in, want := range map[string]string{"/api/instance": "/instance", "/api": "/", "/apix/a": "/apix/a", "/instance": "/instance",
		"/api/proxy/a%2Fb": "/proxy/a/b"} {
		r := httptest.NewRequest("GET", in, nil)
		stripAPIPrefix(r)
		if r.URL.Path != want {
			t.Errorf("%s -> %s, want %s", in, r.URL.Path, want)
		}
	}
	r := httptest.NewRequest("GET", "/api/proxy/a%2Fb", nil)
	stripAPIPrefix(r)
	if r.URL.EscapedPath() != "/proxy/a%2Fb" {
		t.Errorf("escaped %s", r.URL.EscapedPath())
	}
}
