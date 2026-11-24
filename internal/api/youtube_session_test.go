package api

import "testing"

func TestSessionCrypto(t *testing.T) {
	c := newSessionCrypto("0123456789abcdef0123456789abcdef", "typetype.youtube-session")
	enc := c.encrypt("SID=a; SAPISID=b")
	if got, err := c.decrypt(enc); err != nil || got != "SID=a; SAPISID=b" {
		t.Fatalf("round trip = %q %v", got, err)
	}
	other := newSessionCrypto("0123456789abcdef0123456789abcdef", "typetype.bilibili-session")
	if _, err := other.decrypt(enc); err == nil {
		t.Fatal("decrypted with another label")
	}
	if newSessionCrypto("short", "x") != nil {
		t.Fatal("short secret accepted")
	}
}

func TestNormalizeYoutubeCookies(t *testing.T) {
	netscape := "# Netscape HTTP Cookie File\n.google.com\tTRUE\t/\tTRUE\t0\tSID\tgoogle\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tyoutube\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\tapisid\n.example.com\tTRUE\t/\tTRUE\t0\tSID\tother\n.youtube.com\tTRUE\t/\tTRUE\t0\tJUNK\tx\n"
	if got := normalizeYoutubeCookies(netscape); got != "SID=youtube; SAPISID=apisid" {
		t.Fatalf("netscape = %q", got)
	}
	if got := normalizeYoutubeCookies("Cookie: SID=a; foo=b; SAPISID=c"); got != "SID=a; SAPISID=c" {
		t.Fatalf("header = %q", got)
	}
	if !validYoutubeCredentials("SID=a; SAPISID=c", "12345678", 0) || validYoutubeCredentials("SID=a", "12345678", 0) {
		t.Fatal("validator")
	}
}
