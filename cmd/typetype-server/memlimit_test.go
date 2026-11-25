package main

import "testing"

func TestHeapLimitLeavesTheMediaBudgetOutside(t *testing.T) {
	for _, c := range []struct{ budget, cgroup, want int64 }{
		{512 << 20, 0, 384 << 20},
		{512 << 20, 2 << 30, 384 << 20},
		{512 << 20, 768 << 20, 768<<20*9/10 - 512<<20},
		{512 << 20, 512 << 20, 64 << 20},
	} {
		if got := heapLimit(c.budget, c.cgroup); got != c.want {
			t.Fatalf("budget %d cgroup %d: %d, want %d", c.budget>>20, c.cgroup>>20, got>>20, c.want>>20)
		}
	}
}
