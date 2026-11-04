package imageupdates

import (
	"testing"
	"time"
)

func TestWindowBoundariesAndOvernight(t *testing.T) {
	w := Window{Days: []int{1}, Start: "23:30", Minutes: 120, Timezone: "America/New_York"}
	for _, tc := range []struct {
		at   string
		open bool
	}{
		{"2026-11-03T04:29:59Z", false}, {"2026-11-03T04:30:00Z", true}, {"2026-11-03T06:29:59Z", true}, {"2026-11-03T06:30:00Z", false}, {"2026-11-04T04:30:00Z", false},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		if got := w.Slot(at) != ""; got != tc.open {
			t.Errorf("%s open=%v want=%v", tc.at, got, tc.open)
		}
	}
}
func TestDSTDoesNotInventMissingWindowOrRepeatAnAttempt(t *testing.T) {
	w := Window{Days: []int{0}, Start: "02:30", Minutes: 60, Timezone: "America/New_York"}
	at, _ := time.Parse(time.RFC3339, "2026-03-08T07:35:00Z")
	if w.Slot(at) != "" {
		t.Fatal("spring-forward nonexistent start must be skipped")
	}
	w.Start = "01:30"
	w.Minutes = 120
	first, _ := time.Parse(time.RFC3339, "2026-11-01T05:45:00Z")
	second := first.Add(time.Hour)
	if w.Slot(first) == "" || w.Slot(first) != w.Slot(second) {
		t.Fatal("repeated clock hour must retain one window identity")
	}
}
func TestInvalidWindowNeverOpens(t *testing.T) {
	for _, w := range []Window{
		{Days: []int{7}, Start: "03:00", Minutes: 60, Timezone: "UTC"},
		{Days: []int{0, 0}, Start: "03:00", Minutes: 60, Timezone: "UTC"},
		{Days: []int{0}, Start: "3:00", Minutes: 60, Timezone: "UTC"},
		{Days: []int{0}, Start: "03:00", Minutes: 0, Timezone: "UTC"},
		{Days: []int{0}, Start: "03:00", Minutes: 60, Timezone: "bad/zone"},
	} {
		if w.Validate() == nil || w.Slot(time.Now()) != "" {
			t.Fatal("invalid window accepted", w)
		}
	}
}
