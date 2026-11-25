package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("test-secret-test-secret-test-secret")

func TestVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	good := Sign(testSecret, Claims{Subject: "u1", SessionID: "s1", Expires: now.Unix() + 60})
	if c, err := Verify(testSecret, good, now); err != nil || c.Subject != "u1" || c.SessionID != "s1" {
		t.Fatalf("valid token: %+v %v", c, err)
	}
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin","sid":"s1"}`)) + "." + parts[2]
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "." + parts[2]
	future := SignAny(testSecret, map[string]any{"sub": "u1", "nbf": now.Unix() + 30})
	for name, tok := range map[string]string{
		"expired": good, "wrong secret": Sign([]byte("other"), Claims{Subject: "u1"}), "tampered": tampered,
		"alg none": none, "not before": future, "garbage": "a.b", "empty": "",
	} {
		at := now
		if name == "expired" {
			at = now.Add(2 * time.Minute)
		}
		if _, err := Verify(testSecret, tok, at); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestServiceClaimsKeepsSignaturesNotTimes(t *testing.T) {
	s := &Service{secret: testSecret}
	now := time.Unix(1_800_000_000, 0)
	tok := Sign(testSecret, Claims{Subject: "u1", SessionID: "s1", Expires: now.Unix() + 60})
	if c, ok := s.claims(tok, now); !ok || c.Subject != "u1" {
		t.Fatal("valid token refused")
	}
	if _, ok := s.claims(tok, now.Add(2*time.Minute)); ok {
		t.Fatal("kept token accepted after it expired")
	}
	bad := tok[:len(tok)-2] + "xx"
	if _, ok := s.claims(bad, now); ok || len(s.signed) != 1 {
		t.Fatalf("bad signature accepted or kept: %d kept", len(s.signed))
	}
	for i := range signedTokens + 1 {
		s.claims(Sign(testSecret, Claims{Subject: "u" + string(rune('a'+i%26)), ID: string(rune(i)), Expires: now.Unix() + 60}), now)
	}
	if len(s.signed) > signedTokens {
		t.Fatalf("%d tokens kept", len(s.signed))
	}
}

func BenchmarkServiceClaims(b *testing.B) {
	s := &Service{secret: testSecret}
	now := time.Now()
	tok := Sign(testSecret, Claims{Subject: "user-0123456789", SessionID: "session-0123456789", Expires: now.Add(time.Hour).Unix()})
	b.ReportAllocs()
	for b.Loop() {
		s.claims(tok, now)
	}
}
