package auth

import (
	"testing"
	"time"
)

func BenchmarkVerify(b *testing.B) {
	secret := []byte("bench-secret-bench-secret-bench-secret")
	now := time.Now()
	token := Sign(secret, Claims{Subject: "user-0123456789", SessionID: "session-0123456789", Expires: now.Add(time.Hour).Unix()})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Verify(secret, token, now); err != nil {
			b.Fatal(err)
		}
	}
}
