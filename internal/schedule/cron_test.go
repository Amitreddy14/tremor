package schedule

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNext(t *testing.T) {
	cases := []struct{ expr, after, want string }{
		{"* * * * *", "2026-11-11 10:00", "2026-11-11 10:01"},
		{"*/15 * * * *", "2026-11-11 10:07", "2026-11-11 10:15"},
		{"0 9 * * *", "2026-11-11 09:00", "2026-11-12 09:00"},
		{"30 2 * * mon-fri", "2026-11-13 03:00", "2026-11-16 02:30"}, // Fri -> Mon
		{"0 0 1 jan *", "2026-11-11 10:00", "2027-01-01 00:00"},
		{"@hourly", "2026-11-11 10:30", "2026-11-11 11:00"},
		{"0 12 * * 7", "2026-11-11 00:00", "2026-11-15 12:00"}, // 7 = Sunday
		{"0 0 29 2 *", "2026-11-11 00:00", "2028-02-29 00:00"},
		{"10-12 8 * * *", "2026-11-11 08:11", "2026-11-11 08:12"},
	}
	for _, tc := range cases {
		c, err := Parse(tc.expr)
		if err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got := c.Next(at(tc.after)); !got.Equal(at(tc.want)) {
			t.Errorf("%q after %s: got %s want %s", tc.expr, tc.after, got.Format("2006-01-02 15:04"), tc.want)
		}
	}
}

func TestNextNever(t *testing.T) {
	c, err := Parse("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Next(at("2026-01-01 00:00")); !got.IsZero() {
		t.Fatalf("expected no run, got %s", got)
	}
}

func TestDayOrSemantics(t *testing.T) {
	// Both day fields restricted: the 1st OR any Monday.
	c, _ := Parse("0 0 1 * mon")
	if got := c.Next(at("2026-11-02 00:00")); !got.Equal(at("2026-11-09 00:00")) {
		t.Fatalf("got %s", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, e := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "5-1 * * * *", "*/0 * * * *", "a * * * *", "1,,2 * * * *", "* * * foo *", "* * 0 * *"} {
		if _, err := Parse(e); err == nil {
			t.Errorf("%q should not parse", e)
		}
	}
}

func TestDue(t *testing.T) {
	c, _ := Parse("*/10 * * * *")
	latest, missed, ok := c.Due(at("2026-11-11 10:00"), at("2026-11-11 10:35"))
	if !ok || missed != 3 || !latest.Equal(at("2026-11-11 10:30")) {
		t.Fatalf("latest=%s missed=%d ok=%v", latest, missed, ok)
	}
	if _, _, ok := c.Due(at("2026-11-11 10:30"), at("2026-11-11 10:35")); ok {
		t.Fatal("nothing should be due")
	}
	// Exactly on the boundary counts as due.
	if _, m, ok := c.Due(at("2026-11-11 10:00"), at("2026-11-11 10:10")); !ok || m != 1 {
		t.Fatalf("boundary: missed=%d ok=%v", m, ok)
	}
}
