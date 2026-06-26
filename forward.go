package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	tcpListenState = "0A"

	forwardFamilyIPv4 = byte(4)
	forwardFamilyIPv6 = byte(6)
)

type tcpForwardSpec struct {
	family byte
	port   uint16
}

type forwardControl struct {
	parent int
	child  int
}

type forwardBridge struct {
	listeners []*tcpListener
	done      chan error
	closeOnce sync.Once
}

func snapshotOpenTCPForwards() ([]tcpForwardSpec, error) {
	specs, err := parseTCPForwardSpecs("/proc/net/tcp", forwardFamilyIPv4)
	if err != nil {
		return nil, err
	}
	v6, err := parseTCPForwardSpecs("/proc/net/tcp6", forwardFamilyIPv6)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else {
		specs = append(specs, v6...)
	}
	sortTCPForwardSpecs(specs)
	return dedupeTCPForwardSpecs(specs), nil
}

func parseTCPForwardSpecs(path string, family byte) ([]tcpForwardSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var specs []tcpForwardSpec
	scanner := bufio.NewScanner(file)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[3] != tcpListenState {
			continue
		}
		addrHex, port, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		if !isExactLoopbackProcAddr(addrHex, family) {
			continue
		}
		parsedPort, err := strconv.ParseUint(port, 16, 16)
		if err != nil {
			continue
		}
		specs = append(specs, tcpForwardSpec{family: family, port: uint16(parsedPort)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sortTCPForwardSpecs(specs)
	return dedupeTCPForwardSpecs(specs), nil
}

func isExactLoopbackProcAddr(addrHex string, family byte) bool {
	switch family {
	case forwardFamilyIPv4:
		ip, ok := parseProcIPv4(addrHex)
		return ok && ip.Equal(net.IPv4(127, 0, 0, 1))
	case forwardFamilyIPv6:
		ip, ok := parseProcIPv6(addrHex)
		return ok && ip.Equal(net.IPv6loopback)
	default:
		return false
	}
}

func parseProcIPv4(addrHex string) (net.IP, bool) {
	if len(addrHex) != 8 {
		return nil, false
	}
	value, err := strconv.ParseUint(addrHex, 16, 32)
	if err != nil {
		return nil, false
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(value))
	return net.IPv4(raw[0], raw[1], raw[2], raw[3]), true
}

func parseProcIPv6(addrHex string) (net.IP, bool) {
	if len(addrHex) != 32 {
		return nil, false
	}
	raw := make([]byte, net.IPv6len)
	for i := 0; i < 4; i++ {
		part, err := strconv.ParseUint(addrHex[i*8:(i+1)*8], 16, 32)
		if err != nil {
			return nil, false
		}
		// /proc/net/tcp6 prints each 32-bit word in host byte order, so each
		// word must be reversed independently to reconstruct the network-order IP.
		binary.LittleEndian.PutUint32(raw[i*4:(i+1)*4], uint32(part))
	}
	return net.IP(raw), true
}

func sortTCPForwardSpecs(specs []tcpForwardSpec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].family != specs[j].family {
			return specs[i].family < specs[j].family
		}
		return specs[i].port < specs[j].port
	})
}

func dedupeTCPForwardSpecs(specs []tcpForwardSpec) []tcpForwardSpec {
	if len(specs) < 2 {
		return specs
	}
	deduped := specs[:0]
	var last tcpForwardSpec
	for i, spec := range specs {
		if i > 0 && spec == last {
			continue
		}
		deduped = append(deduped, spec)
		last = spec
	}
	return deduped
}

func encodeTCPForwardSpecs(specs []tcpForwardSpec) string {
	if len(specs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, fmt.Sprintf("%d:%d", spec.family, spec.port))
	}
	return strings.Join(parts, ",")
}

func decodeTCPForwardSpecs(encoded string) ([]tcpForwardSpec, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, nil
	}
	var specs []tcpForwardSpec
	for _, part := range strings.Split(encoded, ",") {
		familyText, portText, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("invalid forward spec %q", part)
		}
		family, err := strconv.ParseUint(familyText, 10, 8)
		if err != nil {
			return nil, fmt.Errorf("parse forward family %q: %w", familyText, err)
		}
		if byte(family) != forwardFamilyIPv4 && byte(family) != forwardFamilyIPv6 {
			return nil, fmt.Errorf("unsupported forward family %d", family)
		}
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("parse forward port %q: %w", portText, err)
		}
		specs = append(specs, tcpForwardSpec{family: byte(family), port: uint16(port)})
	}
	sortTCPForwardSpecs(specs)
	return dedupeTCPForwardSpecs(specs), nil
}

