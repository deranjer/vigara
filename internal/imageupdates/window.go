// Package imageupdates defines image monitoring and explicit update policies.
package imageupdates

import (
	"errors"
	"fmt"
	"time"
	_ "time/tzdata"
)

type Window struct {
	Days     []int  `json:"days"` // Sunday=0
	Start    string `json:"start"`
	Minutes  int    `json:"minutes"`
	Timezone string `json:"timezone"`
}

func DefaultWindow() Window {
	return Window{Days: []int{0}, Start: "03:00", Minutes: 120, Timezone: "UTC"}
}
func (w Window) Validate() error {
	if len(w.Days) == 0 || len(w.Days) > 7 || w.Minutes < 15 || w.Minutes > 1440 {
		return errors.New("choose days and a duration from 15 to 1440 minutes")
	}
	seen := map[int]bool{}
	for _, d := range w.Days {
		if d < 0 || d > 6 || seen[d] {
			return errors.New("choose unique weekdays")
		}
		seen[d] = true
	}
	t, err := time.Parse("15:04", w.Start)
	if err != nil || t.Format("15:04") != w.Start {
		return errors.New("use a start time in HH:MM format")
	}
	if _, err = time.LoadLocation(w.Timezone); err != nil {
		return errors.New("choose a valid timezone")
	}
	return nil
}

// Slot uses elapsed duration from a local start. Spring-forward missing starts
// are skipped, and the first occurrence of a repeated fall-back start wins.
// Including yesterday covers windows which cross midnight.
func (w Window) Slot(now time.Time) string {
	if w.Validate() != nil {
		return ""
	}
	loc, _ := time.LoadLocation(w.Timezone)
	local := now.In(loc)
	clock, _ := time.Parse("15:04", w.Start)
	for back := 0; back <= 1; back++ {
		date := local.AddDate(0, 0, -back)
		allowed := false
		for _, d := range w.Days {
			if int(date.Weekday()) == d {
				allowed = true
			}
		}
		if !allowed {
			continue
		}
		start := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if start.Format("15:04") != w.Start {
			continue
		}
		// Explicitly select the earlier repeated wall-clock occurrence.
		for earlier := start.Add(-time.Hour); earlier.Format("2006-01-02 15:04") == start.Format("2006-01-02 15:04"); earlier = start.Add(-time.Hour) {
			start = earlier
		}
		if !now.Before(start) && now.Before(start.Add(time.Duration(w.Minutes)*time.Minute)) {
			return fmt.Sprintf("%s/%s", w.Timezone, start.UTC().Format(time.RFC3339))
		}
	}
	return ""
}
