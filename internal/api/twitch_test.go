package api

import (
	"testing"

	"typetype.dev/server/internal/config"
)

func TestTwitchCryptoRoundTrip(t *testing.T) {
	t.Setenv("TWITCH_SERVICE_URL", "http://live")
	t.Setenv("TWITCH_SERVICE_SECRET", "s")
	t.Setenv("TWITCH_TOKEN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	_ = config.Secret
	tw := newTwitch()
	if tw == nil {
		t.Fatal("twitch disabled")
	}
	enc := tw.encrypt("token-123")
	if dec, err := tw.decrypt(enc); err != nil || dec != "token-123" {
		t.Fatalf("round trip: %q %v", dec, err)
	}
	if _, err := tw.decrypt("plain"); err == nil {
		t.Fatal("accepted a payload without prefix")
	}
}
