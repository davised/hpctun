package sshconfig

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"node*", "node1", true},
		{"node*", "node", true},
		{"node*", "xnode1", false},
		{"gpu??", "gpu01", true},
		{"gpu??", "gpu1", false},
		{"*.example.org", "web.example.org", true},
		{"*.example.org", "example.org", false},
		{"node*", "NODE7", true}, // ssh lowercases the host first
		{"Node*", "node1", true}, // ssh lowercases the pattern too
		{"Chrom1", "chrom1", true},
		{"web1", "web1", true},
		{"web1", "web10", false},
		{"*", "", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.host); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pattern, c.host, got)
		}
	}
	if !MatchAny([]string{"a*", "b*"}, "b2") || MatchAny(nil, "x") {
		t.Error("MatchAny")
	}
}

func TestMatchList(t *testing.T) {
	cases := []struct {
		list, s string
		want    bool
	}{
		{"UseKeychain", "UseKeychain", true},
		{"usekeychain", "UseKeychain", true}, // ssh compares without case
		{"AddKeysToAgent,UseKeychain", "UseKeychain", true},
		{"Use*", "UseKeychain", true},
		{"SomethingElse", "UseKeychain", false},
		{"*,!UseKeychain", "UseKeychain", false}, // a negated match wins
		{"!Other", "UseKeychain", false},         // negation alone matches nothing
		{"", "UseKeychain", false},
	}
	for _, c := range cases {
		if got := MatchList(c.list, c.s); got != c.want {
			t.Errorf("MatchList(%q, %q) = %v", c.list, c.s, got)
		}
	}
}
