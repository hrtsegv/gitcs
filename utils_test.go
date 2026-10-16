package main

import (
	"slices"
	"testing"
	"time"
)

func TestSetTimeFlags(t *testing.T) {
	for _, tt := range []struct {
		name, since, until, wantSince, wantUntil string
		clamped                                  bool
		wantErr                                  bool
	}{
		{"same day", "2026-10-10", "2026-10-10", "2026-10-10", "2026-10-11", false, false},
		{"default range", "", "", "2026-04-17", "2026-10-17", false, false},
		{"end only", "", "2026-10-10", "2026-04-11", "2026-10-11", false, false},
		{"start only", "2026-10-10", "", "2026-10-10", "2026-10-17", false, false},
		{"future end is capped", "2026-10-10", "2099-01-01", "2026-10-10", "2026-10-17", true, false},
		{"reversed", "2026-10-12", "2026-10-10", "", "", false, true},
		{"future start", "2099-01-01", "", "", "", false, true},
		{"invalid start", "10-10-2026", "", "", "", false, true},
		{"invalid end", "", "2026-02-30", "", "", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := setTimeFlags(tt.since, tt.until, testClock)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if b.Since.Format(dateLayout) != tt.wantSince || b.Until.Format(dateLayout) != tt.wantUntil {
				t.Fatalf("got %s..%s, want %s..%s", b.Since, b.Until, tt.wantSince, tt.wantUntil)
			}
			if b.UntilClamped != tt.clamped {
				t.Fatalf("clamped = %v", b.UntilClamped)
			}
		})
	}
}

func TestCalendarDateAcrossDST(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "Asia/Beirut"} {
		t.Run(zone, func(t *testing.T) {
			location := testLocation(t, zone)
			now := time.Date(2026, 3, 30, 12, 0, 0, 0, location)
			b, err := setTimeFlags("2026-03-29", "2026-03-29", now)
			if err != nil {
				t.Fatal(err)
			}
			if b.Since.Format(dateLayout) != "2026-03-29" || b.Until.Format(dateLayout) != "2026-03-30" {
				t.Fatalf("DST changed calendar dates: %+v", b)
			}
			if b.Location != location {
				t.Fatal("reporting timezone was lost")
			}
			if b.Until.Sub(b.Since) != 24*time.Hour {
				t.Fatal("civil-date arithmetic should be independent of DST")
			}
		})
	}
}

func TestDateKeyConvertsTimezone(t *testing.T) {
	when := mustTime(t, "2026-10-10T23:30:00Z")
	if got := dateKey(when, testLocation(t, "Asia/Beirut")); got != "2026-10-11" {
		t.Fatalf("got %s, want local date 2026-10-11", got)
	}
}

func TestParseEmails(t *testing.T) {
	for _, tt := range []struct {
		name        string
		input, want []string
		wantErr     bool
	}{
		{"mailbox", []string{"Tester <tester@test.com>"}, []string{"tester@test.com"}, false},
		{"multiple", []string{"a@test.com", "b@test.com", "a@test.com"}, []string{"a@test.com", "b@test.com"}, false},
		{"wildcard", []string{"*"}, []string{"*"}, false},
		{"mixed wildcard", []string{"*", "a@test.com"}, nil, true},
		{"invalid", []string{"invalid"}, nil, true},
		{"blank", []string{""}, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEmails(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetMaxValue(t *testing.T) {
	if got := getMaxValue(nil); got != 0 {
		t.Fatalf("empty maximum = %d", got)
	}
	if got := getMaxValue(map[string]int{"2026-10-10": 3, "2026-10-11": 100}); got != 100 {
		t.Fatalf("maximum = %d", got)
	}
}
