package sabr

import "testing"

func TestPeekLeavesThePrewarmedSessionForTheCreate(t *testing.T) {
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
	if got, _ := st.PeekPrewarmed("u|v|136|140||0|false"); got != nil {
		t.Fatal("a taken session is still offered")
	}
}
