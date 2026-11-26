package sabr

// MemoryStats is what the store holds: live sessions, prepared ones still waiting for a create, the media bytes of
// all sessions, the bytes in media mappings and in the spare mappings kept for reuse.
type MemoryStats struct {
	Sessions, Prepared                    int
	SessionBytes, MappedBytes, SpareBytes int64
}

func (st *Store) MemoryStats() MemoryStats {
	st.mu.Lock()
	all := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		all = append(all, s)
	}
	out := MemoryStats{Sessions: len(all), Prepared: len(st.prewarm)}
	st.mu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		out.SessionBytes += s.bytes
		s.mu.Unlock()
	}
	out.MappedBytes = mappedBytes.Load()
	out.SpareBytes = spareMappedBytes()
	return out
}
