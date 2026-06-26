package main

import (
	"reflect"
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
