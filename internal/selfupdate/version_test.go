package selfupdate

import (
	"errors"
	"testing"
)

func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.20261007-ab131b6": true,
		"v1.2.3":              true,
		"":                    false,
		"0.20261007-ab131b6":  false,
		"v0.2026 x":           false,
		"v0.20261007-ab/../x": false,
	} {
		if ValidVersion(v) != want {
			t.Errorf("ValidVersion(%q) != %v", v, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		date    string
		dev, ok bool
	}{
		{"v0.20261007-ab131b6", "20261007", false, true},
		{"v0.20261007-ab131b6-dirty", "20261007", true, true},
		{"v0-dev", "", true, true},
		{"v1.2.3", "", false, false},
	}
	for _, c := range cases {
		date, dev, ok := ParseVersion(c.in)
		if date != c.date || dev != c.dev || ok != c.ok {
			t.Errorf("ParseVersion(%q) = %q %v %v", c.in, date, dev, ok)
		}
	}
}

func TestDecide(t *testing.T) {
	const old, cur = "v0.20260928-9662ab3", "v0.20261007-ab131b6"
	cases := []struct {
		name            string
		running, latest string
		force           bool
		want            Action
		err             error
	}{
		{"up to date", cur, cur, false, UpToDate, nil},
		{"forced reinstall", cur, cur, true, Update, nil},
		{"update", old, cur, false, Update, nil},
		{"rolled back", cur, old, false, Downgrade, nil},
		{"same date", "v0.20261007-1111111", cur, false, Update, nil},
		{"dev", "v0-dev", cur, false, 0, ErrDevBuild},
		{"dirty", cur + "-dirty", cur, false, 0, ErrDevBuild},
		{"dev forced", "v0-dev", cur, true, Update, nil},
		{"dirty forced, older date", "v0.20261009-ab131b6-dirty", cur, true, Downgrade, nil},
		{"tagged running", "v1.2.3", cur, false, Update, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Decide(c.running, c.latest, c.force)
			if !errors.Is(err, c.err) || (err == nil && got != c.want) {
				t.Fatalf("Decide = %v, %v; want %v, %v", got, err, c.want, c.err)
			}
		})
	}
	if _, err := Decide(cur, "v1.2.3", false); err == nil {
		t.Fatal("a latest without a date was accepted")
	}
}
