package main

import (
	"reflect"
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
