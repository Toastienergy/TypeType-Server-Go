package config

import "testing"

func TestYoutubeLoginUsesTheTokenServiceByDefault(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://example.test")
	t.Setenv("SUBTITLE_SERVICE_URL", "http://token:8081")
	t.Setenv("YOUTUBE_TOKEN_SERVICE_URL", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.YoutubeTokenURL != "http://token:8081" {
		t.Fatalf("login goes to %q", c.YoutubeTokenURL)
	}
	t.Setenv("YOUTUBE_TOKEN_SERVICE_URL", "http://other:9000")
	if c, _ = Load(); c.YoutubeTokenURL != "http://other:9000" {
		t.Fatalf("explicit URL ignored: %q", c.YoutubeTokenURL)
	}
}
