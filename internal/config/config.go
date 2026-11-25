package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config reads the same environment variables as the Kotlin server, with the same defaults, so an existing
// deployment switches binaries without touching its compose file.
type Config struct {
	Port             string
	DatabaseURL      string
	DatabaseUser     string
	DatabasePassword string
	DragonflyURL     string
	JWTSecret        string
	AllowedOrigins   map[string]bool
	AllowAnyOrigin   bool
	InstanceID       string
	PushEnabled      bool
	SubtitleService  string
	DownloaderURL    string
	YoutubeProxyURL  string
	YoutubeTokenURL  string
}

func Load() (*Config, error) {
	c := &Config{
		Port:             env("PORT", "8080"),
		DatabaseURL:      env("DATABASE_URL", "jdbc:postgresql://localhost:5432/typetype"),
		DatabaseUser:     env("DATABASE_USER", "typetype"),
		DatabasePassword: env("DATABASE_PASSWORD", "typetype"),
		DragonflyURL:     env("DRAGONFLY_URL", "redis://localhost:6379"),
		JWTSecret:        os.Getenv("JWT_SECRET"),
		InstanceID:       strings.TrimSpace(os.Getenv("TYPE_TYPE_INSTANCE_ID")),
		PushEnabled:      boolEnv("TYPE_TYPE_PUSH_NOTIFICATIONS_ENABLED", true),
		SubtitleService:  env("SUBTITLE_SERVICE_URL", "http://typetype-token:8081"),
		DownloaderURL:    env("DOWNLOADER_SERVICE_URL", "http://typetype-downloader:18093"),
		YoutubeProxyURL:  os.Getenv("YOUTUBE_OUTBOUND_PROXY_URL"),
		YoutubeTokenURL:  os.Getenv("YOUTUBE_TOKEN_SERVICE_URL"),
	}
	if c.InstanceID == "" {
		c.InstanceID = "typetype"
	}
	// The YouTube account login (device code, TV token) lives in the token service, like the Kotlin server's
	// TypetypeTokenYoutubeSessionClient on SUBTITLE_SERVICE_URL: without its own URL it is that one.
	if strings.TrimSpace(c.YoutubeTokenURL) == "" {
		c.YoutubeTokenURL = c.SubtitleService
	}
	if c.JWTSecret == "" {
		b := make([]byte, 16)
		rand.Read(b)
		c.JWTSecret = hex.EncodeToString(b)
	}
	c.AllowedOrigins = map[string]bool{}
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins[o] = true
		}
	}
	if len(c.AllowedOrigins) == 0 {
		return nil, errors.New("ALLOWED_ORIGINS environment variable must be set")
	}
	c.AllowAnyOrigin = c.AllowedOrigins["*"]
	return c, nil
}

// PostgresDSN turns the JDBC URL the Kotlin server takes into a libpq URL, keeping the separate user and password.
func (c *Config) PostgresDSN() (string, error) {
	raw := strings.TrimPrefix(c.DatabaseURL, "jdbc:")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgresql" && u.Scheme != "postgres") {
		return "", fmt.Errorf("DATABASE_URL is not a postgresql URL: %q", c.DatabaseURL)
	}
	q := u.Query()
	if user := q.Get("user"); user != "" {
		c.DatabaseUser = user
		q.Del("user")
	}
	if pw := q.Get("password"); pw != "" {
		c.DatabasePassword = pw
		q.Del("password")
	}
	if q.Get("sslmode") == "" {
		q.Set("sslmode", "disable")
	}
	u.RawQuery = q.Encode()
	u.User = url.UserPassword(c.DatabaseUser, c.DatabasePassword)
	return u.String(), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	switch os.Getenv(key) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

func IntEnv(key string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return def
	}
	return v
}
