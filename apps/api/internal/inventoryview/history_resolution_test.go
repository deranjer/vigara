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
	if got := historyBinSeconds(6*time.Hour, 5); got != 60 {
		t.Fatalf("recent charts should preserve minute resolution: %d", got)
	}
	if got := historyBinSeconds(24*time.Hour, 5); got != 300 {
		t.Fatalf("a day should use five-minute points: %d", got)
	}
}

func TestHistoryResolutionKeepsShortRangesWithinPointBudget(t *testing.T) {
	for _, hours := range []int64{1, 6, 12, 24} {
		for _, series := range []int64{1, 5, 32, 64, 200} {
			duration := time.Duration(hours) * time.Hour
			bin := historyBinSeconds(duration, series)
			capped := min(series, maxHistorySeries)
			if bin < 60 || bin%60 != 0 {
				t.Fatalf("%dh, %d series: invalid bin %d", hours, series, bin)
			}
			if points := (int64(duration/time.Second)/bin + 2) * capped; points > maxHistoryPoints {
				t.Fatalf("%dh, %d series: %d points exceeds budget", hours, series, points)
			}
		}
	}
	// A day of 13 label series used to be rejected after the query had run.
	if bin := historyBinSeconds(24*time.Hour, 18); bin != 300 {
		t.Fatalf("18 container series for a day should still use five-minute points: %d", bin)
	}
}
