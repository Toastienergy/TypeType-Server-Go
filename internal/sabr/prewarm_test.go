package sabr

import (
	"context"
	"testing"
	"time"
)

func TestPeekFindsThePreparedSessionBeforeAndAfterTheTake(t *testing.T) {
	st := NewStore(Options{MaxSessions: 4})
	s := &Session{ID: "s1", store: st}
	st.sessions[s.ID] = s
	st.Remember("u|v|136|140||0|false", s, 0)
	if got, _ := st.PeekPrewarmed("u|v|136|140||0|false"); got != s {
		t.Fatal("peek did not find the prewarmed session")
	}
	if got, _ := st.TakePrewarmed("u|v|136|140||0|false"); got != s {
		t.Fatal("peek took the session away from the create")
	}
	if got, _ := st.PeekPrewarmed("u|v|136|140||0|false"); got != s {
		t.Fatal("a prewarm arriving after the take opens a second session")
	}
	if got, _ := st.TakePrewarmed("u|v|136|140||0|false"); got != nil {
		t.Fatal("a session was taken twice")
	}
}

func preparedSession(st *Store, id, key string, idle, age time.Duration) *Session {
	_, cancel := context.WithCancel(context.Background())
	s := &Session{ID: id, store: st, cancel: cancel, changed: make(chan struct{}), lastUsed: time.Now().Add(-idle)}
	st.sessions[s.ID] = s
	st.prewarm[key] = prewarmed{id: id, at: time.Now().Add(-age)}
	return s
}

func TestSweepReleasesAPreparedSessionNobodyTook(t *testing.T) {
	st := NewStore(Options{MaxSessions: 8, Idle: 4 * time.Minute})
	preparedSession(st, "abandoned", "u|a", 90*time.Second, 90*time.Second)
	st.sweepOnce()
	if st.Lookup("abandoned") != nil {
		t.Fatal("a prepared session nobody took stayed for the full idle eviction")
	}
}

func TestSweepKeepsATakenOrUsedPreparedSession(t *testing.T) {
	st := NewStore(Options{MaxSessions: 8, Idle: 4 * time.Minute})
	preparedSession(st, "clicked", "u|c", 90*time.Second, 50*time.Second)
	if got, _ := st.TakePrewarmed("u|c"); got == nil {
		t.Fatal("take failed")
	}
	preparedSession(st, "playing", "u|p", 5*time.Second, 90*time.Second)
	preparedSession(st, "fresh", "u|f", 20*time.Second, 20*time.Second)
	st.sweepOnce()
	for _, id := range []string{"clicked", "playing", "fresh"} {
		if st.Lookup(id) == nil {
			t.Fatalf("%s was released", id)
		}
	}
}
