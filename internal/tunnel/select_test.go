package tunnel

import (
	"slices"
	"testing"

	"hpctun/internal/config"
	"hpctun/internal/state"
)

func TestSelect(t *testing.T) {
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002}}}

	specs, unknown, err := Select(cfg, st, []string{"jup", "8099", "4002", "7777"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Label())
	}
	if !slices.Equal(names, []string{"jup", "dash", "(ad-hoc)"}) {
		t.Fatalf("specs = %v", names)
	}
	if !slices.Equal(unknown, []int{7777}) {
		t.Fatalf("unknown = %v", unknown)
	}

	if _, _, err := Select(cfg, st, []string{"nope"}); err == nil {
		t.Fatal("unknown name accepted")
	}
	if _, _, err := Select(cfg, st, []string{"70000"}); err == nil {
		t.Fatal("out-of-range port accepted")
	}
}

func TestSpecsListsConfigThenAdhoc(t *testing.T) {
	cfg := testConfig(t)
	st := &state.State{Tunnels: []state.Entry{
		{Name: "dash", Port: 8099}, // named: config wins
		{Port: 4002, Route: config.RouteMaster, Host: "node1", RemotePort: 4002},
	}}
	var ports []int
	for _, s := range Specs(cfg, st) {
		ports = append(ports, s.Port)
	}
	if !slices.Equal(ports, []int{8099, 4001, 5005, 4002}) {
		t.Fatalf("ports = %v (config tunnels in name order, then ad-hoc)", ports)
	}
}
