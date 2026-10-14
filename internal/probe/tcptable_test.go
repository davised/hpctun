package probe

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// v4Row is one MIB_TCPROW_OWNER_PID: state, local addr, local port (network
// order in the low 16 bits), remote addr, remote port, pid.
func v4Row(state, port, pid int) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], uint32(state))
	binary.LittleEndian.PutUint32(b[4:], 0x0100007f) // 127.0.0.1
	b[8], b[9] = byte(port>>8), byte(port)
	b[16], b[17] = 0xc3, 0x50 // remote port 50000
	binary.LittleEndian.PutUint32(b[20:], uint32(pid))
	return b
}

// v6Row is one MIB_TCP6ROW_OWNER_PID: local addr[16], scope, port, remote
// addr[16], scope, port, state, pid.
func v6Row(state, port, pid int) []byte {
	b := make([]byte, 56)
	b[15] = 1 // ::1
	b[20], b[21] = byte(port>>8), byte(port)
	binary.LittleEndian.PutUint32(b[48:], uint32(state))
	binary.LittleEndian.PutUint32(b[52:], uint32(pid))
	return b
}

func table(rows ...[]byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(rows)))
	for _, r := range rows {
		b = append(b, r...)
	}
	return b
}

func TestParseTCPTables(t *testing.T) {
	v4 := parseTCPTable(table(v4Row(2, 8888, 10), v4Row(5, 8888, 11), v4Row(5, 50000, 12)))
	want := []tcpRow{{8888, 2, 10}, {8888, 5, 11}, {50000, 5, 12}}
	if !reflect.DeepEqual(v4, want) {
		t.Fatalf("v4 = %+v", v4)
	}
	v6 := parseTCP6Table(table(v6Row(2, 5005, 20)))
	if !reflect.DeepEqual(v6, []tcpRow{{5005, 2, 20}}) {
		t.Fatalf("v6 = %+v", v6)
	}
	// A count larger than the buffer holds: parse what is there.
	short := table(v4Row(2, 1, 1))
	binary.LittleEndian.PutUint32(short, 9)
	if got := parseTCPTable(short); len(got) != 1 {
		t.Fatalf("short = %+v", got)
	}
	if got := parseTCPTable(nil); got != nil {
		t.Fatalf("nil = %+v", got)
	}
}

func TestSnapshotOf(t *testing.T) {
	rows := []tcpRow{{8888, tcpListen, 10}, {8888, tcpListen, 99}, {8888, tcpEstablished, 10}, {8888, tcpEstablished, 10}, {5005, tcpListen, 20}, {50000, tcpEstablished, 30}}
	images := map[int]string{10: "ssh", 20: "python"}
	s := snapshotOf(rows, func(pid int) string { return images[pid] })
	if s.Listeners[8888] != (Listener{Port: 8888, PID: 10, Command: "ssh"}) || s.Listeners[5005].Command != "python" {
		t.Fatalf("listeners %+v", s.Listeners)
	}
	if s.Conns[8888] != 2 || s.Conns[50000] != 1 || len(s.Listeners) != 2 {
		t.Fatalf("conns %+v", s.Conns)
	}
}
