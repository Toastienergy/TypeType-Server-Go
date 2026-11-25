package api

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"typetype.dev/server/internal/httpx"
)

func TestStartupReportIsLoggedAsNumbers(t *testing.T) {
	var out bytes.Buffer
	s := &Server{Log: slog.New(slog.NewTextHandler(&out, nil)), zones: zones{userData: httpx.NewZone(100, time.Minute)}}
	rt := NewRouter()
	s.startupMetricsRoutes(rt)
	post := func(body string) int {
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, httptest.NewRequest("POST", "/metrics/startup", strings.NewReader(body)))
		return w.Code
	}
	ok := `{"video":"aqz-KE-bpKQ","origin":"click","firstFrameMs":412.6,"warmed":true,"trace":"3f2a-9c",` +
		`"steps":{"create":[150.2,189.9],"Bad Name":[1,2],"segment":[400,430]}}`
	if code := post(ok); code != 204 {
		t.Fatalf("valid report got %d", code)
	}
	line := out.String()
	for _, want := range []string{"msg=client_startup", "videoId=aqz-KE-bpKQ", "firstFrameMs=412", "create=\"[150 189]\"", "trace=3f2a-9c"} {
		if !strings.Contains(line, want) {
			t.Fatalf("%q missing in %s", want, line)
		}
	}
	if strings.Contains(line, "Bad Name") {
		t.Fatal("an unchecked step name was logged")
	}
	if code := post(`{"video":"<script>","firstFrameMs":1}`); code != 400 {
		t.Fatalf("invalid video id got %d", code)
	}
}
