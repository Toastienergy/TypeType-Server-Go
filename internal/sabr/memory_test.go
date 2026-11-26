package sabr

import "testing"

func TestMemoryStatsCountsSessionsPreparedAndBytes(t *testing.T) {
	st := NewStore(Options{MaxSessions: 8})
	a := &Session{ID: "a", store: st, bytes: 3 << 20}
	b := &Session{ID: "b", store: st, bytes: 1 << 20}
	st.sessions[a.ID], st.sessions[b.ID] = a, b
	st.Remember("u|b", b, 0)
	m := st.MemoryStats()
	if m.Sessions != 2 || m.Prepared != 1 || m.SessionBytes != 4<<20 {
		t.Fatalf("stats = %+v", m)
	}
}
