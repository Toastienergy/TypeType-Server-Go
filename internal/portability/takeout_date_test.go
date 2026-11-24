package portability

import (
	"testing"
	"time"
)

func TestTakeoutDate(t *testing.T) {
	utc := func(s string) int64 {
		x, err := time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		return x.UnixMilli()
	}
	cases := []struct {
		in   string
		want int64
	}{
		{"Sep 16, 2026, 6:02:08 PM CEST", 1_789_574_528_000},
		{"2026年9月4日 午後9:45:27 JST", 1_788_525_927_000},
		{"2026年9月4日 晚上9:45:27 CST", utc("2026-09-04 13:45:27")},
		{"16 sept. 2026, 18:02:08 CEST", 1_789_574_528_000},
		{"16 de septiembre de 2026, 18:02:08 CEST", 1_789_574_528_000},
		{"16.09.2026, 18:02:08 MESZ", 1_789_574_528_000},
		{"16. Sept. 2026, 18:02:08 MESZ", 1_789_574_528_000},
		{"16 сент. 2026 г., 18:02:08 CEST", 0},
		{"2026/09/16 18:02:08 CEST", 1_789_574_528_000},
		{"2026-09-16, 18:02:08 CEST", 1_789_574_528_000},
		{"4 سبتمبر 2026, 9:45:27 مساءً CEST", utc("2026-09-04 19:45:27")},
		{"4 सितंबर 2026, 9:45:27 शाम CEST", utc("2026-09-04 19:45:27")},
		{"Jan 5, 2024, 10:00:00 AM PST", utc("2024-01-05 18:00:00")},
		{"5 janv. 2024, 22:00:00 UTC+1", utc("2024-01-05 21:00:00")},
		{"2024-01-05T10:00:00.123Z", utc("2024-01-05 10:00:00") + 123},
		{"2024-01-05", utc("2024-01-05 00:00:00")},
		{"25/12/2023, 08:30:00 GMT", utc("2023-12-25 08:30:00")},
		{"not a date", -1},
	}
	for _, c := range cases {
		got, ok := takeoutDate(c.in)
		switch {
		case c.want == -1 && ok:
			t.Errorf("%q parsed as %d", c.in, got)
		case c.want == 0 && !ok:
			t.Logf("%q not parsed (known gap)", c.in)
		case c.want > 0 && (!ok || got != c.want):
			t.Errorf("%q = %d %v, want %d", c.in, got, ok, c.want)
		}
	}
	if _, ok := takeoutDate("2026년 9월 4일 오후 9:45:27 KST"); !ok {
		t.Error("Korean date not parsed")
	}
}
