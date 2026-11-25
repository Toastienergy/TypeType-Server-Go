package recommend

// Shuffled is Kotlin's list.shuffled(Random(seed)) for an Int seed: a copy shuffled by the same XorWow draws, so a
// seed picks the same items on both servers.
func Shuffled[T any](src []T, seed int32) []T {
	out := append([]T(nil), src...)
	rnd := newXorWow(int64(seed))
	for i := len(out) - 1; i >= 1; i-- {
		j := rnd.intn(int32(i + 1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}
