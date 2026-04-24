package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

type selfTestReport struct {
	failures int
}

func (r *selfTestReport) pass(format string, args ...any) {
	fmt.Printf("[✓] "+format+"\n", args...)
}

func (r *selfTestReport) fail(format string, args ...any) {
	r.failures++
	fmt.Printf("[x] "+format+"\n", args...)
}

func (r *selfTestReport) err() error {
	if r.failures == 0 {
		return nil
	}
	if r.failures == 1 {
		return errors.New("self-test failed with 1 problem")
	}
	return fmt.Errorf("self-test failed with %d problems", r.failures)
}

func runSelfTest() error {
	report := &selfTestReport{}

	uid := os.Getuid()
	gid := os.Getgid()
	home, err := os.UserHomeDir()
	if err != nil {
		report.fail("resolve home directory: %v", err)
	} else {
		report.pass("caller uid=%d gid=%d home=%s", uid, gid, home)
	}

	if value, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		report.pass("kernel.unprivileged_userns_clone=%s", strings.TrimSpace(string(value)))
	} else if !errors.Is(err, os.ErrNotExist) {
		report.fail("read /proc/sys/kernel/unprivileged_userns_clone: %v", err)
	}

	if err := runEndToEndProbe(uid, gid, home); err != nil {
		report.fail("end-to-end probe: %v", err)
	} else {
		report.pass("end-to-end probe completed")
	}

	if err := report.err(); err != nil {
		return err
	}
	report.pass("self-test completed")
	return nil
}

func runInternalProbe(expectUID, expectGID int, expectHome string) error {
	report := &selfTestReport{}

	// This probe runs inside the isolated environment created by nonet itself,
	// so these checks exercise the real execution path rather than a mock.
	if expectUID >= 0 && os.Getuid() != expectUID {
		report.fail("uid matches caller: got %d want %d", os.Getuid(), expectUID)
	} else if expectUID >= 0 {
		report.pass("uid matches caller: %d", os.Getuid())
	}
	if expectGID >= 0 && os.Getgid() != expectGID {
		report.fail("gid matches caller: got %d want %d", os.Getgid(), expectGID)
	} else if expectGID >= 0 {
		report.pass("gid matches caller: %d", os.Getgid())
	}
	if expectHome != "" {
		if _, err := os.ReadDir(expectHome); err != nil {
			report.fail("home access check for %s: %v", expectHome, err)
		} else {
			report.pass("home access check: %s", expectHome)
		}
	}

	names, err := interfaceNames()
	if err != nil {
		report.fail("list interfaces: %v", err)
	} else if !onlyLoopback(names) {
		report.fail("only loopback interface is present: %s", formatInterfaceList(names))
	} else {
		report.pass("only loopback interface is present: %s", formatInterfaceList(names))
	}
	hasDefaultRoute, err := hasDefaultRoute()
	if err != nil {
		report.fail("inspect routes: %v", err)
	} else if hasDefaultRoute {
		report.fail("default route is absent")
	} else {
		report.pass("default route is absent")
	}

	flags, err := linkFlags(loopbackName)
	if err != nil {
		report.fail("inspect loopback flags: %v", err)
	} else if flags&syscall.IFF_UP == 0 {
		report.fail("loopback is up")
	} else {
		report.pass("loopback is up")
	}
	if err := probeLoopbackTCP(); err != nil {
		report.fail("loopback TCP probe: %v", err)
	} else {
		report.pass("loopback TCP probe succeeded")
	}

	return report.err()
}

func runEndToEndProbe(uid, gid int, home string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self executable: %w", err)
	}
	// Re-exec the same binary in hidden probe mode so self-test validates the
	// same clone/map/unshare/exec flow that normal command execution uses.
	cmd := exec.Command(self, internalProbeCommand)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%d", internalExpectUIDEnv, uid),
		fmt.Sprintf("%s=%d", internalExpectGIDEnv, gid),
		fmt.Sprintf("%s=%s", internalExpectHomeEnv, home),
	)
	return runWithEnv(cmd.Args, cmd.Env)
}

func probeLoopbackTCP() error {
	// A listener plus an in-namespace client dial is a cheap end-to-end check
	// that loopback is both present and actually functional, not just flagged UP.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()

	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		var buf [1]byte
		if _, err := conn.Read(buf[:]); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	conn, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		return err
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		conn.Close()
		return err
	}
	conn.Close()

	return <-errCh
}

func hasDefaultRoute() (bool, error) {
	// Parsing procfs keeps the check dependency-free; iproute2 would consult the
	// same kernel routing data through netlink.
	v4, err := hasDefaultRouteV4("/proc/net/route")
	if err != nil {
		return false, err
	}
	if v4 {
		return true, nil
	}
	return hasDefaultRouteV6("/proc/net/ipv6_route")
}

func hasDefaultRouteV4(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 {
			continue
		}
		// /proc/net/route stores destination and mask as hex-encoded IPv4 values
		// in host byte order. A zero destination plus zero mask represents the
		// default route.
		if fields[0] != loopbackName && fields[1] == "00000000" && fields[7] == "00000000" {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func hasDefaultRouteV6(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		// /proc/net/ipv6_route uses 32 hex zeroes plus prefix length 00 for the
		// default route. Ignore loopback-only entries so the check matches what
		// "ip route show default" would treat as externally relevant routing.
		if fields[9] != loopbackName && fields[0] == strings.Repeat("0", 32) && fields[1] == "00" {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return false, nil
}