func newForwardControl() (*forwardControl, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("create forward control socket: %w", err)
	}
	// The child end must survive into the hidden namespace setup helper. The
	// target command must not inherit it; the C shim closes it before execve.
	syscall.CloseOnExec(fds[0])
	return &forwardControl{parent: fds[0], child: fds[1]}, nil
}

func (c *forwardControl) childFD() int {
	if c == nil {
		return -1
	}
	return c.child
}

func (c *forwardControl) closeParent() {
	if c != nil && c.parent >= 0 {
		_ = syscall.Close(c.parent)
		c.parent = -1
	}
}

func (c *forwardControl) closeChild() {
	if c != nil && c.child >= 0 {
		_ = syscall.Close(c.child)
		c.child = -1
	}
}

func (c *forwardControl) startHostBridge(specs []tcpForwardSpec) (*forwardBridge, error) {
	listeners := make([]*tcpListener, 0, len(specs))
	for _, want := range specs {
		got, listener, err := recvForwardedListener(c.parent)
		if err != nil {
			closeTCPListeners(listeners)
			return nil, err
		}
		if got != want {
			listener.Close()
			closeTCPListeners(listeners)
			return nil, fmt.Errorf("received forwarded listener %s, want %s", formatForwardSpec(got), formatForwardSpec(want))
		}
		listeners = append(listeners, &tcpListener{spec: got, listener: listener})
	}

	bridge := &forwardBridge{
		listeners: listeners,
		done:      make(chan error, 1),
	}
	go bridge.run()
	return bridge, nil
}

func recvForwardedListener(controlFD int) (tcpForwardSpec, *net.TCPListener, error) {
	spec, fd, err := recvForwardedFD(controlFD)
	if err != nil {
		return tcpForwardSpec{}, nil, err
	}
	listener, err := tcpListenerFromFD(fd)
	if err != nil {
		return tcpForwardSpec{}, nil, err
	}
	return spec, listener, nil
}

func recvForwardedFD(controlFD int) (tcpForwardSpec, int, error) {
	var data [3]byte
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := syscall.Recvmsg(controlFD, data[:], oob, 0)
	if err != nil {
		return tcpForwardSpec{}, -1, fmt.Errorf("receive forwarded fd: %w", err)
	}
	if n == 0 {
		return tcpForwardSpec{}, -1, io.EOF
	}
	if n != len(data) {
		return tcpForwardSpec{}, -1, fmt.Errorf("receive forwarded fd: short header")
	}
	messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return tcpForwardSpec{}, -1, fmt.Errorf("parse forwarded fd control message: %w", err)
	}
	for _, message := range messages {
		fds, err := syscall.ParseUnixRights(&message)
		if err != nil {
			continue
		}
		if len(fds) > 0 {
			port := binary.BigEndian.Uint16(data[1:3])
			spec := tcpForwardSpec{family: data[0], port: port}
			return spec, fds[0], nil
		}
	}
	return tcpForwardSpec{}, -1, fmt.Errorf("receive forwarded fd: missing fd")
}

func tcpListenerFromFD(fd int) (*net.TCPListener, error) {
	file := os.NewFile(uintptr(fd), "nonet-forwarded-listener")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open forwarded listener fd")
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("wrap forwarded listener fd: %w", err)
	}
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		listener.Close()
		return nil, fmt.Errorf("forwarded listener is %T, want *net.TCPListener", listener)
	}
	return tcpListener, nil
}

func (b *forwardBridge) run() {
	errCh := make(chan error, len(b.listeners))
	var wg sync.WaitGroup
	for _, listener := range b.listeners {
		wg.Add(1)
		go func(listener *tcpListener) {
			defer wg.Done()
			if err := acceptForwardedTCP(listener); err != nil {
				errCh <- err
			}
		}(listener)
	}

	waitCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitCh)
	}()

	var err error
	select {
	case err = <-errCh:
		b.close()
		<-waitCh
	case <-waitCh:
	}
	b.done <- err
}

func (b *forwardBridge) close() {
	b.closeOnce.Do(func() {
		closeTCPListeners(b.listeners)
	})
}

func (b *forwardBridge) wait() error {
	if b == nil {
		return nil
	}
	return <-b.done
}

func closeTCPListeners(listeners []*tcpListener) {
	for _, listener := range listeners {
		listener.listener.Close()
	}
}

func acceptForwardedTCP(listener *tcpListener) error {
	for {
		conn, err := listener.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go bridgeForwardedConn(listener.spec, conn)
	}
}

