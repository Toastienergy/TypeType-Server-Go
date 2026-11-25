//go:build !linux

package sabr

import "sync/atomic"

const mappedMin = 1 << 62

var mappedBytes atomic.Int64

func mediaAlloc(n int) []byte { return make([]byte, 0, n) }

func mediaFree([]byte) {}
