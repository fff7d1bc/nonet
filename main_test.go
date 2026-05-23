package main

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestCommandOrShellUsesProvidedCommand(t *testing.T) {
	args := []string{"printf", "ok"}
	if got := commandOrShell(args); !reflect.DeepEqual(got, args) {
		t.Fatalf("commandOrShell() = %v, want %v", got, args)
	}
}

func TestCommandOrShellFallsBackToShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/bash")
	if got := commandOrShell(nil); !reflect.DeepEqual(got, []string{"/bin/bash"}) {
		t.Fatalf("commandOrShell() = %v, want [/bin/bash]", got)
	}
}

func TestCommandOrShellFallsBackToBinSh(t *testing.T) {
	t.Setenv("SHELL", "")

	if got := commandOrShell(nil); !reflect.DeepEqual(got, []string{"/bin/sh"}) {
		t.Fatalf("commandOrShell() = %v, want [/bin/sh]", got)
	}
}

func TestParseCLIRejectsCommandWithSelfTest(t *testing.T) {
	_, err := parseCLI([]string{"--self-test", "echo"})
	if err == nil {
		t.Fatal("parseCLI() error = nil, want error")
	}
}

func TestOnlyLoopback(t *testing.T) {
	if !onlyLoopback([]string{"lo"}) {
		t.Fatal("onlyLoopback([lo]) = false, want true")
	}
	if onlyLoopback([]string{"lo", "eth0"}) {
		t.Fatal("onlyLoopback([lo eth0]) = true, want false")
	}
	if onlyLoopback(nil) {
		t.Fatal("onlyLoopback(nil) = true, want false")
	}
}

func TestIdentityMapContent(t *testing.T) {
	if got := identityMapContent(1000); got != "1000 1000 1\n" {
		t.Fatalf("identityMapContent() = %q, want %q", got, "1000 1000 1\n")
	}
}

func TestChildExitDescription(t *testing.T) {
	if msg, ok := childExitDescription(childExitUnshareNet); !ok || msg == "" {
		t.Fatalf("childExitDescription() = %q, %v, want non-empty message and true", msg, ok)
	}
	if _, ok := childExitDescription(255); ok {
		t.Fatal("childExitDescription(255) reported known exit code")
	}
}

func TestSelfExePath(t *testing.T) {
	if selfExePath != "/proc/self/exe" {
		t.Fatalf("selfExePath = %q, want /proc/self/exe", selfExePath)
	}
	info, err := os.Stat(selfExePath)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", selfExePath, err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatalf("Stat(%q).Mode() = %v, want executable bits", selfExePath, info.Mode())
	}
}

func TestSelfExePathReexecsCurrentBinary(t *testing.T) {
	const helperEnv = "NONET_TEST_SELF_EXE_HELPER"
	if os.Getenv(helperEnv) == "1" {
		t.Log("self-exe helper reached")
		return
	}

	cmd := exec.Command(selfExePath, "-test.run=^TestSelfExePathReexecsCurrentBinary$", "-test.v")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("%s helper reexec error = %v\noutput:\n%s", selfExePath, err, out.String())
	}
	if !strings.Contains(out.String(), "self-exe helper reached") {
		t.Fatalf("%s helper output = %q, want helper marker", selfExePath, out.String())
	}
}

func TestHasDefaultRouteV4(t *testing.T) {
	path := writeTempFile(t, "route-v4", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"+
		"lo\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0\n"+
		"eth0\t00000000\t01010101\t0003\t0\t0\t0\t00000000\t0\t0\t0\n")

	got, err := hasDefaultRouteV4(path)
	if err != nil {
		t.Fatalf("hasDefaultRouteV4() error = %v", err)
	}
	if !got {
		t.Fatal("hasDefaultRouteV4() = false, want true")
	}
}

func TestHasDefaultRouteV6IgnoresLoopbackOnlyEntries(t *testing.T) {
	path := writeTempFile(t, "route-v6", ""+
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200 lo\n"+
		"00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000002 00000000 80200001 lo\n")

	got, err := hasDefaultRouteV6(path)
	if err != nil {
		t.Fatalf("hasDefaultRouteV6() error = %v", err)
	}
	if got {
		t.Fatal("hasDefaultRouteV6() = true, want false")
	}
}

func TestHasDefaultRouteV6DetectsNonLoopbackDefault(t *testing.T) {
	path := writeTempFile(t, "route-v6-default", ""+
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000064 00000000 00000000 00000000 eth0\n")

	got, err := hasDefaultRouteV6(path)
	if err != nil {
		t.Fatalf("hasDefaultRouteV6() error = %v", err)
	}
	if !got {
		t.Fatal("hasDefaultRouteV6() = false, want true")
	}
}

func writeTempFile(t *testing.T, pattern, content string) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), pattern)
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("WriteString() error = %v", err)
	}
	return file.Name()
}
