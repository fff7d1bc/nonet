package main

import (
	"os"
	"reflect"
	"syscall"
	"testing"
)

func TestParseTCPForwardSpecsIPv4(t *testing.T) {
	path := writeTempFile(t, "tcp-v4", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0\n"+
		"   1: 00000000:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1 0000000000000000 100 0 0 10 0\n"+
		"   2: 0200007F:1F92 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 3 1 0000000000000000 100 0 0 10 0\n"+
		"   3: 0100007F:1F93 00000000:0000 01 00000000:00000000 00:00000000 00000000  1000        0 4 1 0000000000000000 100 0 0 10 0\n")

	got, err := parseTCPForwardSpecs(path, forwardFamilyIPv4)
	if err != nil {
		t.Fatalf("parseTCPForwardSpecs() error = %v", err)
	}
	want := []tcpForwardSpec{{family: forwardFamilyIPv4, port: 8080}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTCPForwardSpecs() = %v, want %v", got, want)
	}
}

func TestParseTCPForwardSpecsIPv6(t *testing.T) {
	path := writeTempFile(t, "tcp-v6", "  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0000000000000000 100 0 0 10 0\n"+
		"   1: 00000000000000000000000000000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1 0000000000000000 100 0 0 10 0\n")

	got, err := parseTCPForwardSpecs(path, forwardFamilyIPv6)
	if err != nil {
		t.Fatalf("parseTCPForwardSpecs() error = %v", err)
	}
	want := []tcpForwardSpec{{family: forwardFamilyIPv6, port: 8080}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTCPForwardSpecs() = %v, want %v", got, want)
	}
}

func TestEncodeDecodeTCPForwardSpecs(t *testing.T) {
	specs := []tcpForwardSpec{
		{family: forwardFamilyIPv6, port: 443},
		{family: forwardFamilyIPv4, port: 80},
		{family: forwardFamilyIPv4, port: 80},
	}
	encoded := encodeTCPForwardSpecs(specs)
	got, err := decodeTCPForwardSpecs(encoded)
	if err != nil {
		t.Fatalf("decodeTCPForwardSpecs() error = %v", err)
	}
	want := []tcpForwardSpec{
		{family: forwardFamilyIPv4, port: 80},
		{family: forwardFamilyIPv6, port: 443},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeTCPForwardSpecs() = %v, want %v", got, want)
	}
}

func TestEncodeTCPForwardSpecsEmpty(t *testing.T) {
	if got := encodeTCPForwardSpecs(nil); got != "" {
		t.Fatalf("encodeTCPForwardSpecs(nil) = %q, want empty string", got)
	}
}

func TestForwardedFDPassing(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	spec := tcpForwardSpec{family: forwardFamilyIPv4, port: 8080}

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatalf("Socketpair() error = %v", err)
	}
	parentFD := fds[0]
	childFD := fds[1]
	defer syscall.Close(parentFD)

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- sendForwardedFD(childFD, spec, int(readFile.Fd()))
		_ = syscall.Close(childFD)
	}()

	gotSpec, gotFD, err := recvForwardedFD(parentFD)
	if err != nil {
		t.Fatalf("recvForwardedFD() error = %v", err)
	}
	gotFile := os.NewFile(uintptr(gotFD), "nonet-forward-test-pipe")
	if gotFile == nil {
		t.Fatal("NewFile() returned nil")
	}
	defer gotFile.Close()
	if err := <-sendErr; err != nil {
		t.Fatalf("sendForwardedFD() error = %v", err)
	}
	if gotSpec != spec {
		t.Fatalf("recvForwardedFD() spec = %v, want %v", gotSpec, spec)
	}

	if _, err := writeFile.Write([]byte{42}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	var buf [1]byte
	if _, err := gotFile.Read(buf[:]); err != nil {
		t.Fatalf("Read() from received fd error = %v", err)
	}
	if buf[0] != 42 {
		t.Fatalf("received fd read byte = %d, want 42", buf[0])
	}
}

func TestForwardedFDRejectsMalformedRecord(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatalf("Socketpair() error = %v", err)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])

	if err := syscall.Sendmsg(fds[1], []byte{forwardFamilyIPv4, 0}, nil, nil, 0); err != nil {
		t.Fatalf("Sendmsg() error = %v", err)
	}
	if _, _, err := recvForwardedFD(fds[0]); err == nil {
		t.Fatal("recvForwardedFD() error = nil, want malformed-record error")
	}
}

func TestForwardedFDPreservesRecordBoundaries(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	defer readFile.Close()
	defer writeFile.Close()

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET, 0)
	if err != nil {
		t.Fatalf("Socketpair() error = %v", err)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])

	wants := []tcpForwardSpec{
		{family: forwardFamilyIPv4, port: 8080},
		{family: forwardFamilyIPv6, port: 8081},
	}
	for _, want := range wants {
		if err := sendForwardedFD(fds[1], want, int(readFile.Fd())); err != nil {
			t.Fatalf("sendForwardedFD() error = %v", err)
		}
	}
	for _, want := range wants {
		got, fd, err := recvForwardedFD(fds[0])
		if err != nil {
			t.Fatalf("recvForwardedFD() error = %v", err)
		}
		_ = syscall.Close(fd)
		if got != want {
			t.Fatalf("recvForwardedFD() spec = %v, want %v", got, want)
		}
	}
}
