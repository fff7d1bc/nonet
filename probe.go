package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func runSelfTest() error {
	uid := os.Getuid()
	gid := os.Getgid()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}

	fmt.Printf("uid=%d gid=%d\n", uid, gid)
	if value, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		fmt.Printf("unprivileged_userns_clone: %s\n", strings.TrimSpace(string(value)))
	}

	if err := runEndToEndProbe(uid, gid, home); err != nil {
		return fmt.Errorf("end-to-end probe failed: %w", err)
	}

	fmt.Println("self-test: ok")
	return nil
}

func runInternalProbe(expectUID, expectGID int, expectHome string) error {
	if expectUID >= 0 && os.Getuid() != expectUID {
		return fmt.Errorf("uid mismatch: got %d want %d", os.Getuid(), expectUID)
	}
	if expectGID >= 0 && os.Getgid() != expectGID {
		return fmt.Errorf("gid mismatch: got %d want %d", os.Getgid(), expectGID)
	}
	if expectHome != "" {
		if _, err := os.ReadDir(expectHome); err != nil {
			return fmt.Errorf("home access check failed for %s: %w", expectHome, err)
		}
	}

	names, err := interfaceNames()
	if err != nil {
		return fmt.Errorf("list interfaces: %w", err)
	}
	if !onlyLoopback(names) {
		return fmt.Errorf("unexpected interfaces present: %s", formatInterfaceList(names))
	}

	flags, err := linkFlags(loopbackName)
	if err != nil {
		return fmt.Errorf("inspect loopback flags: %w", err)
	}
	if flags&syscall.IFF_UP == 0 {
		return errors.New("loopback is not up")
	}
	if err := probeLoopbackTCP(); err != nil {
		return fmt.Errorf("loopback probe failed: %w", err)
	}

	fmt.Printf("probe: uid=%d gid=%d lo=up loopback=tcp-ok\n", os.Getuid(), os.Getgid())
	return nil
}

func runEndToEndProbe(uid, gid int, home string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve self executable: %w", err)
	}
	cmd := exec.Command(self, internalProbeCommand)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%d", internalExpectUIDEnv, uid),
		fmt.Sprintf("%s=%d", internalExpectGIDEnv, gid),
		fmt.Sprintf("%s=%s", internalExpectHomeEnv, home),
	)
	return runWithEnv(cmd.Args, cmd.Env)
}

func probeLoopbackTCP() error {
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