func bridgeForwardedConn(spec tcpForwardSpec, namespaceConn net.Conn) {
	defer namespaceConn.Close()

	// The accepted socket belongs to the isolated namespace because the listener
	// was created there. This Dial runs in the original host namespace. Copying
	// between those two sockets is the bridge; no host-network fd is handed to
	// the target command.
	hostConn, err := net.Dial(tcpNetwork(spec.family), net.JoinHostPort(loopbackHost(spec.family), strconv.Itoa(int(spec.port))))
	if err != nil {
		return
	}
	defer hostConn.Close()

	copyBidirectional(namespaceConn, hostConn)
}

func copyBidirectional(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	wg.Wait()
}

func closeWrite(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}
}

func runInternalForwarder(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("%s %s expects control fd, ready fd, and specs", internalFlag, internalForwarderMode)
	}
	controlFD, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("parse forward control fd: %w", err)
	}
	readyFD, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("parse forward ready fd: %w", err)
	}
	specs, err := decodeTCPForwardSpecs(args[2])
	if err != nil {
		return err
	}
	return runNamespaceForwarder(controlFD, readyFD, specs)
}

func runNamespaceForwarder(controlFD, readyFD int, specs []tcpForwardSpec) error {
	if err := setParentDeathSignal(); err != nil {
		return err
	}

	listeners := make([]*tcpListener, 0, len(specs))
	for _, spec := range specs {
		ln, err := net.ListenTCP(tcpNetwork(spec.family), &net.TCPAddr{
			IP:   loopbackIP(spec.family),
			Port: int(spec.port),
		})
		if err != nil {
			return fmt.Errorf("listen on forwarded %s: %w", formatForwardSpec(spec), err)
		}
		defer ln.Close()
		listeners = append(listeners, &tcpListener{spec: spec, listener: ln})
	}

	for _, listener := range listeners {
		if err := sendForwardedListener(controlFD, listener.spec, listener.listener); err != nil {
			return err
		}
	}

	// Readiness is reported only after every listener is bound and handed to the
	// parent. The helper exits after this point, leaving no long-lived child
	// process for launchers such as Steam/Proton to wait on.
	ready := os.NewFile(uintptr(readyFD), "nonet-forward-ready")
	if ready == nil {
		return errors.New("open forward ready pipe")
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		_ = ready.Close()
		return fmt.Errorf("signal forward readiness: %w", err)
	}
	_ = ready.Close()
	return nil
}

type tcpListener struct {
	spec     tcpForwardSpec
	listener *net.TCPListener
}

func sendForwardedListener(controlFD int, spec tcpForwardSpec, listener *net.TCPListener) error {
	file, err := listener.File()
	if err != nil {
		return fmt.Errorf("duplicate forwarded listener fd: %w", err)
	}
	defer file.Close()

	return sendForwardedFD(controlFD, spec, int(file.Fd()))
}

func sendForwardedFD(controlFD int, spec tcpForwardSpec, fd int) error {
	var data [3]byte
	data[0] = spec.family
	binary.BigEndian.PutUint16(data[1:3], spec.port)
	if err := syscall.Sendmsg(controlFD, data[:], syscall.UnixRights(fd), nil, 0); err != nil {
		return fmt.Errorf("send forwarded fd %s: %w", formatForwardSpec(spec), err)
	}
	return nil
}

func tcpNetwork(family byte) string {
	if family == forwardFamilyIPv6 {
		return "tcp6"
	}
	return "tcp4"
}

func loopbackHost(family byte) string {
	if family == forwardFamilyIPv6 {
		return "::1"
	}
	return "127.0.0.1"
}

func loopbackIP(family byte) net.IP {
	if family == forwardFamilyIPv6 {
		return net.IPv6loopback
	}
	return net.IPv4(127, 0, 0, 1)
}

func formatForwardSpec(spec tcpForwardSpec) string {
	return net.JoinHostPort(loopbackHost(spec.family), strconv.Itoa(int(spec.port)))
}

func formatTCPForwardSpecs(specs []tcpForwardSpec) string {
	if len(specs) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		parts = append(parts, formatForwardSpec(spec))
	}
	return strings.Join(parts, ", ")
}

func setParentDeathSignal() error {
	_, _, errno := syscall.Syscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM), 0)
	if errno != 0 {
		return errno
	}
	// If the command process exited between exec and prctl, exit instead of
	// leaving a hidden forwarder detached from the wrapped command lifetime.
	if os.Getppid() == 1 {
		os.Exit(0)
	}
	return nil
}
