package probe

import "encoding/binary"

// TCP states in GetExtendedTcpTable rows (MIB_TCP_STATE).
const (
	tcpListen      = 2
	tcpEstablished = 5
)

// tcpRow is what hpctun reads from one row of Windows' TCP table.
type tcpRow struct {
	Port  int // local port
	State int
	PID   int
}

// parseTCPTable reads a MIB_TCPTABLE_OWNER_PID: a DWORD count, then rows
// of six DWORDs (state, local addr, local port, remote addr, remote port,
// pid). Ports are in network byte order in the DWORD's first two bytes.
func parseTCPTable(b []byte) []tcpRow { return parseRows(b, 24, 0, 8, 20) }

// parseTCP6Table reads a MIB_TCP6TABLE_OWNER_PID: rows of local addr[16],
// scope, port, remote addr[16], scope, port, state, pid.
func parseTCP6Table(b []byte) []tcpRow { return parseRows(b, 56, 48, 20, 52) }

func parseRows(b []byte, size, stateAt, portAt, pidAt int) []tcpRow {
	if len(b) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b))
	var rows []tcpRow
	for i := 0; i < n && 4+(i+1)*size <= len(b); i++ {
		r := b[4+i*size : 4+(i+1)*size]
		rows = append(rows, tcpRow{
			Port:  int(r[portAt])<<8 | int(r[portAt+1]),
			State: int(binary.LittleEndian.Uint32(r[stateAt:])),
			PID:   int(binary.LittleEndian.Uint32(r[pidAt:])),
		})
	}
	return rows
}

// snapshotOf turns table rows into a Snapshot as lsof's output is: the
// first listener on a port wins, and established connections are counted
// by local port. image names a pid's program.
func snapshotOf(rows []tcpRow, image func(pid int) string) Snapshot {
	s := Snapshot{Listeners: map[int]Listener{}, Conns: map[int]int{}}
	for _, r := range rows {
		switch r.State {
		case tcpListen:
			if _, seen := s.Listeners[r.Port]; !seen {
				s.Listeners[r.Port] = Listener{Port: r.Port, PID: r.PID, Command: image(r.PID)}
			}
		case tcpEstablished:
			s.Conns[r.Port]++
		}
	}
	return s
}
