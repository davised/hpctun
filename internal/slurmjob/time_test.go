package slurmjob

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30":         30 * time.Minute,
		"90:30":      90*time.Minute + 30*time.Second,
		"24:00:00":   24 * time.Hour,
		"1-12":       36 * time.Hour,
		"1-12:30":    36*time.Hour + 30*time.Minute,
		"2-00:00:10": 48*time.Hour + 10*time.Second,
	} {
		if got, err := ParseTime(in); err != nil || got != want {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0:00", "UNLIMITED", "infinite", "-5", "+5", " 5", "1:2:3:4", "1-", "1-2-3", "a:b"} {
		if _, err := ParseTime(in); err == nil {
			t.Errorf("ParseTime(%q) accepted", in)
		}
	}
}

func TestDeadline(t *testing.T) {
	if got := Deadline(24*time.Hour, 30*time.Minute); got != "now+1470minutes" {
		t.Errorf("got %q", got)
	}
	if got := Deadline(90*time.Second, 30*time.Minute); got != "now+32minutes" {
		t.Errorf("rounding: got %q", got)
	}
}
