package tunnel

import (
	"fmt"
	"strconv"

	"hpctun/internal/config"
	"hpctun/internal/state"
)

// Select resolves command-line arguments to tunnels. A name must be a config
// tunnel. A number is the config tunnel on that port, else the ad-hoc state
// entry on that port, else it is returned in unknownPorts for the caller to
// treat as a new ad-hoc tunnel (up) or an error (down).
func Select(cfg *config.Config, st *state.State, args []string) (specs []Spec, unknownPorts []int, err error) {
	for _, a := range args {
		port, numErr := strconv.Atoi(a)
		if numErr != nil {
			t, ok := cfg.Lookup(a)
			if !ok {
				return nil, nil, fmt.Errorf("no tunnel named %q", a)
			}
			specs = append(specs, FromConfig(t))
			continue
		}
		if !config.ValidPort(port) {
			return nil, nil, fmt.Errorf("port %d is out of range", port)
		}
		if t, ok := cfg.ByPort(port); ok {
			specs = append(specs, FromConfig(t))
		} else if e, ok := st.ByPort(port); ok {
			specs = append(specs, FromEntry(e))
		} else {
			unknownPorts = append(unknownPorts, port)
		}
	}
	return specs, unknownPorts, nil
}
