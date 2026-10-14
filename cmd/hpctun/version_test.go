package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFrom(t *testing.T) {
	vcs := func(rev, time, modified string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
			{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: rev},
			{Key: "vcs.time", Value: time}, {Key: "vcs.modified", Value: modified},
		}}
	}
	mod := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	cases := []struct {
		name string
		info *debug.BuildInfo
		want string
	}{
		{"checkout", vcs("1dac96b98ddcf01a707218e21eec903373b90bd7", "2026-09-24T05:10:49Z", "false"), "v0.20260924-1dac96b"},
		{"checkout with edits", vcs("1dac96b98ddcf01a707218e21eec903373b90bd7", "2026-09-24T05:10:49Z", "true"), "v0.20260924-1dac96b-dirty"},
		{"date is UTC", vcs("1dac96b98ddcf01a707218e21eec903373b90bd7", "2026-09-23T22:10:49-07:00", "false"), "v0.20260924-1dac96b"},
		{"go install of a commit", mod("v0.0.0-20260924051049-1dac96b98ddc"), "v0.20260924-1dac96b"},
		{"pseudo-version after a tag", mod("v0.1.1-0.20260924051049-1dac96b98ddc"), "v0.20260924-1dac96b"},
		{"tagged release", mod("v1.0.0"), "v1.0.0"},
		{"go run, no vcs", mod("(devel)"), "v0-dev"},
		{"no build info", nil, "v0-dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := versionFrom(c.info); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
