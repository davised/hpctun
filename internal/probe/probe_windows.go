package probe

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"hpctun/internal/proc"
	"hpctun/internal/sshctl"
)

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL

// Take reads Windows' TCP tables, IPv4 then IPv6. The runner is not used:
// Windows has no lsof.
func Take(ctx context.Context, _ sshctl.Runner) (Snapshot, error) {
	v4, err := tcpTable(windows.AF_INET)
	if err != nil {
		return Snapshot{}, err
	}
	v6, err := tcpTable(windows.AF_INET6)
	if err != nil {
		return Snapshot{}, err
	}
	rows := append(parseTCPTable(v4), parseTCP6Table(v6)...)
	images := map[int]string{}
	return snapshotOf(rows, func(pid int) string {
		if im, ok := images[pid]; ok {
			return im
		}
		info, _ := proc.Lookup(pid)
		images[pid] = info.Image
		return info.Image
	}), nil
}

// tcpTable returns the raw table for family, growing the buffer while
// connections come and go between calls.
func tcpTable(family uint32) ([]byte, error) {
	size := uint32(0)
	var buf []byte
	for range 8 {
		var p unsafe.Pointer
		if len(buf) > 0 {
			p = unsafe.Pointer(&buf[0])
		}
		r, _, _ := getExtendedTCPTable.Call(uintptr(p), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		switch windows.Errno(r) {
		case 0:
			return buf, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			buf = make([]byte, size)
		default:
			return nil, fmt.Errorf("GetExtendedTcpTable: %w", windows.Errno(r))
		}
	}
	return nil, fmt.Errorf("GetExtendedTcpTable: the table kept growing")
}
