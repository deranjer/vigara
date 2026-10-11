package main

import (
	"reflect"
	"testing"
	"time"
)

func TestLocalTestTargetRequiresLoopbackAndMarkedDatabase(t *testing.T) {
	for _, tc := range []struct {
		host, database string
		want           bool
	}{
		{"localhost", "vigira_test", true},
		{"127.0.0.1", "metrics_bench", true},
		{"::1", "vigira_dev", true},
		{"localhost", "vigira", false},
		{"localhost", "vigira_device", false},
		{"localhost", "prod_test", false},
		{"localhost", "vigira_public_bench", false},
		{"db.internal", "vigira_test", false},
		{"10.0.0.1", "metrics_bench", false},
	} {
		if got := localTestTarget(tc.host, tc.database); got != tc.want {
			t.Errorf("localTestTarget(%q, %q) = %v, want %v", tc.host, tc.database, got, tc.want)
		}
	}
}

func TestParseDeviceCounts(t *testing.T) {
	got, err := deviceCounts("12, 36,60")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{12, 36, 60}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deviceCounts() = %v, want %v", got, want)
	}
	for _, input := range []string{"0", "61", "abc", ""} {
		if _, err := deviceCounts(input); err == nil {
			t.Errorf("deviceCounts(%q) unexpectedly succeeded", input)
		}
	}
}

func TestDurationList(t *testing.T) {
	got, err := durationList("25h,48h,90d")
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{25 * time.Hour, 48 * time.Hour, 90 * 24 * time.Hour}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durationList() = %v, want %v", got, want)
	}
	for _, input := range []string{"0h", "invalid", "-1h"} {
		if _, err := durationList(input); err == nil {
			t.Errorf("durationList(%q) unexpectedly succeeded", input)
		}
	}
}

func TestPercentileUsesNearestRankFloor(t *testing.T) {
	values := []time.Duration{5 * time.Millisecond, time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	if got := percentile(values, .5); got != 3*time.Millisecond {
		t.Fatalf("median = %s, want 3ms", got)
	}
	if got := percentile(values, .95); got != 4*time.Millisecond {
		t.Fatalf("p95 = %s, want 4ms for five samples", got)
	}
}

func TestDurationLabel(t *testing.T) {
	for d, want := range map[time.Duration]string{25 * time.Hour: "25h", 48 * time.Hour: "2d", 90 * 24 * time.Hour: "90d"} {
		if got := durationLabel(d); got != want {
			t.Errorf("durationLabel(%s) = %q, want %q", d, got, want)
		}
	}
}
