package db

import (
	"testing"
	"time"
)

func TestBreaker(t *testing.T) {
	b := &breaker{timeout: time.Second, probeEvery: 2 * time.Second}
	t0 := time.Unix(1000, 0)
	if b.before(t0) != nil {
		t.Fatal("first attempt refused")
	}
	b.after(t0.Add(10 * time.Millisecond))
	if b.before(t0.Add(time.Second)) != nil {
		t.Fatal("attempt after a success refused")
	}
	if b.before(t0.Add(1500*time.Millisecond)) != nil {
		t.Fatal("attempt while the previous one may still connect refused")
	}
	if b.before(t0.Add(3*time.Second)) != ErrDatabaseDown {
		t.Fatal("attempt right after a failed one allowed")
	}
	if b.before(t0.Add(5*time.Second)) != nil {
		t.Fatal("probe refused")
	}
	if b.before(t0.Add(6100*time.Millisecond)) != ErrDatabaseDown {
		t.Fatal("second attempt after a failed probe allowed")
	}
	b.after(t0.Add(8 * time.Second))
	if b.before(t0.Add(8100*time.Millisecond)) != nil {
		t.Fatal("attempt after recovery refused")
	}
}
