package api

import (
	"runtime"
	"time"
)

// logSabrMemory writes once a minute what the SABR store holds next to the Go heap, so memory under real load can be
// split between playback media and everything else.
func (s *Server) logSabrMemory() {
	for range time.Tick(time.Minute) {
		m := s.Sabr.MemoryStats()
		var rt runtime.MemStats
		runtime.ReadMemStats(&rt)
		s.Log.Info("sabr_memory", "sessions", m.Sessions, "prepared", m.Prepared, "sessionMb", m.SessionBytes>>20,
			"mappedMb", m.MappedBytes>>20, "spareMb", m.SpareBytes>>20, "heapInuseMb", rt.HeapInuse>>20, "sysMb", rt.Sys>>20)
	}
}
