package main

import (
	"fmt"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

const loopbackName = "lo"

type ifreqFlags struct {
	Name  [syscall.IFNAMSIZ]byte
	Flags uint16
	Pad   [22]byte
}

func linkFlags(name string) (uint16, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("open control socket: %w", err)
	}
	defer syscall.Close(fd)

	var req ifreqFlags
	copy(req.Name[:], name)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.SIOCGIFFLAGS), uintptr(unsafe.Pointer(&req))); errno != 0 {
		return 0, errno
	}
	return req.Flags, nil
}

func interfaceNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		names = append(names, iface.Name)
	}
	return names, nil
}

func onlyLoopback(names []string) bool {
	return len(names) == 1 && names[0] == loopbackName
}

func formatInterfaceList(names []string) string {
	return strings.Join(names, ", ")
}
