package tunnel

import "os"

// KillTERM ends pid. Windows has no SIGTERM, and hpctun down refuses there;
// this keeps the package building.
func KillTERM(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
