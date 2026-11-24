package config

import (
	"os"
	"strings"
)

var placeholders = map[string]bool{
	"SET_ME_SHARED_SECRET": true, "SET_ME_YOUTUBE_REMOTE_LOGIN_INTERNAL_TOKEN": true,
	"SET_ME_YOUTUBE_SESSION_ENCRYPTION_KEY": true, "SET_ME_RECOMMENDER_INTERNAL_TOKEN": true,
	"replace-with-shared-internal-token": true, "replace-with-at-least-32-random-characters": true,
}

// Secret reads NAME, else the file named by NAME_FILE, ignoring the placeholders shipped in the example
// compose files, like SecretConfigReader.
func Secret(name string) string {
	if v := envText(os.Getenv(name)); v != "" {
		return v
	}
	path := envText(os.Getenv(name + "_FILE"))
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func envText(v string) string {
	v = strings.TrimSpace(v)
	if placeholders[v] {
		return ""
	}
	return v
}

// TrimmedURL is an optional service URL without trailing slashes, "" when unset.
func TrimmedURL(name string) string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/")
}

// Nonblank returns the first of the variables that is set to something other than blanks.
func Nonblank(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
