package bilibili

import "testing"

func TestBVAVRoundTrip(t *testing.T) {
	if got := BV2AV("BV17x411w7KC"); got != 170001 {
		t.Fatalf("BV17x411w7KC -> %d", got)
	}
	if got := AV2BV(170001); got != "BV17x411w7KC" {
		t.Fatalf("170001 -> %s", got)
	}
}
