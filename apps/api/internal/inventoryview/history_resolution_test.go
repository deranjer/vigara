package inventoryview

import (
	"testing"
	"time"
)

func TestHistoryResolutionKeepsYearChartsWithinPointBudget(t *testing.T) {
	for _, days := range []int64{2, 30, 90, 365} {
		for _, series := range []int64{1, 5, 32, 64} {
			duration := time.Duration(days) * 24 * time.Hour
			bin := historyBinSeconds(duration, series)
			if bin < 3600 || bin%3600 != 0 {
				t.Fatalf("invalid hourly multiple: %d", bin)
			}
			points := (int64(duration/time.Second)/bin + 2) * series
			if points > 20000 {
				t.Fatalf("%d days, %d series: %d points exceeds budget", days, series, points)
			}
		}
	}
	if got := historyBinSeconds(365*24*time.Hour, 1); got != 3600 {
		t.Fatalf("single-series yearly chart should preserve hourly resolution: %d", got)
	}
	if got := historyBinSeconds(24*time.Hour, 5); got != 60 {
		t.Fatalf("recent charts should preserve minute resolution: %d", got)
	}
}
