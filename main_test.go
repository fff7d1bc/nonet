package main

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
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

func TestParseCLIForwardOpenTCP(t *testing.T) {
	cfg, err := parseCLI([]string{"--forward-open-tcp", "--", "echo"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if !cfg.forwardOpenTCP {
		t.Fatal("parseCLI().forwardOpenTCP = false, want true")
	}
	if !reflect.DeepEqual(cfg.command, []string{"echo"}) {
		t.Fatalf("parseCLI().command = %v, want [echo]", cfg.command)
	}
}

func TestParseCLIForwardOpenTCPAlias(t *testing.T) {
	cfg, err := parseCLI([]string{"-F", "echo"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if !cfg.forwardOpenTCP {
		t.Fatal("parseCLI().forwardOpenTCP = false, want true")
	}
}

func TestParseCLIRejectsForwardWithSelfTest(t *testing.T) {
	_, err := parseCLI([]string{"--self-test", "-F"})
	if err == nil {
		t.Fatal("parseCLI() error = nil, want error")
	}
}

func TestParseCLIDebug(t *testing.T) {
	cfg, err := parseCLI([]string{"--debug", "echo"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if !cfg.debug {
		t.Fatal("parseCLI().debug = false, want true")
	}
	if !reflect.DeepEqual(cfg.command, []string{"echo"}) {
		t.Fatalf("parseCLI().command = %v, want [echo]", cfg.command)
	}
}

func TestParseCLIDebugWithForwardOpenTCP(t *testing.T) {
	cfg, err := parseCLI([]string{"--debug", "-F", "--", "echo"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if !cfg.debug {
		t.Fatal("parseCLI().debug = false, want true")
	}
	if !cfg.forwardOpenTCP {
		t.Fatal("parseCLI().forwardOpenTCP = false, want true")
	}
	if !reflect.DeepEqual(cfg.command, []string{"echo"}) {
		t.Fatalf("parseCLI().command = %v, want [echo]", cfg.command)
	}
}

func TestParseCLIRejectsDebugWithSelfTest(t *testing.T) {
	_, err := parseCLI([]string{"--self-test", "--debug"})
	if err == nil {
		t.Fatal("parseCLI() error = nil, want error")
	}
}

func TestParseInternalArgs(t *testing.T) {
	mode, rest, err := parseInternalArgs([]string{internalFlag, internalProbeMode})
	if err != nil {
		t.Fatalf("parseInternalArgs() probe error = %v", err)
	}
	if mode != internalProbeMode {
		t.Fatalf("parseInternalArgs() probe mode = %q, want %q", mode, internalProbeMode)
	}
	if len(rest) != 0 {
		t.Fatalf("parseInternalArgs() probe rest = %v, want empty", rest)
	}

	mode, rest, err = parseInternalArgs([]string{internalFlag, internalForwarderMode, "3", "4", "4:8080"})
	if err != nil {
		t.Fatalf("parseInternalArgs() forwarder error = %v", err)
	}
	if mode != internalForwarderMode {
		t.Fatalf("parseInternalArgs() forwarder mode = %q, want %q", mode, internalForwarderMode)
	}
	if !reflect.DeepEqual(rest, []string{"3", "4", "4:8080"}) {
		t.Fatalf("parseInternalArgs() forwarder rest = %v, want [3 4 4:8080]", rest)
	}
}

func TestParseInternalArgsRejectsMissingMode(t *testing.T) {
	_, _, err := parseInternalArgs([]string{"--internal"})
	if err == nil {
		t.Fatal("parseInternalArgs() error = nil, want error")
	}
}

func TestRunRejectsUnknownInternalMode(t *testing.T) {
	if err := run([]string{"--internal", "unknown"}); err == nil {
		t.Fatal("run() error = nil, want error")
	}
}

func TestParseCLIDoesNotTreatCommandAfterDashDashAsInternal(t *testing.T) {
	cfg, err := parseCLI([]string{"--", "--internal"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if !reflect.DeepEqual(cfg.command, []string{"--internal"}) {
		t.Fatalf("parseCLI().command = %v, want [--internal]", cfg.command)
	}
}

func TestParseCLIDoesNotTreatHelpAfterDashDashAsHelp(t *testing.T) {
	cfg, err := parseCLI([]string{"--", "--help"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if cfg.showHelp {
		t.Fatal("parseCLI().showHelp = true, want false")
	}
	if !reflect.DeepEqual(cfg.command, []string{"--help"}) {
		t.Fatalf("parseCLI().command = %v, want [--help]", cfg.command)
	}
}

func TestParseCLIStopsLookingForHelpAtCommand(t *testing.T) {
	cfg, err := parseCLI([]string{"echo", "--help", "-h", "-help"})
	if err != nil {
		t.Fatalf("parseCLI() error = %v", err)
	}
	if cfg.showHelp {
		t.Fatal("parseCLI().showHelp = true, want false")
	}
	want := []string{"echo", "--help", "-h", "-help"}
	if !reflect.DeepEqual(cfg.command, want) {
		t.Fatalf("parseCLI().command = %v, want %v", cfg.command, want)
	}
}

func TestParseCLIRecognizesLeadingHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "-help"} {
		t.Run(arg, func(t *testing.T) {
			cfg, err := parseCLI([]string{arg})
			if err != nil {
				t.Fatalf("parseCLI() error = %v", err)
			}
			if !cfg.showHelp {
				t.Fatal("parseCLI().showHelp = false, want true")
			}
		})
	}
}

func TestShowSharedProbePasses(t *testing.T) {
	tests := []struct {
		name             string
		forwardTestAddrs string
		want             bool
	}{
		{name: "plain probe", want: true},
		{name: "blank forwarding addrs", forwardTestAddrs: " \t\n", want: true},
		{name: "forwarding probe", forwardTestAddrs: "127.0.0.1:12345", want: false},
		{name: "multiple forwarding addrs", forwardTestAddrs: "127.0.0.1:12345,[::1]:12346", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := showSharedProbePasses(tt.forwardTestAddrs); got != tt.want {
				t.Fatalf("showSharedProbePasses(%q) = %v, want %v", tt.forwardTestAddrs, got, tt.want)
			}
		})
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
	if msg, ok := childExitDescription(childExitForwarderStart); !ok || msg == "" {
		t.Fatalf("childExitDescription() = %q, %v, want non-empty message and true", msg, ok)
	}
	if msg, ok := childExitDescription(childExitLowPortPolicy); !ok || msg == "" {
		t.Fatalf("childExitDescription() = %q, %v, want non-empty message and true", msg, ok)
	}
	if _, ok := childExitDescription(255); ok {
		t.Fatal("childExitDescription(255) reported known exit code")
	}
}

func TestReadChildSetupStatus(t *testing.T) {
	started, err := readChildSetupStatus(strings.NewReader(""))
	if err != nil {
		t.Fatalf("readChildSetupStatus() EOF error = %v", err)
	}
	if !started.targetStarted {
		t.Fatal("readChildSetupStatus() targetStarted = false, want true")
	}

	failed, err := readChildSetupStatus(strings.NewReader(string([]byte{childExitWaitSync})))
	if err != nil {
		t.Fatalf("readChildSetupStatus() code error = %v", err)
	}
	if failed.targetStarted || failed.exitCode != childExitWaitSync {
		t.Fatalf("readChildSetupStatus() = %+v, want setup code %d", failed, childExitWaitSync)
	}
}

func TestClassifyChildStatusDistinguishesTargetExit(t *testing.T) {
	status := syscall.WaitStatus(childExitWaitSync << 8)
	err := classifyChildStatus(status, childSetupStatus{targetStarted: true})
	targetErr, ok := err.(*targetExitError)
	if !ok || targetErr.exitCode != childExitWaitSync {
		t.Fatalf("classifyChildStatus() = %T %v, want target exit %d", err, err, childExitWaitSync)
	}

	err = classifyChildStatus(status, childSetupStatus{exitCode: childExitWaitSync})
	if _, ok := err.(*targetExitError); ok {
		t.Fatalf("classifyChildStatus() = %T, want setup error", err)
	}
}

func TestExitMainPreservesTargetTermination(t *testing.T) {
	const helperEnv = "NONET_TEST_EXIT_MAIN"
	if mode := os.Getenv(helperEnv); mode != "" {
		switch mode {
		case "exit":
			exitMain(&targetExitError{exitCode: childExitWaitSync})
		case "signal":
			exitMain(&targetExitError{signal: syscall.SIGTERM})
		}
		return
	}

	for _, mode := range []string{"exit", "signal"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(selfExePath, "-test.run=^TestExitMainPreservesTargetTermination$")
			cmd.Env = append(os.Environ(), helperEnv+"="+mode)
			err := cmd.Run()
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("helper error = %v, want *exec.ExitError", err)
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatalf("helper status = %T, want syscall.WaitStatus", exitErr.Sys())
			}
			if mode == "exit" && (!status.Exited() || status.ExitStatus() != childExitWaitSync) {
				t.Fatalf("exit helper status = %v, want exit %d", status, childExitWaitSync)
			}
			if mode == "signal" && (!status.Signaled() || status.Signal() != syscall.SIGTERM) {
				t.Fatalf("signal helper status = %v, want SIGTERM", status)
			}
		})
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

func TestHasDefaultRouteAllowsMissingIPv6Routes(t *testing.T) {
	v4Path := writeTempFile(t, "route-v4-no-default", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n")
	missing := v4Path + "-missing"

	got, err := hasDefaultRouteFiles(v4Path, missing)
	if err != nil {
		t.Fatalf("hasDefaultRouteFiles() error = %v", err)
	}
	if got {
		t.Fatal("hasDefaultRouteFiles() = true, want false")
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
