package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Hashes use password4j's PHC layout with the server's psw4j.properties: argon2id, v=19, m=15360, t=2, p=1,
// 32-byte hash, 64-byte salt. Both servers verify each other's hashes.
const (
	argonMemory  = 15360
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 64
)

// hashSlots caps concurrent hashing at two, like the Kotlin password dispatcher: each hash holds 15 MiB.
var hashSlots = make(chan struct{}, 2)

func acquire(ctx context.Context) error {
	select {
	case hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func release() { <-hashSlots }

func HashPassword(ctx context.Context, password string) (string, error) {
	if err := acquire(ctx); err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, argonSaltLen)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword accepts any argon2 PHC hash the parameters of which it can read; anything else (profile
// placeholders, empty hashes) never matches.
func CheckPassword(ctx context.Context, password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[2] != "v=19" {
		return false
	}
	var m, t, p uint64
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return false
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		}
	}
	salt, err1 := decodeB64(parts[4])
	want, err2 := decodeB64(parts[5])
	if err1 != nil || err2 != nil || m == 0 || t == 0 || p == 0 || m > 1<<20 || len(want) == 0 {
		return false
	}
	if err := acquire(ctx); err != nil {
		return false
	}
	defer release()
	var got []byte
	switch parts[1] {
	case "argon2id":
		got = argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	case "argon2i":
		got = argon2.Key([]byte(password), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	default:
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func decodeB64(s string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}
